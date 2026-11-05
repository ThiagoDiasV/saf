//go:build desktop

// The desktop app: SAF in its own window. Wails only draws the window (WebView2 on Windows,
// WebKit on a Mac); SAF answers it on a random port of 127.0.0.1 over plain HTTP, so redirects,
// cookies and downloads work as in a browser. (Wails' own URL scheme was tried first: macOS
// WebKit does not follow redirects on it.)

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const desktopBuild = true

func desktop() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := portableDir(exe)
	store, box, keyText, err := openPortable(dir)
	if err != nil {
		return err
	}
	defer store.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token := rand.Text()
	// Only Host 127.0.0.1: a web page that points its own name at this PC gets nothing.
	inventory, handler := app(store, box, keyText, filepath.Join(dir, dataDirName, "photos"), "127.0.0.1")
	server := newServer(onlyThisWindow(token, handler))
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			slog.Error("SAF server stopped", "err", err)
		}
	}()
	start := fmt.Sprintf("http://%s/abrir?t=%s", listener.Addr(), token)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go purgeDaily(ctx, inventory)

	var window context.Context // Wails' context for the window, set once it starts
	err = wails.Run(&options.App{
		Title:            "SAF – Serviço de Apoio à Farmácia",
		Width:            1280,
		Height:           860,
		MinWidth:         360,
		MinHeight:        560,
		BackgroundColour: options.NewRGB(0xF5, 0xF6, 0xF9), // the page background, so opening doesn't flash
		// Wails' own page only sends the window on to SAF's address.
		AssetServer: &assetserver.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><meta http-equiv="refresh" content="0;url=%s"><title>SAF</title>`, start)
		})},
		OnStartup: func(c context.Context) { window = c },
		// A second double-click brings the open window forward instead of starting another SAF.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "br.edu.unilab.farmacia.saf",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				runtime.WindowUnminimise(window)
				runtime.WindowShow(window)
			},
		},
	})
	// The window is closed: let a save in progress finish before the database closes.
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	return errors.Join(err, server.Shutdown(shutdownCtx))
}
