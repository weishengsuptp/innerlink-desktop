//go:build !windows

// No-op stub. On macOS and Linux, Wails doesn't
// spawn msedgewebview2 children — it uses WebKit
// (Cocoa on macOS, webkit2gtk on Linux). WebKit
// child processes die cleanly with the parent
// process and don't hold file handles on the
// binary, so we don't need the Job Object dance.
//
// This keeps `go vet` / `go build` happy on
// non-Windows platforms (CI runs on macOS + Linux
// too) and gives us a single function name to call
// from main.go regardless of OS.

package main

func initJobObject() error {
	return nil
}
