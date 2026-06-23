// Package main is the innerlink-desktop Wails binding.
//
// The desktop UI is a thin shell around the public pkg/node
// API exposed by innerlink-core. Each Go method here is
// auto-bound to JavaScript by Wails (see
// frontend/wailsjs/go/main/App.* after `wails dev` or
// `wails build`). No JSON-RPC, no separate daemon, no CLI
// wrapping: the Wails window owns the only Node in this
// process and JS calls these methods directly.
//
// Event streams (SubscribePeers / SubscribeMessages) are
// not exposed as JS-callable channels; instead a small
// dispatcher goroutine pumps them into Wails runtime
// events (`peer:event`, `message:event`) so the frontend
// can register Wails.EventsOn() handlers.
package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/weishengsuptp/innerlink-core/pkg/node"
)

// App is the JS-bound facade. All exported methods are
// callable from TypeScript via window.go.main.App.Method().
type App struct {
	ctx context.Context

	// pumpCancel cancels the long-running pumpPeers /
	// pumpMessages goroutines on shutdown. Without this,
	// `for ev := range ch` blocks forever and keeps the
	// Go runtime alive past main()'s return, which in
	// turn keeps the process alive past wails.Run()
	// returning. This was the actual root cause of the
	// "process stays alive after X close" bug: not a
	// Wails main-loop hang, but our own pump goroutines
	// never exiting.
	pumpCancel context.CancelFunc

	mu   sync.Mutex
	node *node.Node // nil before Start, after Close
}

// NewApp constructs an empty App. The actual *node.Node is
// created in startup() once we have the Wails context
// (and therefore a usable data dir + log file).
func NewApp() *App { return &App{} }

// startup wires the runtime context and brings up the
// innerlink-core Node. Data, log, and key paths live under
// the OS-conventional per-user data directory so successive
// runs share the same identity (the SM2 device key persists
// across sessions 鈥?same identity as the CLI's
// <cwd>/.innerlink/device.key, just under a stable path).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Start the window-state watchdog. Wails v2.12 on
	// Windows has a shutdown synchronization bug: the
	// user's X click does hide the window and the
	// WebView2 children do get reaped by the Job
	// Object (set up in main.go), but the Go process
	// itself stays alive headlessly because Wails'
	// main message loop is waiting for a "webview
	// complete" event from Chromium that never arrives.
	// OnBeforeClose / OnShutdown / runtime.Quit /
	// os.Exit goroutines all fail to exit the process
	// in practice.
	//
	// The watchdog finds our own top-level window via
	// EnumWindows (filtered by pid) and polls
	// IsWindowVisible every 200ms. When the user closes
	// the window (Wails hides it via ShowWindow(SW_HIDE)
	// on X click), the watchdog gives Wails 200ms of
	// grace to clean up if it can, then calls
	// kernel32!TerminateProcess on our own pid. This
	// bypasses Wails' main loop entirely; the kernel
	// tears us down. Job Object reaps surviving
	// msedgewebview2 children in the same step.
	//
	// On non-Windows this is a no-op (see
	// watchdog_other.go).
	startWindowWatchdog(uint32(os.Getpid()))

	dataDir, logFile, deviceKey, saveDir := desktopPaths()
	opts := node.Options{
		DataDir:   dataDir,
		DeviceKey: deviceKey,
		SaveDir:   saveDir,
		LogFile:   logFile,
		LogLevel:  "info",
	}
	nd, err := node.New(opts)
	if err != nil {
		wailsruntime.LogErrorf(ctx, "node.New: %v", err)
		return
	}
	if err := nd.Start(ctx); err != nil {
		wailsruntime.LogErrorf(ctx, "node.Start: %v", err)
		_ = nd.Close()
		return
	}

	a.mu.Lock()
	a.node = nd
	a.mu.Unlock()

	// Build a cancellable context for the pump goroutines.
	// OnShutdown calls a.pumpCancel() so `for ev := range ch`
	// can fall through to its `<-ctx.Done()` branch and
	// return. Without this, the pumps block forever on
	// the channel and the Go runtime keeps the process
	// alive past wails.Run() returning — this is what
	// used to leave an orphan innerlink-desktop.exe
	// process behind after every X close.
	pumpCtx, pumpCancel := context.WithCancel(context.Background())
	a.pumpCancel = pumpCancel

	// Pump peer + message streams to the JS side as Wails
	// events. Both goroutines watch pumpCtx.Done() so
	// shutdown can break them out of `range ch` even if
	// the underlying channel hasn't been closed yet.
	go a.pumpPeers(pumpCtx, nd)
	go a.pumpMessages(pumpCtx, nd)

	wailsruntime.LogInfof(ctx, "innerlink-desktop: started, peerID=%s", nd.SelfPeerID())
}

