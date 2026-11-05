# SAF – Serviço de Apoio à Farmácia

Stock control for the UNILAB university pharmacy: medicines, lots, entries, exits with FEFO,
history with estorno, photos of prescriptions. One Go binary with SQLite, pt-BR UI, for one
admin and one view-only user. It runs only on the pharmacy's Windows PC, as a desktop app (being
built: Phases 10c–10f in `docs/PLAN.md`).

How it is designed and why — decisions, LGPD rules, schema, security — is in
[`docs/PLAN.md`](docs/PLAN.md). The clickable prototype the screens came from is
`docs/mockup/index.html`.

## Run it locally

Needs Go 1.27 (an older `go` downloads 1.27 by itself, as `go.mod` asks).

```sh
make run
```

The first run creates `dev.key`, the encryption key for patients' first names and photos. The app
serves on http://127.0.0.1:8080 (this computer only) and keeps its data in `./data`.

**First login:** an empty database gets the login `admin` with the password `primeiro-acesso`. It
opens only "Configure o administrador": your name, your own login and password. After that `admin`
no longer exists and can't be chosen again. Then create the view-only user under Usuários.

`saf create-admin <login> "<nome>"` adds another administrator with a random first password, e.g.
when the only administrator has forgotten theirs.

**Start over:** stop the app and delete `./data`. Keep `dev.key` with it: without the key, the
names and photos in `./data` cannot be read. Both are in `.gitignore`.

## Check it

| Command | What it does |
|---|---|
| `make check` | `gofmt`, `go vet`, `staticcheck`, `govulncheck`, `go test -race ./...`. Must pass before any commit. Also runs in CI. |
| `make smoke` | Builds the app, starts it on a fresh data dir and clicks through every screen in headless Chrome. Fails on any console error, CSP violations included. Needs Node 23+ and Google Chrome. `SHOTS=<dir>` also saves screenshots. Not in CI. |
| `make scan` | Builds the Windows executable and scans the libraries in it with Trivy; fails on fixable HIGH/CRITICAL findings. Needs Docker. Also runs in CI. |

Tests use real SQLite files in temporary directories; nothing is mocked.

## Configuration

| Variable | Meaning |
|---|---|
| `SAF_DATA_DIR` | Database (`saf.db`), `photos/`, `backups/`. Default `./data`. |
| `SAF_KEY_FILE` | The encryption key, as printed by `saf keygen`. Required by `saf serve`. |

## Commands

```
saf serve                          development: SAF in the browser at http://127.0.0.1:8080
saf create-admin <login> "<nome>"  an administrator with a random first password
saf keygen                         a new encryption key
```

## Code

```
cmd/saf/          the commands above; the daily 6-month deletion runs inside serve
internal/db/      SQLite: connection settings, numbered migrations, snapshot before migrating
internal/auth/    users, passwords, login lock, sessions
internal/stock/   catalog, lots, entries, exits (FEFO), history, estorno, photos, 6-month deletion
internal/seal/    AES-256-GCM for first names and photos
internal/form/    field errors shown next to form inputs
internal/web/     handlers, security middleware, templates, static files
test/browser/     the smoke test
```
