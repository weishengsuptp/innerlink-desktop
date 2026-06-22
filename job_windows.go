//go:build windows

// Windows Job Object wrapper.
//
// We attach our process to a Job Object configured with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. When the job
// handle is closed — which happens automatically when
// our process exits, even via hard kill (Stop-Process,
// taskkill /F, segfault, power loss) — the Windows
// kernel terminates every process assigned to the job.
// Since child processes default-inherit their parent's
// job, every msedgewebview2.exe child that Wails spawns
// gets the same treatment. No polling, no WMI, no
// PowerShell, no race window.
//
// This is the same pattern Chrome / Firefox / Visual
// Studio / VS Code (Electron) / Tauri use on Windows.
// Wails itself does not provide it; each app has to
// wire it up.
//
// Two important rules:
//
//  1. Create the job BEFORE wails.Run() so the WebView2
//     children that Wails spawns inherit it. If you
//     create the job in OnStartup, the children are
//     already orphaned and the assignment is moot.
//
//  2. Keep `jobHandle` alive for the entire process
//     lifetime. Once the last handle to a job is
//     closed, the job is destroyed and the limit
//     stops applying. The global variable is the
//     anchor — Do not close it.

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

const (
	// JobObjectExtendedLimitInformation is the
	// information class for SetInformationJobObject
	// that carries LimitFlags. Value from MSDN.
	jobObjectExtendedLimitInformation = 9

	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: when the
	// last handle to the job is closed, terminate
	// all processes in the job.
	jobObjectLimitKillOnJobClose = 0x00002000
)

// JOBOBJECT_BASIC_LIMIT_INFORMATION — the limit-flags
// struct embedded inside the extended info struct.
// Layout must match the Windows SDK; unsafe.Sizeof
// checks at build time keep us honest.
type jobobjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

// IO_COUNTERS — required filler in the extended info
// struct, between the basic info and the memory limits.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// JOBOBJECT_EXTENDED_LIMIT_INFORMATION — what
// SetInformationJobObject wants when info class is
// JobObjectExtendedLimitInformation. We only set
// LimitFlags; the rest stays zero.
type jobobjectExtendedLimitInformation struct {
	BasicLimitInformation jobobjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// jobHandle is the anchor that keeps the job alive.
// A zero value here would let the runtime close the
// handle when this package's last reference goes out
// of scope, which would destroy the job and silently
// disable the kill-on-close behavior. The init() below
// sets it once and never touches it again.
var jobHandle syscall.Handle

// initJobObject creates the job, sets the kill-on-close
// limit, and assigns the current process to it. Returns
// nil on success. On failure, returns the error but does
// not abort — the app still runs, it just won't get the
// auto-cleanup guarantee.
func initJobObject() error {
	// CreateJobObjectW(NULL, NULL) — unnamed job,
	// owned by our process. Second arg is the name;
	// NULL means anonymous.
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return fmt.Errorf("CreateJobObjectW: %w", err)
	}
	jobHandle = syscall.Handle(h)

	// SetInformationJobObject with
	// JobObjectExtendedLimitInformation. We only
	// need LimitFlags set; everything else stays 0.
	var info jobobjectExtendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose

	ret, _, err := procSetInformationJobObject.Call(
		uintptr(jobHandle),
		uintptr(jobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
	)
	if ret == 0 {
		_ = syscall.CloseHandle(jobHandle)
		jobHandle = 0
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}

	// AssignProcessToJobObject(job, GetCurrentProcess()).
	// GetCurrentProcess returns a pseudo-handle (-1)
	// that means "this process" — no need to close it.
	//
	// Edge case: if we're already inside another job
	// (e.g. launched from a debugger or a test harness
	// that wraps its child in a job), this call fails
	// with ERROR_ACCESS_DENIED. We treat that as a
	// soft failure: log it but keep going. The user
	// can still rely on cleanup.ps1 as a fallback.
	//
	// Note: a process can only belong to one job at
	// a time, so we cannot stack.
	currentProcess := uintptr(^uintptr(0)) // -1 = GetCurrentProcess pseudo-handle
	ret, _, err = procAssignProcessToJobObject.Call(
		uintptr(jobHandle),
		currentProcess,
	)
	if ret == 0 {
		// 5 = ERROR_ACCESS_DENIED means we were
		// already in another job. Don't tear down
		// the job we just created — that would also
		// kill the outer job's children. Just leave
		// it unassigned and move on.
		if errno, ok := err.(syscall.Errno); ok && errno == 5 {
			return nil
		}
		_ = syscall.CloseHandle(jobHandle)
		jobHandle = 0
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}

	return nil
}