// shutdown tears down the Node. Wails calls this when the
// window closes; we also rely on the context cancel that
// Wails propagates here.
//
// The msedgewebview2.exe children that Wails spawned are
// no longer our problem: main.go attached the process to
// a Windows Job Object with KILL_ON_JOB_CLOSE, so the
// kernel terminates them the moment we exit. No PowerShell
// spawn, no WMI walk, no race window.
//
// On Wails v2.12 + Windows, OnShutdown is sometimes not
// called at all (the v2.12 main loop is hung waiting for
// a webview completion signal that never arrives). The
// os.Exit goroutine here is defense-in-depth for the case
// where it does get called.
//
// We can't rely on the deferred release() in main() to
// remove the lockfile once os.Exit fires (os.Exit skips
// defers), so we remove it here explicitly.
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	nd := a.node
	a.node = nil
	a.mu.Unlock()

	// Cancel the pump goroutines first so they can break
	// out of `for ev := range ch`. Without this they
	// would keep the Go runtime alive past main()'s
	// return, leaving an orphan process holding the
	// m_lExecutable section on our .exe file.
	if a.pumpCancel != nil {
		a.pumpCancel()
	}

	// Then close the Node. nd.Close() unblocks the
	// SubscribePeers / SubscribeMessages channels too,
	// which is what makes the `<-ctx.Done()` branch
	// inside the pumps actually fire.
	if nd != nil {
		_ = nd.Close()
	}

	_ = os.Remove(lockPath())
}

// beforeClose is Wails' last-chance hook. It runs
// synchronously while the user is still in the close
// gesture.
//
// The real fix for the "process stays alive after X
// close" bug isn't here — it's in shutdown() and the
// pump goroutines. This hook just asks Wails to start
// the shutdown sequence: runtime.Quit posts the quit
// message, then returning false lets Wails call our
// shutdown() callback, which cancels the pump context
// and closes the Node. Once those goroutines exit, main()
// can return and the process exits cleanly.
//
// On v2.12 + Windows the Wails main loop occasionally
// hangs on the WebView2 async cleanup. In that case the
// watchdog in watchdog_windows.go detects the hidden
// window and TerminateProcesses us as a last resort. So
// this hook is a hint, not a guarantee.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	wailsruntime.Quit(ctx)
	return false
}

// -----------------------------------------------------------------------
// Read-side methods (return JSON to JS).
// -----------------------------------------------------------------------

// SelfPeerID returns our own 32-char hex SM2-derived ID.
// The frontend shows it in the status bar / about dialog.
func (a *App) SelfPeerID() string {
	nd := a.getNode()
	if nd == nil {
		return ""
	}
	return nd.SelfPeerID()
}

// ListPeers returns the current roster snapshot for the
// sidebar. Each entry mirrors pkg/node.PeerInfo.
func (a *App) ListPeers() []node.PeerInfo {
	nd := a.getNode()
	if nd == nil {
		return nil
	}
	return nd.ListPeers()
}

// History returns the chat log with `peerRef` (alias or
// hex PeerID). The UI calls this when the user opens a
// conversation to backfill the message list.
func (a *App) History(peerRef string) []node.Message {
	nd := a.getNode()
	if nd == nil {
		return nil
	}
	return nd.History(peerRef)
}

// ListAliases returns the local alias table for the
// "manage names" UI. Empty-name rows (placeholder Touch
// entries) are kept so the user can see "this peer
// exists, name it" hints.
func (a *App) ListAliases() []node.Alias {
	nd := a.getNode()
	if nd == nil {
		return nil
	}
	return nd.ListAliases()
}

// -----------------------------------------------------------------------
// Action-side methods (JS calls, no return value).
// -----------------------------------------------------------------------

// SendText sends a chat line to `peerRef`. Returns an
// error string if the send fails (empty string on
// success). The UI surfaces the error in a toast.
func (a *App) SendText(peerRef, text string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	if err := nd.SendText(peerRef, text); err != nil {
		return err.Error()
	}
	return ""
}

// SendFile offers `path` to `peerRef`. The receiving side
// auto-accepts into the configured SaveDir (the core has
// no confirm-on-receive hook 鈥?see docs/PRD.md).
func (a *App) SendFile(peerRef, path string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	if err := nd.SendFile(peerRef, path); err != nil {
		return err.Error()
	}
	return ""
}

// SetAlias names `peerRef` (alias or hex PeerID) as `name`.
// Persisted to aliases.json. Empty name is rejected.
func (a *App) SetAlias(peerRef, name string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	if err := nd.SetAlias(peerRef, name); err != nil {
		return err.Error()
	}
	return ""
}

// RemoveAlias clears the alias for `peerRef`. The peer
// remains in the roster (visible by hex ID) but the
// friendly name goes away.
func (a *App) RemoveAlias(peerRef string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	if err := nd.RemoveAlias(peerRef); err != nil {
		return err.Error()
	}
	return ""
}

