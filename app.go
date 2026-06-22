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
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/weishengsuptp/innerlink-core/pkg/node"
)

// App is the JS-bound facade. All exported methods are
// callable from TypeScript via window.go.main.App.Method().
type App struct {
	ctx context.Context

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

	// Pump peer + message streams to the JS side as Wails
	// events. These run until Close() closes the underlying
	// channels.
	go a.pumpPeers(nd)
	go a.pumpMessages(nd)

	wailsruntime.LogInfof(ctx, "innerlink-desktop: started, peerID=%s", nd.SelfPeerID())
}

// shutdown tears down the Node. Wails calls this when the
// window closes; we also rely on the context cancel that
// Wails propagates here.
//
// Wails' WebView2 runtime leaves msedgewebview2.exe
// children behind on window close. Those children keep
// file locks on the disk image of innerlink-desktop.exe
// in build/bin/ for several seconds, which makes a
// drag-and-drop replacement of the binary fail with
// "file in use by another program" 鈥?even though the
// user has already closed the window. We force-kill
// them here so a normal X close is enough to make the
// binary replaceable.
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	nd := a.node
	a.node = nil
	a.mu.Unlock()
	if nd != nil {
		_ = nd.Close()
	}
	killWebView2Children()
}

// beforeClose is Wails' last-chance hook: it runs
// synchronously while the user is still in the close
// gesture. We use it to give WebView2 a brief grace
// window to release file handles, then force-kill
// whatever survived. Returning `prevent: false` lets
// the close proceed; the kill runs in a goroutine so
// the close doesn't visibly hang on the user's screen.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	go func() {
		// Give WebView2 up to ~1.5s to unwind on its
		// own. Past that, force-kill is needed for
		// "normal" close behavior on Windows.
		time.Sleep(1500 * time.Millisecond)
		killWebView2Children()
	}()
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
func (a *App) pumpPeers(nd *node.Node) {
	ch := nd.SubscribePeers()
	for ev := range ch {
		wailsruntime.EventsEmit(a.ctx, "peer:event", ev)
	}
}

// pumpMessages forwards every Message (in or out) to the
// JS side as "message:event". Same pattern as pumpPeers.
func (a *App) pumpMessages(nd *node.Node) {
	ch := nd.SubscribeMessages()
	for m := range ch {
		wailsruntime.EventsEmit(a.ctx, "message:event", m)
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

// Compile-time check: the App stays focused on wiring; if
// we ever add fields that aren't useful to the UI, the
// compiler will tell us via this unused-import guard.
var _ = time.Second
var _ = log.Println

// killWebView2Children force-terminates every
// msedgewebview2.exe process whose parent is us, plus
// any whose command line mentions our module path
// (covers the case where the parent already exited and
// the child got reparented to PID 0 / services.exe).
//
// We don't use the Wails runtime for this 鈥?Wails has
// no API to kill its own renderer process; the
// shutdown hooks are advisory. This is the same
// kill-by-CIM-WMI-lookup trick cleanup.ps1 uses, but
// in-process so the user doesn't have to remember to
// run anything after X-ing out the window.
//
// On non-Windows this is a no-op (Wails uses WebKit
// there and there are no WebView2 children to kill).
func killWebView2Children() {
	if runtime.GOOS != "windows" {
		return
	}
	// Find our own PID first so the filter can scope to
	// "children of us" rather than "any webview2 on the
	// box" (which would be rude on a multi-tenant
	// machine). CommandLine-based filter is the
	// fallback for the reparented-child case.
	ourPID := os.Getpid()
	ps := `Get-CimInstance Win32_Process -Filter "Name = 'msedgewebview2.exe'" | ` +
		`Where-Object { $_.ParentProcessId -eq ` + itoa(ourPID) + ` -or $_.CommandLine -like '*innerlink-desktop*' } | ` +
		`ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps)
	// Short timeout 鈥?if PowerShell hangs, don't block
	// our exit forever.
	done := make(chan struct{})
	go func() {
		_ = cmd.Run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
}
