//go:build !windows

// No-op stub. On macOS and Linux, Wails doesn't have
// the v2.12 WebView2-shutdown sync bug — WebKit (Cocoa
// on macOS, webkit2gtk on Linux) dies cleanly with the
// parent process. There's no "window destroyed but
// process alive" pathology on those platforms, and the
// m_lExecutable section lock that this whole watchdog
// dance exists to work around simply doesn't exist on
// POSIX (Linux and macOS allow the binary to be replaced
// or unlinked while it's running — the running process
// keeps the old inode until it exits).

package main

func startWindowWatchdog(ownPID uint32) {}