// Scan kicks off a one-shot TCP-probe of `cidr` (e.g.
// "192.168.40.0/24"). Returns immediately; results land
// as peer:event transitions on the JS event bus.
func (a *App) Scan(cidr string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	go func() {
		if err := nd.Scan(a.ctx, cidr); err != nil {
			wailsruntime.LogErrorf(a.ctx, "scan %s: %v", cidr, err)
		}
	}()
	return ""
}

// DialAddr forces a TCP dial to `addr` ("ip:port"). Use
// for cross-subnet peers that UDP broadcast can't reach.
// Returns immediately; success lands as PeerOnline.
func (a *App) DialAddr(addr string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	if err := nd.DialAddr(addr); err != nil {
		return err.Error()
	}
	return ""
}

// Ping sends a protocol Ping envelope; the peer replies
// with Pong. We don't surface the reply to JS today (the
// protocol handles it internally); this method exists so
// the UI can offer a "is the peer alive?" button.
func (a *App) Ping(peerRef string) string {
	nd := a.getNode()
	if nd == nil {
		return "node not started"
	}
	if err := nd.Ping(peerRef); err != nil {
		return err.Error()
	}
	return ""
}

// -----------------------------------------------------------------------
// Event pumps 鈥?translate pkg/node channels into Wails events.
// -----------------------------------------------------------------------

// pumpPeers forwards every PeerEvent from the core to the
// JS side as a Wails "peer:event" with the struct as the
// event payload. The frontend listens via
// EventsOn("peer:event", cb).
//
// Returns when either:
//   - the SubscribePeers channel is closed (Node.Close
//     was called), or
//   - pumpCtx is cancelled (OnShutdown).
//
// Without the pumpCtx branch, `for ev := range ch` would
// block forever if the channel isn't closed yet, and the
// Go runtime would keep the process alive past main()'s
// return — leaving an orphan innerlink-desktop.exe.
func (a *App) pumpPeers(pumpCtx context.Context, nd *node.Node) {
	ch := nd.SubscribePeers()
	for {
		select {
		case <-pumpCtx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			wailsruntime.EventsEmit(a.ctx, "peer:event", ev)
		}
	}
}

// pumpMessages forwards every Message (in or out) to the
// JS side as "message:event". Same pattern as pumpPeers.
func (a *App) pumpMessages(pumpCtx context.Context, nd *node.Node) {
	ch := nd.SubscribeMessages()
	for {
		select {
		case <-pumpCtx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			wailsruntime.EventsEmit(a.ctx, "message:event", m)
		}
	}
}

// -----------------------------------------------------------------------
// Internals.
// -----------------------------------------------------------------------

// getNode returns the live *node.Node or nil if startup
// failed / shutdown ran. Every exported method routes
// through this so we never panic on a torn-down Node.
func (a *App) getNode() *node.Node {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.node
}

// desktopPaths returns the per-user data dir + log file +
// device key + incoming-files save dir for the current
// OS. We keep these outside the install dir so users
// don't lose identity across updates / uninstalls.
//
// On Windows: %APPDATA%\innerlink\ (dataDir), with logs/
// next to it and received/ for inbound files.
// On macOS:   ~/Library/Application Support/innerlink/
// On Linux:   $XDG_DATA_HOME/innerlink (default
//             ~/.local/share/innerlink).
//
// The data dir layout inside (device.key, aliases.json,
// chat.enc, roster.json) matches what pkg/node expects
// via the existing internal/paths package 鈥?we just
// anchor it at a stable OS location instead of cwd.
func desktopPaths() (dataDir, logFile, deviceKey, saveDir string) {
	// os.UserConfigDir returns:
	//   Windows: %APPDATA% = C:\Users\<user>\AppData\Roaming
	//   macOS:   ~/Library/Application Support
	//   Linux:   $XDG_CONFIG_HOME or ~/.config
	//
	// For Linux we actually want $XDG_DATA_HOME (data, not
	// config) but for a per-user single-app the distinction
	// is mostly aesthetic. UserConfigDir is good enough.
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		// Last-ditch fallback: cwd-relative so we still
		// boot even if os.UserConfigDir returns empty.
		base = "."
	}
	dataDir = filepath.Join(base, "innerlink")
	logFile = filepath.Join(dataDir, "innerlink.log")
	deviceKey = filepath.Join(dataDir, "device.key")
	saveDir = filepath.Join(dataDir, "received")
	return
}

// (killWebView2Children used to live here. It spawned
// PowerShell from a `-H windowsgui` Wails binary, which
// flashed a console window at the user on every X close,
// AND raced with Wails' own shutdown so it didn't always
// find the children in time. Replaced by a Windows Job
// Object in main.go: see job_windows.go for the new
// implementation. Same idea (kill the WebView2 children
// on exit), but kernel-level, race-free, and silent.)
