# SAF – Serviço de Apoio à Farmácia

Stock control for the UNILAB university pharmacy: medicines, lots, entries, exits with FEFO,
history with estorno, photos of prescriptions. One Go binary with SQLite, pt-BR UI, for one
admin and one view-only user.

How it is designed and why — decisions, LGPD rules, schema, security, hosting — is in
[`docs/PLAN.md`](docs/PLAN.md). The clickable prototype the screens came from is
`docs/mockup/index.html`.

## Run it locally

Needs Go 1.27 (an older `go` downloads 1.27 by itself, as `go.mod` asks).

```sh
make run
```

The first run creates `dev.key`, the encryption key for patients' first names and photos. The app
serves on http://localhost:8080 and keeps its data in `./data`.

**First login:** an empty database gets the login `admin` with the password `primeiro-acesso`. It
opens only "Configure o administrador": your name, your own login and password. After that `admin`
no longer exists and can't be chosen again. Then create the view-only user under Usuários.

`saf create-admin <login> "<nome>"` adds another administrator with a random first password, e.g.
when the only administrator has forgotten theirs.

- **Start over:** stop the app and delete `./data`. Keep `dev.key` with it: without the key, the
  names and photos in `./data` cannot be read. Both are in `.gitignore`.
- **On a phone:** on the same Wi-Fi, open `http://<this computer's IP>:8080`. macOS may ask to allow
  incoming connections. Locally it is plain HTTP, so the session cookie is not marked Secure.

## Trial build for the pharmacy

To let the pharmacy try SAF on a Windows PC before the deploy:

```sh
make trial    # dist/SAF-teste.zip: SAF-teste.exe + LEIA-ME.txt (instructions in Portuguese)
```

`make trial-mac` builds the same trial for an Apple Silicon Mac in `dist/SAF-teste-mac/`.

A double-click on `SAF-teste.exe` or `SAF-teste` (`saf` with no arguments, on any system) runs the trial:

- data and key in `saf-dados` next to the executable;
- on the first run, sample data and the prototype's logins, `paulo.mendes` (admin) and
  `thiago.dias` (view-only), password `farmacia2026`;
- it answers only on `127.0.0.1:8080`, so nobody else on the network can open it, and it opens
  the browser.

The executable is not signed: Windows SmartScreen warns once ("Mais informações" → "Executar
assim mesmo"), and a locked-down PC may refuse it. `saf serve` never adds sample data.

## Run it in the container

The same image production will run: distroless, non-root, read-only file system, with Litestream
starting SAF and streaming every database change to `SAF_REPLICA_URL` (here a folder in the volume).

```sh
make image
go run ./cmd/saf keygen > dev.key    # skip if you already have one
docker run --rm --read-only --name saf -p 8080:8080 \
  -v saf-data:/data -v "$PWD/dev.key:/etc/saf/key:ro" -e SAF_REPLICA_URL=file:///data/replica saf:dev
```

Then log in as `admin` / `primeiro-acesso` (above).

Without a readable key at `/etc/saf/key` the app refuses to start.

## Check it

| Command | What it does |
|---|---|
| `make check` | `gofmt`, `go vet`, `staticcheck`, `govulncheck`, `go test -race ./...`. Must pass before any commit. Also runs in CI. |
| `make smoke` | Builds the app, starts it on a fresh data dir and clicks through every screen in headless Chrome. Fails on any console error, CSP violations included. Needs Node 23+ and Google Chrome. `SHOTS=<dir>` also saves screenshots. Not in CI. |
| `make image` | Builds the container image `saf:dev`. |
| `make scan` | Builds the image and scans it with Trivy; fails on fixable HIGH/CRITICAL findings. Needs Docker. Also runs in CI. |
| `make trial` | Builds the Windows trial package `dist/SAF-teste.zip`. |
| `make release` | Builds the image for the GCP VM (x86-64) into `dist/saf-release.tar.gz`. |
| `make litestream-deps` | Upgrades every library Litestream uses, pinned in `third_party/litestream`. Dependabot can't, so run it when Trivy flags Litestream, then `make litestream-test check scan`. |
| `make litestream-test` | Runs Litestream's own tests against those pins. |

Tests use real SQLite files in temporary directories; nothing is mocked.

## Configuration

| Variable | Meaning |
|---|---|
| `SAF_DATA_DIR` | Database (`saf.db`), `photos/`, `backups/`. Default `./data`. |
| `SAF_KEY_FILE` | The encryption key, as printed by `saf keygen`. Required by `saf serve`. In production it lives outside `SAF_DATA_DIR` and outside the backups. |
| `SAF_DOMAIN` | Production: HTTPS on 8443 with a Let's Encrypt certificate (renewed by itself, cached in `SAF_DATA_DIR/autocert`), port 8080 answering Let's Encrypt and redirecting to HTTPS, and `421` for any other `Host`. Leave empty locally: plain HTTP on 8080. |
| `SAF_REPLICA_URL` | Read by Litestream in the container: where database changes go (`gs://<bucket>/db` in production). |

## Commands

```
saf                                local trial: sample data, this computer only
saf serve                          web server on :8080
saf create-admin <login> "<nome>"  an administrator with a random first password
saf keygen                         a new encryption key
```

## Code

```
cmd/saf/          the commands above; the daily 6-month deletion runs inside serve and the trial
internal/sample/  the trial's sample data
internal/db/      SQLite: connection settings, numbered migrations, snapshot before migrating
internal/auth/    users, passwords, login lock, sessions
internal/stock/   catalog, lots, entries, exits (FEFO), history, estorno, photos, 6-month deletion
internal/seal/    AES-256-GCM for first names and photos
internal/form/    field errors shown next to form inputs
internal/web/     handlers, security middleware, templates, static files
test/browser/     the smoke test
deploy/           Litestream config, the VM's photo-backup timer, example settings
```

## Deploy

Step by step in [`docs/DEPLOY.md`](docs/DEPLOY.md): GCP project, bucket, VM, firewall, domain,
key, first users, backups, restore drill, updates, rollback, and what to do if the VM is lost.
