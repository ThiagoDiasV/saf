//go:build desktop

// The desktop app: SAF in its own window. Wails only draws the window (WebView2 on Windows,
// WebKit on a Mac); SAF answers it on a random port of 127.0.0.1 over plain HTTP, so redirects,
// cookies and downloads work as in a browser. (Wails' own URL scheme was tried first: macOS
// WebKit does not follow redirects on it.)

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"saf/internal/db"
	"saf/internal/update"
	"saf/internal/web"
)

const desktopBuild = true

// waitPIDVar names the SAF that restarted into this one: wait for it to close before opening.
const waitPIDVar = "SAF_WAIT_PID"

func desktop() error {
	if pid, err := strconv.Atoi(os.Getenv(waitPIDVar)); err == nil {
		waitForExit(pid, 30*time.Second)
	}
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
	var window atomic.Pointer[context.Context] // Wails' context for the window, set once it starts
	var restart atomic.Bool
	updates := newUpdates(exe, func() {
		restart.Store(true)
		runtime.Quit(*window.Load())
	})
	inventory, handler := app(store, box, filepath.Join(dir, dataDirName, "photos"), web.Config{
		KeyText: keyText,
		Version: version,
		Domain:  "127.0.0.1", // a web page that points its own name at this PC gets nothing
		Updates: updates,
	})
	server := newServer(onlyThisWindow(token, handler))
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			slog.Error("SAF server stopped", "err", err)
		}
	}()
	start := fmt.Sprintf("http://%s/abrir?t=%s", listener.Addr(), token)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go repeat(ctx, 24*time.Hour, "6-month purge", purge(inventory))
	dataDir := filepath.Join(dir, dataDirName)
	go repeat(ctx, time.Hour, "backup", backup(store, dataDir))

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
		OnStartup: func(c context.Context) {
			window.Store(&c)
			// Here, not earlier: only the SAF that keeps running gets here, so a second
			// double-click never downloads or swaps SAF.exe.
			if updates != nil {
				go repeat(ctx, 24*time.Hour, "update check", updates.Check)
			}
		},
		// A second double-click brings the open window forward instead of starting another SAF.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "br.edu.unilab.farmacia.saf",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				runtime.WindowUnminimise(*window.Load())
				runtime.WindowShow(*window.Load())
			},
		},
	})
	// The window is closed: let a save in progress finish before the database closes.
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	err = errors.Join(err, server.Shutdown(shutdownCtx))
	// One more copy as SAF closes, so the last hour's work is in backups/ too.
	err = errors.Join(err, backup(store, dataDir)(context.Background()))
	if restart.Load() {
		cmd := exec.Command(exe) // the installed version, under the same name
		cmd.Env = append(os.Environ(), waitPIDVar+"="+strconv.Itoa(os.Getpid()))
		err = errors.Join(err, cmd.Start())
	}
	return err
}

// backup copies the database into dataDir/backups when it changed (db.Backup); Google Drive
// carries that folder off the PC.
func backup(store *sql.DB, dataDir string) func(context.Context) error {
	return func(context.Context) error {
		written, err := db.Backup(store, dataDir, time.Now())
		if written != "" {
			slog.Info("backup", "file", written)
		}
		return err
	}
}

// newUpdates returns the updater for a release build on Windows. Development builds ("dev") and
// the Mac build don't update themselves.
func newUpdates(exe string, restart func()) *update.Manager {
	current, err := update.ParseVersion(version)
	if err != nil || goruntime.GOOS != "windows" {
		return nil
	}
	source, err := update.GitHub()
	if err != nil {
		slog.Error("updates off", "err", err)
		return nil
	}
	return &update.Manager{Source: source, Current: current, Exe: exe, Restart: restart}
}
