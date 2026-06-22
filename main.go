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

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
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
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatalf("innerlink-desktop: %v", err)
	}
}