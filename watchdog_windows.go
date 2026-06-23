//go:build windows

// watch the Wails main window and force-exit the process
// if the user clicks X and Wails' own quit chain wedges.
//
// v0.1.8 tried to be polite: poll IsWindowVisible every
// 200ms, sleep 200ms after it goes 0, then TerminateProcess.
// That worked on the dev VM (where Wails v2.12's main
// loop happened to be in a state that responded to
// runtime.Quit), but it did NOT work on the Win10 1909
// physical box: there, the entire quit chain (OnBeforeClose,
// runtime.Quit, OnShutdown, pump-cancel) silently failed,
// the log file went silent the moment the user clicked X,
// and the process kept the m_lExecutable section on the .exe
// forever.
//
// v0.1.9 first cut used SetWindowSubclass (comctl32 v6).
// That ALSO broke: on a default Wails build with no app
// manifest, comctl32 v6 functions aren't exported — Go's
// syscall.LazyProc panics with "procedure not found" and
// the watchdog goroutine brings the process down on
// startup. Backed out.
//
// v0.1.9 second cut (this file) uses the simplest possible
// signal: poll IsWindow(hwnd). When the user clicks X,
// Wails eventually destroys the window even if it never
// makes it through its own quit chain. Once IsWindow
// returns 0, we know the close gesture is in progress.
// Start a 3s hard-kill timer. If the process is still
// alive 3s later, TerminateProcess(self, 0). Job Object
// (job_windows.go) reaps msedgewebview2 children in the
// same kernel step.
//
// Why 3s: Wails graceful path, when it works, takes
// ~50-200ms. 3s is 15x that — plenty of headroom for slow
// disks or busy systems, but well under the time a user
// would notice a hung close.
//
// Why not IsWindowVisible (v0.1.8): on Win10 1909 the
// Wails window stayed "visible" (IsWindowVisible=1) for
// the entire hung session, so the poll never tripped.
// IsWindow on a destroyed handle is the more reliable
// signal because the kernel invalidates the handle.

package main

import (
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	wdProcEnumWindows            = modUser32.NewProc("EnumWindows")
	wdProcGetWindowThreadProcess = modUser32.NewProc("GetWindowThreadProcessId")
	wdProcIsWindow               = modUser32.NewProc("IsWindow")
	wdProcGetClassNameW          = modUser32.NewProc("GetClassNameW")

	wdProcTerminateProcess = modKernel32.NewProc("TerminateProcess")

	// -1 is the pseudo-handle for "current process", per
	// https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getcurrentprocess
	currentProcessPseudoHandle = ^uintptr(0)
)

const wailsWindowClass = "wailsWindow"

const hardKillTimeout = 3 * time.Second

// hardKillArmed flips to 1 the moment the watchdog
// detects the close gesture (window destroyed). After
// that, the hardKillTimer goroutine will TerminateProcess
// in hardKillTimeout. atomic.Bool would be ideal but
// works on Go 1.19+; uint32 is universal.
var hardKillArmed uint32

// armHardKill starts the one-shot TerminateProcess
// timer. Idempotent — only the first call schedules.
// TerminateProcess is fire-and-forget at the kernel
// level: the OS kills us mid-call, the timer goroutine
// doesn't return.
func armHardKill() {
	if !atomic.CompareAndSwapUint32(&hardKillArmed, 0, 1) {
		return
	}
	time.AfterFunc(hardKillTimeout, func() {
		wdProcTerminateProcess.Call(currentProcessPseudoHandle, 0)
	})
}

// startWindowWatchdog is the public entry point. Called
// from App.startup on Windows; no-op on other platforms
// (see watchdog_other.go).
func startWindowWatchdog(ownPID uint32) {
	if ownPID == 0 {
		return
	}
	go runWatchdog(ownPID)
}

// findWailsWindow enumerates top-level windows owned by
// ownPID and returns the one whose class is "wailsWindow".
// Returns 0 if not found yet.
func findWailsWindow(ownPID uint32) uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var windowPID uint32
		wdProcGetWindowThreadProcess.Call(
			hwnd,
			uintptr(unsafe.Pointer(&windowPID)),
		)
		if windowPID != ownPID {
			return 1
		}
		cls := make([]uint16, 256)
		n, _, _ := wdProcGetClassNameW.Call(
			hwnd,
			uintptr(unsafe.Pointer(&cls[0])),
			uintptr(len(cls)),
		)
		if n == 0 {
			return 1
		}
		var s string
		for i := 0; i < int(n); i++ {
			if cls[i] == 0 {
				break
			}
			s += string(rune(cls[i]))
		}
		if s != wailsWindowClass {
			return 1
		}
		found = hwnd
		return 0
	})
	wdProcEnumWindows.Call(cb, 0)
	return found
}

// runWatchdog: locate the Wails window, then poll
// IsWindow(hwnd) every 200ms. When IsWindow returns 0
// (the user clicked X and Wails destroyed the window
// on its way to wedging), arm the hard-kill timer and
// exit the loop. From there the OS does the rest.
func runWatchdog(ownPID uint32) {
	// Window may not exist yet at OnStartup. Retry
	// for up to 10 seconds.
	deadline := time.Now().Add(10 * time.Second)
	var hwnd uintptr
	for time.Now().Before(deadline) {
		hwnd = findWailsWindow(ownPID)
		if hwnd != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hwnd == 0 {
		return
	}

	// Poll IsWindow. Returns nonzero while the
	// window handle is valid; 0 once it's been
	// destroyed. Wails v2.12 always destroys the
	// window on X click (even when its own quit
	// chain wedges after destroy), so this is the
	// reliable "user wants to close" signal.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		stillWindow, _, _ := wdProcIsWindow.Call(hwnd)
		if stillWindow == 0 {
			armHardKill()
			return
		}
	}
}
