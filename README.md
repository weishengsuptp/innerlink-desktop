# innerlink-desktop

Cross-platform desktop UI for [innerlink-core](https://github.com/weishengsuptp/innerlink-core).

LAN P2P encrypted IM + file transfer. Same WiFi / same subnet, no account, no cloud.

## Stack

- **[Wails](https://wails.io/) v2** — Go backend + system WebView frontend
- **Frontend**: vanilla TS + Vite (no Vue/React framework, kept minimal)
- **Backend**: Go, directly imports `innerlink-core` (no IPC, no daemon)

## Architecture

```
innerlink-desktop/
├── main.go              Wails entry point
├── app.go               App struct: Go methods exposed to JS frontend
├── wails.json           Wails build config
├── go.mod               Imports innerlink-core as a library
└── frontend/
    ├── index.html
    ├── src/
    │   ├── main.ts      UI logic; calls bound Go methods via Wails JS bridge
    │   └── style.css
    └── package.json
```

The Go side imports `innerlink-core/protocol`, `innerlink-core/roster`, etc.,
and exposes high-level methods (`SendText`, `ListPeers`, `SetAlias`, ...) to the
JS frontend via Wails' binding generator. No subprocess, no JSON-RPC, no
daemon. The single innerlink process (managed by Wails) runs the whole thing.

## Status

🚧 Scaffold only — requirements not yet collected, no UI code written.

## Build

Once Wails CLI is installed (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`):

```bash
wails dev          # hot-reload dev mode
wails build        # production binary per OS
```

## License

Apache 2.0 — same as innerlink-core.