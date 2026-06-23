//go:build windows

// watch the Wails main window and force-exit the process
// when the user clicks X — regardless of whether Wails'
// own quit chain runs.
//
// v0.1.8 polled IsWindowVisible. Failed on the
// physical box: Wails v2.12 + Win10 1909 doesn't hide
// OR destroy the window during the X-click handler; the
// window stays around (visible=1, IsWindow=1) while the
// process wedges headlessly. Polling never tripped.
//
// v0.1.9 polled IsWindow. Same problem — Wails leaves
// the hwnd alive (just shows SW_HIDE), so IsWindow
// returns 1 forever. v0.1.9 also never tripped.
//
// v0.1.10: use SetWinEventHook to subscribe to
// EVENT_OBJECT_HIDE and EVENT_OBJECT_DESTROY. The
// Accessibility event hook fires whenever the OS
// reports a window state change — independent of
// Wails' internal state. If Wails calls
// ShowWindow(SW_HIDE) (which it does on X click to
// "soft-close"), EVENT_OBJECT_HIDE fires. If Wails
// gets far enough to call DestroyWindow, we get
// EVENT_OBJECT_DESTROY too. Either way we trip.
//
// The hook callback runs on a Windows internal
// thread. We can't block or call Go runtime from
// there; we set a uint32 atomically and return.
// A small goroutine in runWatchdog sees the flag,
// calls time.AfterFunc(3s, TerminateProcess) once,
// and exits.
//
// Job Object (job_windows.go) reaps msedgewebview2
// children in the same kernel step.

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
	wdProcGetClassNameW          = modUser32.NewProc("GetClassNameW")

	wdProcSetWinEventHook = modUser32.NewProc("SetWinEventHook")
	wdProcUnhookWinEvent  = modUser32.NewProc("UnhookWinEvent")

	wdProcGetCurrentThreadId = modKernel32.NewProc("GetCurrentThreadId")

	wdProcTerminateProcess = modKernel32.NewProc("TerminateProcess")

	currentProcessPseudoHandle = ^uintptr(0)
)

const wailsWindowClass = "wailsWindow"

const hardKillTimeout = 3 * time.Second

// EVENT_OBJECT_HIDE fires when ShowWindow(SW_HIDE) is
// called on a window. Wails v2.12 uses this on X click.
// EVENT_OBJECT_DESTROY fires when DestroyWindow is
// called. We accept either — both mean "user wants to
// close". The constant values come from
// <winuser.h> EVENT_OBJECT_HIDE / EVENT_OBJECT_DESTROY.
const (
	eventObjectHide    = 0x8003
	eventObjectDestroy = 0x8007
)

// closeDetected is flipped to 1 by the event hook
// callback (running on an OS thread) the moment
// Hide/Destroy fires on our window. The watchdog
// goroutine polls this and arms the hard-kill timer
// on the first transition.
var closeDetected uint32

// armHardKill is called from runWatchdog once
// closeDetected flips. Idempotent via CompareAndSwap.
// TerminateProcess is fire-and-forget at the kernel
// level: the OS kills us mid-call, the timer goroutine
// doesn't return.
func armHardKill() {
	if !atomic.CompareAndSwapUint32(&closeDetected, 1, 2) {
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

// eventHookCallback is the SetWinEventHook callback.
// Runs on a Windows internal thread — we must NOT do
// any Go runtime calls, blocking I/O, or anything that
// might be reentrant. Just set the atomic flag and
// return.
//
// Parameters per the WinEventProc signature:
//   hWinEventHook: the hook handle (we ignore)
//   event:         the event code (we filter Hide/Destroy)
//   hwnd:          the window the event is about
//   idObject, idChild: object/child IDs (we ignore)
//   idEventThread: the thread that fired the event
//   dwmsEventTime: timestamp (we ignore)
func eventHookCallback(
	hWinEventHook, event, hwnd, idObject, idChild,
	idEventThread, dwmsEventTime uintptr,
) uintptr {
	if event == eventObjectHide || event == eventObjectDestroy {
		atomic.StoreUint32(&closeDetected, 1)
	}
	return 0
}

// runWatchdog: locate the Wails window, install an
// event hook scoped to that window, then park on a
// short poll of closeDetected. The hook callback (on
// an OS thread) flips closeDetected when Wails hides
// or destroys the window. We arm the hard-kill timer
// on the first transition.
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

	// Install the event hook. The callback runs on
	// a Windows-internal thread (NOT the thread that
	// called SetWinEventHook), so we cannot rely on
	// any Go runtime state from the callback — only
	// atomics. The procPtr must be stable for the
	// hook's lifetime, so we wrap once and reuse.
	procPtr := syscall.NewCallback(eventHookCallback)

	// Process-wide hooks (0, 0) work for us because
	// we filter by hwnd in the callback. We could
	// pass hwnd as the second arg to scope it, but
	// scoping to a specific hwnd isn't a documented
	// guarantee and the filter is cheap. Using 0, 0
	// is the safe path.
	hook, _, _ := wdProcSetWinEventHook.Call(
		eventObjectHide,    // eventMin
		eventObjectDestroy, // eventMax
		0,                  // hmodWinEventProc (NULL = calling process)
		procPtr,            // pfnWinEventProc
		0,                  // idProcess (0 = all)
		0,                  // idThread  (0 = all)
		0, // dwFlags (0 = WINEVENT_OUTOFCONTEXT | WINEVENT_SKIPOWNPROCESS)
	)
	if hook == 0 {
		return
	}
	defer wdProcUnhookWinEvent.Call(hook)

	// Park here. The event callback flips
	// closeDetected; we arm the hard-kill timer on
	// the first 0->1 transition. Poll at 100ms —
	// fast enough that the user doesn't see delay
	// between Hide and the kill timer arming.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if atomic.LoadUint32(&closeDetected) == 1 {
			armHardKill()
			return
		}
	}
}
