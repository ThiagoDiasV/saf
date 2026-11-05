package main

import (
	"context"
	"net/http"
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

func TestNewServerKeepsTheTimeouts(t *testing.T) {
	srv := newServer(http.NotFoundHandler())

	assert.Equal(t, 5*time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 120*time.Second, srv.ReadTimeout)
	assert.Equal(t, 64<<10, srv.MaxHeaderBytes)
}
