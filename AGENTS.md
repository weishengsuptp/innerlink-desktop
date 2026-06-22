# innerlink-desktop — Agent Notes

## Locked decisions (2026-06-22, do not flip without user consent)

- **Stack: Wails v2** — Go backend + system WebView (Edge/WKWebView/WebKitGTK).
  No Tauri, no Electron. Reason: Wails lets the Go backend import
  innerlink-core directly and bind methods to JS — zero IPC layer, no daemon.
- **No framework on frontend**: vanilla TS + Vite. No Vue/React/Svelte.
  Reason: minimum deps, fastest cold start, fits the "极简主义" project ethos.
- **No daemon / no subprocess / no JSON-RPC.** innerlink-core is the
  single-process runtime; the UI talks to it through Wails' Go↔JS binding.
- **One repo, three platforms**: Win + macOS + Linux from the same Wails
  codebase. Cross-compile from a Mac host for release builds.
- **License: Apache 2.0**, matching innerlink-core.

## Project layout (planned)

```
D:\innerlink-desktop\
├── main.go              Wails entry; calls wails.Run with options.App{...App}
├── app.go               App struct + bound methods (SendText, ListPeers, ...)
├── wails.json           Build config (name, output binary name per platform)
├── go.mod               go mod init github.com/weishengsuptp/innerlink-desktop
│                         require github.com/weishengsuptp/innerlink-core v0.x
├── build/               Icons + platform resources
└── frontend/
    ├── index.html
    ├── package.json     Vite + TS deps only
    ├── tsconfig.json
    ├── vite.config.ts
    └── src/
        ├── main.ts      Bootstrap; subscribes to backend events
        ├── style.css    Minimal styling
        └── (more files added as features land)
```

## innerlink-core integration (TBD pending requirements)

The backend exposes Go methods to JS by binding them on the App struct:

```go
// app.go (sketch, not real yet)
func (a *App) ListPeers() ([]Peer, error) {
    return a.node.Roster().Snapshot(), nil
}

func (a *App) SendText(peerID, text string) error {
    return a.node.Send(peerID, text)
}
```

```ts
// frontend/src/main.ts (sketch)
import { ListPeers, SendText } from '../wailsjs/go/main/App';
const peers = await ListPeers();
await SendText('alice', 'hello');
```

Exact API surface depends on user requirements — TBD.

## CI (planned)

GitHub Actions on push: build Wails app on Ubuntu + macOS + Windows.
Wails CLI installed in CI via `go install`. Artifacts: per-platform binaries.

## Gotchas to remember

- Wails requires Node + npm on dev machine (not needed at runtime, only for
  building the frontend bundle). If user wants to dev without Node, that's
  a blocker — confirm before adding dev tooling.
- Cross-compile: Mac→Win+Linux easy; Win→others hard; Linux→Win needs MinGW.
  Release builds should happen on a Mac host.
- Win10 1909 EOL machines need WebView2 runtime — pre-installed on 1809+.
- innerlink-core's go.mod requires `golang.org/x/sys` as DIRECT dep (see
  innerlink-core AGENTS.md "Build-tag dependency pitfall"). When we add
  innerlink-core as a dependency here, that propagates.