package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
)

func TestFirstRunCreatesTheSetupAdminOnce(t *testing.T) {
	store, err := openStore(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, firstRun(store))
	require.NoError(t, firstRun(store), "the next start finds a user and adds nothing")

	users, err := auth.NewAccounts(store, auth.HashCost).List(context.Background())
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.True(t, users[0].NeedsSetup() && users[0].IsAdmin(), "%+v", users[0])
}

func TestHTTPSServers(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("SAF")) })
	secure, redirect := httpsServers(app, "saf-farmacia.duckdns.org", t.TempDir())

	t.Run("port 80 sends everything to HTTPS", func(t *testing.T) {
		res := httptest.NewRecorder()
		redirect.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "http://saf-farmacia.duckdns.org/estoque?q=dipirona", nil))
		assert.Equal(t, http.StatusFound, res.Code)
		assert.Equal(t, "https://saf-farmacia.duckdns.org/estoque?q=dipirona", res.Header().Get("Location"))

		res = httptest.NewRecorder()
		redirect.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://saf-farmacia.duckdns.org/login", nil))
		assert.Equal(t, http.StatusBadRequest, res.Code, "a form is never sent over plain HTTP")
	})

	t.Run("certificates only for our domain", func(t *testing.T) {
		res := httptest.NewRecorder()
		redirect.Handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "http://evil.example/.well-known/acme-challenge/token", nil))
		assert.Equal(t, http.StatusForbidden, res.Code)

		_, err := secure.TLSConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example"})
		assert.ErrorContains(t, err, "not configured", "refused before asking Let's Encrypt")
		assert.Contains(t, secure.TLSConfig.NextProtos, "acme-tls/1", "the TLS check on 443 is offered too")
	})

	t.Run("both keep the timeouts", func(t *testing.T) {
		for _, srv := range []*http.Server{secure, redirect} {
			require.Equal(t, 5*time.Second, srv.ReadHeaderTimeout)
			assert.Equal(t, 64<<10, srv.MaxHeaderBytes)
		}
	})
}
