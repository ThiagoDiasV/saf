// Command saf runs SAF, the stock control app of the UNILAB university pharmacy.
//
//	SAF.exe / SAF.app                  the desktop app (desktop build, see desktop.go)
//	saf serve                          development: SAF in the browser at http://127.0.0.1:8080
//	saf create-admin <login> "<nome>"  add an administrator and print a first password
//	saf keygen                         print a new encryption key for SAF_KEY_FILE
//
// Configuration comes from the environment; see docs/PLAN.md §2.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"saf/internal/auth"
	"saf/internal/db"
	"saf/internal/seal"
	"saf/internal/stock"
	"saf/internal/web"
)

// version is set by release builds (-ldflags "-X main.version=1.4.2"); "dev" never updates itself.
var version = "dev"

const usage = `uso:
  saf serve
  saf create-admin <login> "<nome completo>"
  saf keygen`

func main() {
	if len(os.Args) == 1 && desktopBuild { // a double-click on SAF.exe or SAF.app
		if err := desktop(); err != nil {
			showError(err)
			os.Exit(1)
		}
		return
	}
	attachConsole()
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "serve":
		err = serve()
	case len(os.Args) == 4 && os.Args[1] == "create-admin":
		err = createAdmin(os.Args[2], os.Args[3])
	case len(os.Args) == 2 && os.Args[1] == "keygen":
		fmt.Println(seal.NewKey())
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("saf failed", "err", err)
		os.Exit(1)
	}
}

func dataDir() string {
	if dir := os.Getenv("SAF_DATA_DIR"); dir != "" {
		return dir
	}
	return "./data"
}

func openStore(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Join(dir, "photos"), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dir, err)
	}
	return db.Open(dir)
}

// serve runs SAF for development: plain HTTP, reachable from this computer only.
func serve() error {
	// Refuse to start without the key rather than run unable to read or write patient data.
	box, keyText, err := seal.LoadKey(os.Getenv("SAF_KEY_FILE"))
	if err != nil {
		return err
	}
	store, err := openStore(dataDir())
	if err != nil {
		return err
	}
	defer store.Close()
	if err := firstRun(store); err != nil {
		return err
	}

	inventory, handler := app(store, box, filepath.Join(dataDir(), "photos"), web.Config{KeyText: keyText, Version: version})
	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		return err
	}
	return run(inventory, newServer(handler), listener)
}

// firstRun gives an empty database its first-run administrator (auth.SetupLogin), who must set
// up the real one at the first login.
func firstRun(store *sql.DB) error {
	created, err := auth.NewAccounts(store, auth.HashCost).CreateSetupUser(context.Background(), time.Now())
	if created {
		slog.Info("first run: log in as admin (password in the README) to set up the administrator")
	}
	return err
}

// app wires SAF together on an open database; cfg brings what differs between the desktop app
// and development (key text, version, Host, updates).
func app(store *sql.DB, box *seal.Box, photoDir string, cfg web.Config) (*stock.Inventory, http.Handler) {
	inventory := stock.NewInventory(store, box, photoDir)
	cfg.Accounts = auth.NewAccounts(store, auth.HashCost)
	cfg.Inventory = inventory
	return inventory, web.Handler(cfg)
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       120 * time.Second, // photo uploads
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// run serves until the process is told to stop (Ctrl+C, or Windows closing it), with the daily
// 6-month purge; then it finishes in-flight requests so the database closes cleanly.
func run(inventory *stock.Inventory, server *http.Server, listener net.Listener) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	slog.Info("SAF listening", "url", "http://"+listener.Addr().String())
	go repeat(ctx, 24*time.Hour, "6-month purge", purge(inventory))

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	slog.Info("SAF stopped")
	return err
}

// repeat runs task now and then every interval, until ctx ends. A failed run is logged and tried
// again at the next one.
//
//	go repeat(ctx, 24*time.Hour, "6-month purge", purge(inventory))
func repeat(ctx context.Context, interval time.Duration, name string, task func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := task(ctx); err != nil {
			slog.Error(name+" failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// purge deletes photos and first names past six months (LGPD).
func purge(inventory *stock.Inventory) func(context.Context) error {
	return func(ctx context.Context) error {
		result, err := inventory.Purge(ctx, time.Now())
		if err == nil {
			slog.Info("6-month purge", "photos", result.Photos, "names", result.Names, "files", result.Files)
		}
		return err
	}
}

// createAdmin adds an administrator from the command line, e.g. when the only one has forgotten
// their password. The password is random and shown once; they change it in "Alterar minha senha".
func createAdmin(login, name string) error {
	store, err := openStore(dataDir())
	if err != nil {
		return err
	}
	defer store.Close()

	password := rand.Text() // 26 characters, 128 random bits
	accounts := auth.NewAccounts(store, auth.HashCost)
	_, err = accounts.Create(context.Background(),
		auth.NewUser{Name: name, Login: login, Password: password, Role: auth.RoleAdmin}, time.Now())
	if err != nil {
		return err // form.Errors reads "invalid form: login: Este login já existe."
	}
	fmt.Printf("Administrador %s criado.\nSenha inicial: %s\nEntre e troque em \"Alterar minha senha\".\n", login, password)
	return nil
}
