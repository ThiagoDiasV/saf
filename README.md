# SAF – Serviço de Apoio à Farmácia

Stock control for the UNILAB university pharmacy: medicines, lots, entries, exits with FEFO,
history with estorno, photos of prescriptions. One Go binary with SQLite, pt-BR UI, for one
admin and one view-only user. It runs only on the pharmacy's Windows PC, as a desktop app.

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
opens only "Configure o administrador": the encryption key to print (setup waits for the box "Imprimi
a chave e guardei a folha"), then your name, your own login and password. After that `admin` no
longer exists and can't be chosen again. Then create the view-only user under Usuários.

`saf create-admin <login> "<nome>"` adds another administrator with a random first password, e.g.
when the only administrator has forgotten theirs.

**Start over:** stop the app and delete `./data`. Keep `dev.key` with it: without the key, the
names and photos in `./data` cannot be read. Both are in `.gitignore`.

## The desktop app

Instructions for the pharmacy, in Portuguese: [`docs/LEIA-ME.txt`](docs/LEIA-ME.txt) (install, first access,
Google Drive, updates). Every release and `make windows` ship it next to `SAF.exe`.

```sh
make windows   # dist/SAF-windows.zip: SAF\SAF.exe and LEIA-ME.txt, for the pharmacy's PC
make mac       # dist/SAF.app, to check builds on an Apple Silicon Mac (needs Xcode's Command Line Tools)
```

A double-click on `SAF.exe` (or `SAF.app`) opens SAF in its own window; a second double-click brings
that window forward. Everything stays in the folder around the program:

```
C:\SAF\SAF.exe
C:\SAF\chave.key      the encryption key, made on the first start (never synced to Drive)
C:\SAF\dados\         the database (saf.db) and the photos (photos\)
C:\SAF\dados\backups\ hourly copies of the database, saf-AAAA-MM-DD-HHMM.db
```

**Backups.** While SAF is open, every hour and once more when it closes, SAF copies the database
into `dados\backups` if anything changed, and deletes copies older than 30 days. Google Drive for
desktop syncs `dados\backups` and `dados\photos` to the pharmacy's Drive. Never sync `dados` as a
whole (Drive would upload `saf.db` mid-write) and never put `chave.key` in Drive.

**Restore** (a new PC, or going back to a copy): close SAF; put `SAF.exe` in `C:\SAF`; copy the
chosen `saf-….db` from Drive to `C:\SAF\dados\saf.db` (delete any `saf.db-wal` and `saf.db-shm`
there) and the Drive `photos` folder to `C:\SAF\dados\photos`; create `C:\SAF\chave.key` with the
text of the printed key. Then open SAF.

If `dados` exists but `chave.key` doesn't, SAF refuses to start rather than make a new key that
can't read the old names and photos: put the printed key back in `chave.key`.

Inside, SAF answers its window on a random port of `127.0.0.1` with a token made at each start, so
other programs on the PC can't use it. `SAF.exe` is not signed: Windows SmartScreen warns once
("Mais informações" → "Executar assim mesmo"), and a locked-down PC may refuse it.

## Releases and updates

A tag `vX.Y.Z` runs `.github/workflows/release.yml`. After you approve it in the `release`
environment, the job runs `make check`, builds and scans `SAF.exe`, signs `latest.json` with the
`SAF_SIGNING_KEY` secret, and publishes the GitHub release.

```sh
git tag v1.4.2 && git push origin v1.4.2
```

`SAF.exe` (a release build, never a `dev` one) checks
`https://github.com/ThiagoDiasV/saf/releases/latest/download/latest.json` when it opens and then
daily. It trusts it only if the signature matches `update.ReleaseKey`:

- **minor or patch** (1.4.2 → 1.5.0): downloads, checks the SHA-256, and swaps `SAF.exe` (the old
  one becomes `SAF.anterior.exe`). It runs from the next start; a banner offers "Reiniciar agora".
- **major** (1.x → 2.0.0): a banner for administrators, "Atualizar agora". Nothing changes before
  the click. Use a major version when an update needs people to know about it.

Never an older version. To roll back by hand: close SAF and rename `SAF.anterior.exe` to `SAF.exe`.
A database migration is forward-only; its copy is in `dados/backups/pre-migration-NNN.db`.

The signing key: `go run ./cmd/release keygen <file>` writes a new one and prints its public half,
which goes in `update.ReleaseKey`. A new key means SAF.exe must be installed by hand once more,
because the running version only trusts the old one.

## Check it

| Command | What it does |
|---|---|
| `make check` | `gofmt`, `go vet`, `staticcheck`, `govulncheck`, `go test -race ./...`. Must pass before any commit. Also runs in CI. |
| `make smoke` | Builds the app, starts it on a fresh data dir and clicks through every screen in headless Chrome. Fails on any console error, CSP violations included. Needs Node 23+ and Google Chrome. `SHOTS=<dir>` also saves screenshots. Not in CI. |
| `make scan` | Builds `SAF.exe` and scans the libraries in it with Trivy; fails on fixable HIGH/CRITICAL findings. Needs Docker. Also runs in CI. |

CI also runs the smoke test on Windows and starts `SAF.exe` on a fresh Windows machine
(`.github/workflows/windows.yml`): the window reaches SAF, the key and database appear next to it,
and a second double-click doesn't start a second SAF. Clicking through the window itself is a
manual check.

Tests use real SQLite files in temporary directories; nothing is mocked.

## Configuration

For `saf serve` only; the desktop app uses the folder around it.

| Variable | Meaning |
|---|---|
| `SAF_DATA_DIR` | Database (`saf.db`), `photos/`, `backups/`. Default `./data`. |
| `SAF_KEY_FILE` | The encryption key, as printed by `saf keygen`. Required by `saf serve`. |

## Commands

```
SAF.exe / SAF.app                  the desktop app (no arguments)
saf serve                          development: SAF in the browser at http://127.0.0.1:8080
saf create-admin <login> "<nome>"  an administrator with a random first password
saf keygen                         a new encryption key
```

## Code

```
cmd/saf/          the commands above; desktop.go is the window, desktop_common.go its folder and guard
cmd/release/      signs releases (CI) and makes the signing key
internal/update/  the updater: signed latest.json, SAF.exe swap, the banner's state
build/            the app icon, the Windows build script, the Mac bundle's Info.plist
internal/db/      SQLite: connection settings, numbered migrations, snapshot before migrating
internal/auth/    users, passwords, login lock, sessions
internal/stock/   catalog, lots, entries, exits (FEFO), history, estorno, photos, 6-month deletion
internal/seal/    AES-256-GCM for first names and photos
internal/form/    field errors shown next to form inputs
internal/web/     handlers, security middleware, templates, static files
test/browser/     the smoke test
```
