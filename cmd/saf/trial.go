package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"saf/internal/auth"
	"saf/internal/sample"
	"saf/internal/seal"
	"saf/internal/stock"
)

// trialAddr: the trial answers only on this computer, so nobody else on the network can reach it.
const trialAddr = "127.0.0.1:8080"

// trial is what a double-click on the executable does: a ready-to-try SAF for the pharmacy to test
// before the real deployment. Data and key live in "saf-dados" next to the executable; the first
// run fills in sample data and the two prototype logins.
func trial() error {
	useUTF8Console()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(exe), "saf-dados")
	store, box, err := openTrial(dir, time.Now())
	if err != nil {
		return err
	}
	defer store.Close()

	inventory, handler := app(store, box, filepath.Join(dir, "photos"), "")
	local, err := listen(trialAddr, newServer(handler), false)
	if err != nil {
		openBrowser("http://localhost:8080")
		return fmt.Errorf("não foi possível usar a porta 8080 (%v). Talvez o SAF já esteja aberto em outra janela", err)
	}
	fmt.Printf(`
  SAF – versão de teste

  Abrindo http://localhost:8080 no navegador.

  Administrador:   %s   senha %s
  Só visualiza:    %s   senha %s
  (se ainda não trocou a senha em "Alterar minha senha")

  Os dados ficam em %s
  Os dados são de exemplo. Use só nomes fictícios.

  Para parar o SAF, feche esta janela.

`, sample.AdminLogin, sample.Password, sample.ViewerLogin, sample.Password, dir)
	openBrowser("http://localhost:8080")
	return run(inventory, local)
}

// openTrial opens the trial's data folder, creating its key on the first run and filling an
// empty database with sample data.
func openTrial(dir string, now time.Time) (*sql.DB, *seal.Box, error) {
	store, err := openStore(dir)
	if err != nil {
		return nil, nil, err
	}
	keyFile := filepath.Join(dir, "chave.key") // in the trial only; production keeps its key apart
	if _, err := os.Stat(keyFile); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(keyFile, []byte(seal.NewKey()+"\n"), 0o600); err != nil {
			store.Close()
			return nil, nil, fmt.Errorf("create key: %w", err)
		}
	}
	box, err := seal.LoadKey(keyFile)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	accounts := auth.NewAccounts(store, auth.HashCost)
	users, err := accounts.List(context.Background())
	if err == nil && len(users) == 0 {
		err = sample.Fill(context.Background(), accounts, stock.NewInventory(store, box, filepath.Join(dir, "photos")), now)
	}
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return store, box, nil
}

// openBrowser opens url in the default browser. If that fails the window still shows the address.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// waitBeforeClosing keeps a double-clicked window open long enough to read what went wrong.
func waitBeforeClosing(err error) {
	fmt.Printf("\n  Erro: %v\n\n  Pressione Enter para fechar.\n", err)
	bufio.NewReader(os.Stdin).ReadString('\n')
}
