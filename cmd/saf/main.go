// Command saf runs SAF, the stock control app of the UNILAB university pharmacy.
//
//	saf                                local trial: sample data, this computer only (trial.go)
//	saf serve                          start the web server on :8080
//	saf create-admin <login> "<nome>"  create an administrator and print a first password
//	saf keygen                         print a new encryption key for SAF_KEY_FILE
//
// Configuration comes from the environment; see docs/PLAN.md §2.
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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"saf/internal/auth"
	"saf/internal/db"
	"saf/internal/seal"
	"saf/internal/stock"
	"saf/internal/web"
)

const usage = `uso:
  saf                       versão de teste neste computador
  saf serve
  saf create-admin <login> "<nome completo>"
  saf keygen`

func main() {
	var err error
	switch {
	case len(os.Args) == 1: // a double-click
		if err := trial(); err != nil {
			waitBeforeClosing(err)
			os.Exit(1)
		}
		return
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

func serve() error {
	// Refuse to start without the key rather than run unable to read or write patient data.
	box, err := seal.LoadKey(os.Getenv("SAF_KEY_FILE"))
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

	domain := os.Getenv("SAF_DOMAIN")
	inventory, handler := app(store, box, filepath.Join(dataDir(), "photos"), domain)
	if domain == "" { // local work: plain HTTP
		plain, err := listen(":8080", newServer(handler), false)
		if err != nil {
			return err
		}
		return run(inventory, plain)
	}
	secure, redirect := httpsServers(handler, domain, filepath.Join(dataDir(), "autocert"))
	https, err := listen(":8443", secure, true)
	if err != nil {
		return err
	}
	http80, err := listen(":8080", redirect, false)
	if err != nil {
		https.listener.Close()
		return err
	}
	return run(inventory, https, http80)
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

// app wires SAF together on an open database.
func app(store *sql.DB, box *seal.Box, photoDir, domain string) (*stock.Inventory, http.Handler) {
	inventory := stock.NewInventory(store, box, photoDir)
	return inventory, web.Handler(web.Config{
		Accounts:  auth.NewAccounts(store, auth.HashCost),
		Inventory: inventory,
		Domain:    domain,
	})
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       120 * time.Second, // photo uploads on slow mobile data
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

// httpsServers serves SAF over HTTPS with a Let's Encrypt certificate for domain (kept in
// cacheDir and renewed by itself), plus plain HTTP that only answers Let's Encrypt's checks and
// redirects everything else to HTTPS.
//
// Let's Encrypt checks from servers abroad and port 443 only accepts Brazil, so its check on
// 443 fails and autocert falls back to the one on port 80, which stays open (docs/PLAN.md §3).
func httpsServers(handler http.Handler, domain, cacheDir string) (secure, redirect *http.Server) {
	certs := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache(cacheDir),
	}
	secure = newServer(handler)
	secure.TLSConfig = certs.TLSConfig()
	return secure, newServer(certs.HTTPHandler(nil))
}

// endpoint is one address SAF answers on.
type endpoint struct {
	server   *http.Server
	listener net.Listener
	tls      bool // certificates come from server.TLSConfig
}

func listen(addr string, server *http.Server, tls bool) (endpoint, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return endpoint{}, err
	}
	return endpoint{server: server, listener: listener, tls: tls}, nil
}

// run serves the endpoints until the process is told to stop, with the daily 6-month purge.
func run(inventory *stock.Inventory, endpoints ...endpoint) error {
	// docker stop sends SIGTERM (so does closing the trial's window on Windows): finish in-flight
	// requests, then close the database cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, len(endpoints))
	for _, e := range endpoints {
		go func() {
			if e.tls {
				serveErr <- e.server.ServeTLS(e.listener, "", "")
			} else {
				serveErr <- e.server.Serve(e.listener)
			}
		}()
		slog.Info("SAF listening", "addr", e.listener.Addr().String(), "https", e.tls)
	}
	go purgeDaily(ctx, inventory)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var errs []error
	for _, e := range endpoints {
		errs = append(errs, e.server.Shutdown(shutdownCtx))
	}
	slog.Info("SAF stopped")
	return errors.Join(errs...)
}

// purgeDaily deletes photos and first names past six months (LGPD), at startup and then every
// 24 hours, until ctx ends. A failed run is logged and retried the next day.
func purgeDaily(ctx context.Context, inventory *stock.Inventory) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		result, err := inventory.Purge(ctx, time.Now())
		if err != nil {
			slog.Error("6-month purge failed", "err", err)
		} else {
			slog.Info("6-month purge", "photos", result.Photos, "names", result.Names, "files", result.Files)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// createAdmin makes the first administrator (docs/PLAN.md §1). The password is random and shown
// once; the admin changes it in "Alterar minha senha".
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
