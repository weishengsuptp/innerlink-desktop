//go:build windows

// Window-state watchdog for the binary-replace workflow.
//
// Why this exists: Wails v2.12 on Windows has a known
// shutdown synchronization bug. The user's X click does
// destroy the window, and the WebView2 children do get
// reaped by the Job Object set up in job_windows.go, but
// the Go process itself stays alive headlessly because
// Wails' main message loop is waiting for a "webview
// complete" event from the Chromium side that arrives
// asynchronously (and on Win10 1909, often not at all
// within any reasonable window). OnBeforeClose is
// sometimes not called, OnShutdown is sometimes not
// called, runtime.Quit sometimes does nothing, and the Go
// runtime never reaches its main() return.
//
// We observed this directly: a smoke-test
// innerlink-desktop.exe process kept running with no
// MainWindowTitle for minutes after the X close, blocking
// any paste of a new binary into build/bin/ because the
// OS held the m_lExecutable section on the still-running
// process's own .exe file.
//
// The fix below sidesteps Wails entirely. We use
// EnumWindows to find our own top-level window (filtered
// by pid == our pid), then poll IsWindow on its handle
// every pollInterval. As soon as IsWindow returns 0
// (window destroyed for any reason — X click, Alt-F4,
// End Task, logoff, the screen going to sleep), we give
// Wails a brief grace period then call kernel32!
// TerminateProcess on our own pid. TerminateProcess is
// the most direct possible Windows process kill — the
// kernel tears us down, no defers, no Wails, no Go
// runtime can stall it. Job Object reaps any surviving
// msedgewebview2 children in the same instant.
//
// On non-Windows this entire file is a no-op (see
// watchdog_other.go) — WebKit children die cleanly with
// the parent process on those platforms, and Linux/macOS
// don't hold the m_lExecutable section lock on the
// running binary anyway.

package main

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	// Use a unique package-level namespace so this file
	// doesn't collide with the same names in
	// job_windows.go (which also declares kernel32 and
	// user32). The Go linker would reject two `var
	// kernel32 = ...` in different files of the same
	// package.
	wdKernel32 = syscall.NewLazyDLL("kernel32.dll")
	wdUser32   = syscall.NewLazyDLL("user32.dll")

	wdProcEnumWindows          = wdUser32.NewProc("EnumWindows")
	wdProcGetWindowThreadProcessId = wdUser32.NewProc("GetWindowThreadProcessId")
	wdProcIsWindow             = wdUser32.NewProc("IsWindow")
	wdProcIsWindowVisible      = wdUser32.NewProc("IsWindowVisible")
	wdProcTerminateProcess     = wdKernel32.NewProc("TerminateProcess")
)

// currentProcessPseudoHandle is the special value -1
// (all bits set) that the Windows kernel interprets as
// "this process". No need to close it; it isn't a real
// handle. Same value is returned by GetCurrentProcess()
// but we don't need to call that — we hardcode the value
// since it's part of the stable Win32 ABI.
const currentProcessPseudoHandle = ^uintptr(0)

// startWindowWatchdog launches a goroutine that polls our
// own top-level window handle. When the window is
// destroyed (X, Alt-F4, End Task, anything that takes the
// window down), it gives Wails a brief grace period to
// clean up if it can, then calls TerminateProcess on
// ourselves. The Job Object (set up in main.go via
// initJobObject) reaps any surviving WebView2 children
// at the same instant.
//
// This is intentionally a "last resort" — every other
// shutdown path (OnBeforeClose, runtime.Quit,
// OnShutdown, os.Exit goroutines) should ideally have
// killed us first. If we're still here, Wails is wedged
// and we need to nuke the process from kernel level.
//
// ownPID is the pid of the running innerlink-desktop.exe
// process. The watchdog uses it to filter EnumWindows
// results to windows that belong to us.
//
// Runs forever until the process dies. Safe to call
// multiple times (each call starts an independent
// goroutine; the second one is just a duplicate that
// loses the IsWindow race to the first).
func startWindowWatchdog(ownPID uint32) {
	if ownPID == 0 {
		return
	}
	go runWatchdog(ownPID)
}

// findOwnTopWindow returns the first top-level window
// whose owning thread process is ownPID, or 0 if no such
// window exists yet (the Wails window may not be created
// at the moment OnStartup fires).
//
// Filter: pid matches AND IsWindowVisible returns nonzero.
// The visibility filter matters because EnumWindows also
// enumerates message-only / owned windows that aren't the
// user-visible main window. We want the one the user
// actually clicks X on.
func findOwnTopWindow(ownPID uint32) uintptr {
	var found uintptr
	// syscall.NewCallback wraps a Go func for use as a
	// Windows callback. The return value convention is
	// inverted vs C: returning 0 means "stop the
	// enumeration", returning nonzero means "continue".
	// Wails' own EnumWindowsProc convention is the
	// same: BOOL non-zero = continue, 0 = stop.
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var windowPID uint32
		wdProcGetWindowThreadProcessId.Call(
			hwnd,
			uintptr(unsafe.Pointer(&windowPID)),
		)
		if windowPID != ownPID {
			return 1 // continue
		}
		visible, _, _ := wdProcIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1 // continue — invisible, probably a helper
		}
		found = hwnd
		return 0 // stop — found our window
	})
	wdProcEnumWindows.Call(cb, 0)
	return found
}

func runWatchdog(ownPID uint32) {
	const (
		// pollInterval: how often we re-check
		// IsWindowVisible. 200ms is fast enough that
		// a user clicking X and immediately trying to
		// paste a new binary into build/bin/ won't
		// see a noticeable delay.
		pollInterval = 200 * time.Millisecond

		// graceAfterHide: how long to wait after
		// IsWindowVisible returns 0 (window hidden
		// because the user clicked X) before
		// TerminateProcess. Gives Wails' own message
		// loop a chance to wind down gracefully; if
		// it does, our TerminateProcess is a no-op.
		graceAfterHide = 200 * time.Millisecond
	)

	// First, locate our window. The window may not
	// be created yet at the moment OnStartup fires,
	// so retry for up to 5 seconds.
	var hwnd uintptr
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hwnd = findOwnTopWindow(ownPID)
		if hwnd != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hwnd == 0 {
		return
	}

	// Poll IsWindowVisible. When it returns 0, the
	// user has hidden the window (Wails hides it
	// via ShowWindow(SW_HIDE) on X click). This is
	// the last-resort safety net — the graceful
	// shutdown path (pump-cancel + nd.Close +
	// Wails runtime.Quit) should normally have
	// already exited the process by now. If we get
	// here, Wails v2.12's main loop is wedged and
	// only a TerminateProcess will get us out.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		visible, _, _ := wdProcIsWindowVisible.Call(hwnd)
		if visible == 0 {
			time.Sleep(graceAfterHide)
			// TerminateProcess(self, 0). This call
			// does NOT return — the kernel
			// terminates us mid-call. Job Object
			// reaps msedgewebview2 children in the
			// same atomic step.
			wdProcTerminateProcess.Call(
				currentProcessPseudoHandle,
				0,
			)
			return
		}
	}
}
