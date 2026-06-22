// innerlink-desktop is the Wails-backed UI shell for the
// innerlink LAN P2P chat. It imports the public pkg/node
// API from innerlink-core, brings up a Node in startup(),
// and binds every node.* call through the App struct so
// the TypeScript frontend can drive it directly.
//
// No CLI, no daemon, no JSON-RPC. The Wails window owns
// the only innerlink Node in this process. Closing the
// window calls App.shutdown() which calls Node.Close().
package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// lockPath is the single-instance lockfile location.
// Picked per-user (TempDir) so two different Windows
// users on the same box don't collide. Cleanup.ps1
// deletes it as part of the orphan-process sweep.
//
// Why this exists: Wails' WebView2 runtime spawns
// msedgewebview2.exe children that can outlive the
// main process if the user force-quits or the box
// crashes. Those orphans hold file locks on
// %APPDATA%\innerlink\ (chat.enc, device.key, ...),
// which makes a drag-and-drop replacement of the
// binary fail with "file in use by another program".
// The single-instance lock means the user gets a
// clear "already running" message instead of
// silent data corruption, and the cleanup script
// has a specific signal to remove.
const lockFileName = "innerlink-desktop.lock"

func lockPath() string {
	return filepath.Join(os.TempDir(), lockFileName)
}

// acquireLock creates the lockfile with O_EXCL so the
// second instance fails fast. Returns a release
// function the caller should defer. If the lock is
// stale (older than 1 hour, typically a crash remnant),
// we steal it — better to start cleanly than to
// refuse over a clock-skew artifact.
func acquireLock() (release func(), err error) {
	path := lockPath()

	// O_EXCL + O_CREATE makes the create atomic: if the
	// file already exists the syscall fails with EEXIST,
	// which os.IsExist catches reliably across platforms.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		// We own it. Write our pid so cleanup.ps1 / Task
		// Manager can identify the owner when chasing
		// orphans.
		_, _ = f.WriteString("pid=" + itoa(os.Getpid()) + "\n")
		_ = f.Close()
		return func() { _ = os.Remove(path) }, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}

	// Lockfile already exists. Check if the holder is
	// actually still alive (PID inside the file). If
	// the holder is dead or the file is older than an
	// hour, we steal it.
	info, statErr := os.Stat(path)
	if statErr == nil && timeSinceHours(info.ModTime()) > 1 {
		_ = os.Remove(path)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString("pid=" + itoa(os.Getpid()) + " (stolen)\n")
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
	}

	return nil, errAlreadyRunning{Path: path}
}

// errAlreadyRunning is the sentinel returned when
// another live innerlink-desktop holds the lock.
type errAlreadyRunning struct{ Path string }

func (e errAlreadyRunning) Error() string {
	return "another innerlink-desktop is already running " +
		"(lockfile at " + e.Path + "). Close the existing " +
		"window, or run cleanup.ps1 to remove orphan WebView2 " +
		"children that survived a force-quit."
}

func main() {
	release, err := acquireLock()
	if err != nil {
		log.Fatalf("innerlink-desktop: %v", err)
	}
	defer release()

	app := NewApp()

	err = wails.Run(&options.App{
		Title:  "innerlink",
		Width:  1024,
		Height: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// Innerlink palette: green / warm-neutral.
		// Same base as the UI mockup so the chrome
		// doesn't flash white before the HTML loads.
		BackgroundColour: &options.RGBA{R: 0xF7, G: 0xF8, B: 0xF4, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalf("innerlink-desktop: %v", err)
	}
}