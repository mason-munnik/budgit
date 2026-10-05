// Command budgit-app is budgit as a desktop window. The dashboard runs in the
// OS's own web view and calls internal/app's Go methods directly; it opens no
// network port. Build the .app with `wails build` from this directory.
package main

import (
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/mason-munnik/budgit/internal/app"
	"github.com/mason-munnik/budgit/internal/store"
)

func main() {
	assets, err := app.Frontend()
	if err != nil {
		fmt.Fprintln(os.Stderr, "budgit:", err)
		os.Exit(1)
	}
	a := app.New(store.DefaultPath())

	err = wails.Run(&options.App{
		Title:            "budgit",
		Width:            1200,
		Height:           860,
		MinWidth:         720,
		MinHeight:        520,
		AssetServer:      &assetserver.Options{Assets: assets},
		Bind:             []any{a},
		BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 255},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "budgit:", err)
		os.Exit(1)
	}
}
