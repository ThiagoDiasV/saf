# SAF – Serviço de Apoio à Farmácia · Plan

Stock control for the UNILAB university pharmacy. One admin and one view-only user, pt-BR UI, a
desktop app on the pharmacy's Windows PC. Source requirements: `CRIAR UM SISTEMA DE CONTROLE DE ESTOQUE DE UMA FARMÁCIA UNIVERSITÁRIA.docx`.

**2026-11-05: no cloud server.** A VM with HTTPS, a Brazil-only firewall and Litestream was built and
dropped: SAF runs only on that PC (photos are taken there too), backups go to the pharmacy's Google
Drive, and updates come from this repository's signed releases. Cost: none.

**Internal control only.** SAF is not the legal record for controlled substances (SNGPC / Portaria 344).

The clickable prototype `docs/mockup/index.html` is the UI spec. The real app copies its CSS and markup;
screenshots of the real app are in `docs/screens/` (`SHOTS=docs/screens make smoke`). The prototype is frozen (2026-10-07): where it differs from this plan
(first-name rule, blurred photos, CSV without names, 6-month notice), the plan wins.

---

## 1. Decisions

### Product

| Topic | Decision |
|---|---|
| Users | Two roles. **Admin** does everything. **Viewer** sees Estoque and Histórico only. One login per person. A fresh install has `admin` / `primeiro-acesso`, which can only set up the real first administrator (name, login, password) and then stops existing; `admin` is a reserved login. `saf create-admin` adds an administrator from the CLI (recovery). |
| Login | Password only (no 2FA). 12–64 characters (bcrypt reads at most 72 bytes). 5 wrong tries lock the login for 15 min. |
| Medicine (catalog item) | Active ingredient + strength + dosage form + counting unit, all free text with suggestions. Duplicate = same ingredient + strength + form, ignoring case, accents and spaces. |
| Lot | Medicine + lot + manufacturer (same folding). Same lot again merges into it; a different expiry for the same lot blocks the save. |
| Expiry | `MM/AAAA`, valid until the last day of that month. Highlight when ≤ 90 days; expired lots are flagged. |
| Movements | Every entry and exit has 1..N lines. Stock is **always** the sum of lines; no stored balance. |
| Entry types | Doação, Permuta, Abastecimento, Devolução, Saldo inicial, Outros |
| Exit types | Dispensação, Perda, Extravio, Vencimento/Descarte, Permuta, Devolução ao fornecedor |
| FEFO | Exit lines are pre-filled from the lot with the closest expiry; editable per lot. Lines must add up to the requested quantity. Expired lots only leave by Vencimento/Descarte. |
| Dates | Movement date can be backdated, never in the future. "Registrado em" (save time + user) is always shown. |
| Corrections | Quantities: **estorno** only — a linked reversal of the whole movement, dated today, once per movement, blocked if the units already left. Text fields (observação, lot details, catalog fields): editable, logged old → new as "Ajuste" in Histórico. |
| Catalog removal | **Archive** (blocked while stock > 0). Real delete only if the medicine never moved. |
| Photos | Optional, several per movement, addable later, removable by the admin (logged). Shrunk in the browser to 1600 px JPEG before upload. |
| Histórico, Estoque | Filters + CSV export (`;`, UTF-8 with BOM, opens in Excel). Estoque is grouped by medicine, groups collapsed by default. |
| UI | pt-BR. Light + dark theme (device default, toggle saved in the browser). UNILAB palette: accent `#A02D34`, header `#6D1F26`, dark-theme accent `#DFA6A9`. Status text red `#A50E0E` + icon. |

Added while building the prototype and **pending the pharmacist's validation**: `MM/AAAA` expiry,
lot editing from Estoque, catalog edits logged as Ajuste, user deactivate/reactivate + password reset +
own password change, the login lock, photo picker without forced camera, status red `#A50E0E`
(the agreed `#D93025` is 4.4:1 on the page background, below the 4.5:1 minimum).

### Personal data (LGPD)

The app's job (stock, FEFO, history) needs no patient data. The only patient data kept:

| Data | Rule |
|---|---|
| "Dispensado para" | Optional, Dispensação only. **First name only**: one word, letters (accents allowed), ≤ 20 characters, checked in the browser and on the server. "Ana Paula" → type "Ana". |
| Photos | May show prescriptions with full name, CPF, diagnosis. |

Protection:

1. **Encrypted at rest.** First names and photo files are sealed with AES-256-GCM (Go standard library).
   The movement or photo id is bound as associated data, so a ciphertext copied to another row fails to open.
   Stock data is not encrypted (not personal data).
2. **Deleted after 6 months.** A job inside the app (at startup, then every 24 h) deletes **all photos**
   and **all first names** saved more than 6 months ago. Movements and quantities stay.
   SQLite `secure_delete` overwrites the deleted values.
3. **Backups expire.** Hourly database copies are kept 30 days; the photo copy mirrors deletions. Google Drive keeps
   deleted files 30 more days in its trash. Everything is gone everywhere within about 8 months.
4. **Photos only through the app**, logged in, `Cache-Control: no-store`, blurred until clicked.
   Canvas re-encoding drops EXIF (phone GPS).
5. **UI hints:** Histórico says "Nomes e fotos com mais de 6 meses são apagados". The Observação field says
   "Não escreva dados do paciente aqui" — observações are not purged.

Encryption protects copies of the data (the Drive backups, a copied database file). It does **not** protect
against someone using the PC's Windows account (the key sits next to SAF.exe) or a stolen admin login.

**Key:** 32 random bytes, made on SAF's first start in `chave.key` next to `SAF.exe`, outside `dados\` and so outside
the backups. The setup page shows it to be printed, and setup waits until the printout is confirmed: the paper,
kept away from the PC, is the only other copy. Never in Drive or e-mail. If `dados\` exists but the key is missing,
SAF refuses to start rather than make a new key. Lost key = the last ≤ 6 months of names and photos are unreadable;
stock is unaffected. No key rotation is built.

---

## 2. Architecture

```
 SAF.exe (one Go binary) on the pharmacy's PC
   ├─ window: Wails + WebView2 (Windows' own Edge engine) ──http + launch token──┐
   ├─ SAF on 127.0.0.1:<random port> ◀───────────────────────────────────────────┘
   │    ├─ html/template pages + plain JS
   │    ├─ SQLite  C:\SAF\dados\saf.db          (names sealed)
   │    ├─ photos  C:\SAF\dados\photos\<id>      (sealed files)
   │    └─ jobs: 6-month purge (daily), backup (hourly + on close), update check (daily)
   ├─ C:\SAF\chave.key                           (outside dados\)
   └─ C:\SAF\dados\backups\saf-<date>.db ──Google Drive for desktop──▶ pharmacy's Drive (+ photos\)
 GitHub releases (signed latest.json + SAF.exe) ──daily check──▶ SAF.exe swaps itself
```

The first plan was a web app on a VM so two PCs and phones could share one database. The pharmacy chose one PC
(2026-11-05): no server to run or pay for, nothing reachable from the internet.

**Why a local port and not Wails' own URL scheme:** on macOS, WebKit does not follow redirects on custom schemes,
and SAF answers every save with a redirect. Over real HTTP the window behaves like a browser (redirects,
cookies, downloads). Other programs on the PC can find the port, so the window starts at `/abrir?t=<token>`
(random at each start), which sets a cookie every other request must carry; the only Host accepted is `127.0.0.1`.

### Stack

- **Go 1.27** (currently 1.27.3; Go supports the two latest releases), standard library first: `net/http` (method routing), `html/template`, `embed`,
  `log/slog`, `encoding/csv`, `crypto/aes` + `crypto/cipher`, `crypto/ed25519` (release signatures), `image/jpeg`, `time/tzdata`.
- **Dependencies (only these):** `modernc.org/sqlite` (pure Go, no cgo), `golang.org/x/crypto` (bcrypt),
  `github.com/wailsapp/wails/v2` (the window; pure Go on Windows, cgo on the Mac), `github.com/stretchr/testify` (tests).
  Accent folding is hand-written for Portuguese letters instead of adding `x/text`. Build tool only, never in the
  binary: `go-winres` (icon and DPI-aware manifest for SAF.exe).
- **Front end:** server-rendered pages, the prototype's CSS, plain JS for FEFO pre-fill, add/remove lines,
  `MM/AAAA` mask, photo shrink, confirm dialogs, theme toggle, printing the key. No npm, no build step.
- **Security:** see §3.
- **SQLite:** WAL, `foreign_keys`, `busy_timeout=5000`, `synchronous=FULL`, `secure_delete`,
  writes in `BEGIN IMMEDIATE` transactions. Times stored in UTC; shown in `America/Fortaleza`.
- **Migrations:** numbered `.sql` files embedded in the binary, tracked with `PRAGMA user_version`,
  forward-only. When migrations are pending, the app snapshots the database first.

### Layout

```
cmd/saf/              SAF.exe (desktop.go) | serve | create-admin | keygen
cmd/release/          release signing (CI) and the signing key
internal/db/          open + pragmas, migrations, snapshots and hourly backups (VACUUM INTO)
internal/db/migrations/001_init.sql
internal/stock/       catalog, lots, movements, FEFO, estorno, stock queries, CSV rows, photos, purge
internal/auth/        users, passwords, login lock, sessions, first-run admin
internal/form/        field-level validation messages (form.Errors)
internal/seal/        AES-256-GCM box + key file
internal/update/      signed latest.json, SAF.exe swap, banner state
internal/web/         routes, handlers, middleware, templates/, static/
build/                icon, Windows build script, Mac Info.plist
Makefile  .github/workflows/{check,windows,release}.yml
```

### Configuration

The desktop app has none: everything lives in the folder around `SAF.exe`. `saf serve` (development) reads
`SAF_DATA_DIR` (default `./data`) and `SAF_KEY_FILE` (required). Release builds get their version from
`-ldflags "-X main.version=…"`; `dev` builds never update themselves.

---

## 3. Security

Threat model: two staff users, health data, one Windows PC at a counter. Nothing listens on the network. The
realistic risks are someone at the PC (a patient, a visitor), a lost or broken PC, other software on it, and
a tampered update.

| Layer | Measure |
|---|---|
| **Window and port** | SAF listens only on `127.0.0.1`, on a random port. Its window starts with a launch token that becomes a cookie; any other program on the PC gets `403` "Abra o SAF pelo ícone.". Host must be `127.0.0.1` (`421` otherwise), so a web page that points its own name at the PC (DNS rebinding) gets nothing. |
| **Rate limit** (app) | In memory, hand-rolled: 300 requests/min → `429`; `POST /login` 10/min, on top of the per-login lock (5 wrong → 15 min). Counters reset on restart. |
| **HTTP server** | Timeouts: read header 5 s, read 120 s (photo uploads), write 60 s, idle 120 s. Headers ≤ 64 KB. Bodies capped with `http.MaxBytesReader`: forms 1 MB, photo uploads 8 MB and ≤ 10 photos → `413`. |
| **CORS** | **None.** The app never sends `Access-Control-*` headers, so browsers block every cross-origin read; `OPTIONS` gets `405`. No wildcard can exist because there is no CORS config. Pages, JS and photos are same-origin. |
| **Cross-site writes** | `http.CrossOriginProtection` rejects cross-origin `POST`s; session cookie `SameSite=Lax`. |
| **Headers** | A fixed set of security headers on every response, and checks on the headers browsers send — see [Headers](#headers). |
| **Sessions** | 32 random bytes in an `HttpOnly; SameSite=Lax` cookie; only the SHA-256 is stored; 12 h lifetime; deleted on logout, password reset and deactivation. |
| **Shared counter PC** | Logged-in pages, photos and CSV are never cached (`no-store`), so Back after logout shows nothing. Photos show blurred until "Mostrar foto" is clicked, so patients at the counter can't read a prescription on the screen. |
| **Supply chain** | `govulncheck` in `make check`; Dependabot (Go modules, GitHub Actions); **Trivy scans the libraries built into SAF.exe and blocks the build** on fixable HIGH/CRITICAL findings, on every push, weekly on `main` and in every release. Actions pinned by commit. |
| **Updates** | SAF installs a release only if `latest.json` carries a valid ed25519 signature for the key built into it (`update.ReleaseKey`), and SAF.exe matches its SHA-256. The private key is a secret of the `release` environment on GitHub, used only after Thiago approves the run, and only by the signing step (our own code; the build tools never see it). Release tags can't be moved or deleted. Never an older version. |
| **Passwords** | bcrypt, 12–64 characters, per-login lock (§1). |
| **Data at rest** | First names and photos sealed; 6-month purge; backups expire (§1). |
| **PC** | Windows Update (security patches, WebView2 included). SAF.exe is not code-signed: SmartScreen warns once. |
| **Logs** | Never log first names, photo contents, passwords or session tokens. The desktop app keeps no log file. |

### Headers

Sent on **every response** by one middleware (Go sends no `Server` header):

| Header | Value | Why |
|---|---|---|
| `Content-Security-Policy` | `default-src 'self'; img-src 'self' blob:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'` | Only our own scripts and styles run (no inline); `blob:` for photo previews. |
| `X-Content-Type-Options` | `nosniff` | A file is never run as something else. |
| `X-Frame-Options` | `DENY` | Older browsers: no framing (clickjacking). |
| `Referrer-Policy` | `no-referrer` | URLs never leak to other sites. |
| `Cross-Origin-Opener-Policy` | `same-origin` | Other sites can't hold a handle to our window. |
| `Cross-Origin-Resource-Policy` | `same-origin` | Other sites can't embed our photos or files. |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=(), payment=(), usb=()` | No page can use these. The photo picker is a plain file input (the phone's own camera app), which this does not block. |
| `Cache-Control` | `no-store` on logged-in pages, photos, CSV; static files cached 1 day, URLs carry the build version | Shared counter PC. |
| `Clear-Site-Data` | `"cache"` on logout | Wipes cached pages. Not `"storage"`, which would also wipe the theme choice. |
| `Content-Type` | Always with `charset=utf-8`; CSV also `Content-Disposition: attachment`. Static files' types are set by SAF, not taken from the Windows registry (other programs change `.js` there, and with `nosniff` a wrong type stops `app.js`). | |

Checked on **every request**:

| Request header | Rule |
|---|---|
| `Host` | Must be `127.0.0.1` in the desktop app, else `421`. |
| `Sec-Fetch-Site`, `Sec-Fetch-Mode`, `Sec-Fetch-Dest` (sent by all current browsers) | `GET` from another site is allowed only as a top-level navigation (a link clicked elsewhere); any other cross-site load → `403`. Missing headers (old browser, curl) are allowed through. |
| `Origin` / `Sec-Fetch-Site` on `POST` | Same origin only (`http.CrossOriginProtection`). |
| `Content-Type` on `POST` | Only `application/x-www-form-urlencoded` or `multipart/form-data`, else `415`. |

What this does **not** stop:
- **Someone who can use the PC's Windows account** can read `chave.key` and the database. Windows' own lock screen
  and account password are the control; the blurred photos and `no-store` only cover the screen at the counter.
- **Malware on the PC** sees what SAF shows. Windows Update and the university's antivirus are the control.
- **A compromised GitHub account** could release a signed update only through the `release` environment, which
  waits for Thiago's approval; 2FA on that account matters.

---

## 4. Database

```sql
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  name          TEXT NOT NULL,
  login         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('admin', 'viewer')),
  active        INTEGER NOT NULL DEFAULT 1,
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until  TEXT,
  last_login_at TEXT,
  created_at    TEXT NOT NULL
);

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,               -- SHA-256 of the cookie value
  user_id    INTEGER NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL
);

CREATE TABLE medicines (
  id                INTEGER PRIMARY KEY,
  active_ingredient TEXT NOT NULL,
  strength          TEXT NOT NULL,           -- "500 mg", "50 mg/mL"
  dosage_form       TEXT NOT NULL,           -- "comprimido"
  unit              TEXT NOT NULL,           -- how stock is counted
  match_key         TEXT NOT NULL UNIQUE,    -- ingredient+strength+form, folded
  archived_at       TEXT,
  created_at        TEXT NOT NULL
);

CREATE TABLE batches (
  id           INTEGER PRIMARY KEY,
  medicine_id  INTEGER NOT NULL REFERENCES medicines(id),
  lot          TEXT NOT NULL,
  manufacturer TEXT NOT NULL,
  expiry_month TEXT NOT NULL,                -- 'YYYY-MM'
  match_key    TEXT NOT NULL,                -- lot+manufacturer, folded
  UNIQUE (medicine_id, match_key)
);

CREATE TABLE movements (
  id           INTEGER PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('in', 'out', 'reversal')),
  type         TEXT NOT NULL,                -- 'Doação', 'Dispensação', 'Estorno de saída', ...
  occurred_on  TEXT NOT NULL,                -- movement date, local
  recorded_at  TEXT NOT NULL,                -- save time, UTC
  user_id      INTEGER NOT NULL REFERENCES users(id),
  note         TEXT NOT NULL DEFAULT '',
  patient_name BLOB,                         -- sealed first name; NULL if none or purged
  reverses_id  INTEGER UNIQUE REFERENCES movements(id)
);

CREATE TABLE movement_lines (
  id          INTEGER PRIMARY KEY,
  movement_id INTEGER NOT NULL REFERENCES movements(id),
  batch_id    INTEGER NOT NULL REFERENCES batches(id),
  quantity    INTEGER NOT NULL CHECK (quantity <> 0)   -- + in, - out
);
CREATE INDEX movement_lines_batch ON movement_lines(batch_id);

CREATE TABLE photos (
  id          INTEGER PRIMARY KEY,
  movement_id INTEGER NOT NULL REFERENCES movements(id),
  added_at    TEXT NOT NULL,
  added_by    INTEGER NOT NULL REFERENCES users(id),
  removed_at  TEXT,                          -- file deleted
  removed_by  INTEGER REFERENCES users(id)   -- NULL with removed_at = 6-month purge
);

CREATE TABLE changes (                       -- shown as "Ajuste" in Histórico
  id          INTEGER PRIMARY KEY,
  medicine_id INTEGER REFERENCES medicines(id), -- exactly one of these three is set
  batch_id    INTEGER REFERENCES batches(id),
  movement_id INTEGER REFERENCES movements(id),
  field       TEXT NOT NULL,                    -- 'Lote', 'Validade', 'Observação', ...
  old_value   TEXT NOT NULL,
  new_value   TEXT NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id),
  changed_at  TEXT NOT NULL,                    -- one save = one timestamp = one Ajuste entry
  CHECK ((medicine_id IS NOT NULL) + (batch_id IS NOT NULL) + (movement_id IS NOT NULL) = 1)
);
```

- Stock per lot = `SUM(quantity)` over `movement_lines`. Every write that removes units re-checks
  the touched lots inside the same transaction and rolls back if any would go below zero.
- Estorno = new `movements` row with `kind='reversal'`, `reverses_id` set, lines negated.
- Patient names never appear in `changes` ("Dispensado para" is not editable; fix with estorno + new saída).
  Photo files keep no original file name.

---

## 5. Screens and routes

| Screen | Route(s) | Prototype | Notes |
|---|---|---|---|
| Login | `GET/POST /login`, `POST /sair` | <img src="screens/01-login.png" width="260"> | Lock message after 5 tries. |
| Estoque | `GET /estoque`, `GET /estoque.csv`, `POST /lotes/{id}` | <img src="screens/08-estoque.png" width="260"> | Grouped by medicine, collapsed by default, search, filters (vencidos / vencendo), lot edit (admin). |
| Entrada | `GET/POST /entrada` | <img src="screens/07-entrada-conflito.png" width="260"> | N lines; lot merge; expiry conflict blocks. |
| Saída | `GET/POST /saida` | <img src="screens/10-saida.png" width="260"> | One line per medicine, FEFO split per lot, sum check, expired blocked, first name for Dispensação. |
| Histórico | `GET /historico`, `GET /historico.csv` | <img src="screens/11-historico.png" width="260"> | Filters: period, type, medicine, lot, first name (decrypted in memory within the period). 10 per page. |
| Detalhe | `GET /movimentos/{id}`, `POST /movimentos/{id}/observacao`, `POST /movimentos/{id}/estorno`, `POST /movimentos/{id}/fotos`, `POST /fotos/{id}/remover`, `GET /fotos/{id}` | <img src="screens/12-detalhe-estornar.png" width="260"> | Lines, photos, change log, estorno. |
| Medicamentos | `GET/POST /medicamentos`, `POST /medicamentos/{id}`, `POST /medicamentos/{id}/arquivar`, `POST /medicamentos/{id}/excluir` | <img src="screens/06-medicamentos.png" width="260"> | Duplicate check, archive/delete rules. |
| Usuários | `GET/POST /usuarios`, `POST /usuarios/{id}/senha`, `POST /usuarios/{id}/ativo`, `GET/POST /minha-senha` | <img src="screens/02-usuarios.png" width="260"> | Admin only, except own password. |
| Narrow window | same routes | <img src="screens/09-celular-estoque.png" width="260"> | Bottom tabs (≤ 5) + "Mais" menu: the phone layout, kept for a narrow window. |

All writes are `POST` → redirect (no double submit on reload). The viewer gets 403 on every write route
and on the admin screens. Error messages are pt-BR, next to the field, with the prototype's wording.

---

## 6. Running, backup, updates, cost

**Install:** unzip to `C:\SAF` (or put `SAF.exe` there) and double-click; no installer, no admin rights. WebView2
comes with Windows 10/11; if it's missing, Wails offers to download it. Instructions for the pharmacy:
`docs/LEIA-ME.txt` (shipped with every release).

**First start:** SAF makes `chave.key`, `dados\`, the database and the first-run `admin` login. Setup shows the key
to print and makes the real administrator (§1).

**Backup:**
- **Database:** `db.Backup` copies `saf.db` to `dados\backups\saf-AAAA-MM-DD-HHMM.db` (via a temp file outside
  `backups\`), every hour and when SAF closes, only if it changed; copies older than 30 days are deleted.
- **Off the PC:** Google Drive for desktop syncs `dados\backups` and `dados\photos` to the pharmacy's Drive (15 GB free).
  Never `dados` as a whole (Drive would upload `saf.db` mid-write), never `chave.key`.
- **Before migrations:** the app writes `backups\pre-migration-<n>.db` (kept).
- Worst case on losing the PC: the work since the last hourly copy.
- **Restore** (README): copy a backup to `dados\saf.db`, the Drive photos to `dados\photos`, the printed key to
  `chave.key`. Drilled on a Mac (2026-11-05); repeat on Windows at handover, then every 3 months.

**Updates and releases:** a tag `vX.Y.Z` → `release.yml`: approval in the `release` environment, `make check`,
build and Trivy scan of SAF.exe, signature of `latest.json`, GitHub release. SAF checks when its window opens
and daily: patch and minor versions download, are checked and swapped in (`SAF.anterior.exe` kept for a manual
rollback), running from the next start with a "Reiniciar agora" banner; a major version waits for an
administrator's "Atualizar agora". Pipeline proven with v0.9.0. Releases also carry `SAF-mac` and
`latest-mac.json` (built on a macOS runner), so a Mac build can check the update flow by hand.

| Item | Cost |
|---|---|
| GitHub (public repository, Actions, releases) | free |
| Google Drive (pharmacy account, 15 GB) | free |
| Code-signing certificate | not bought: SmartScreen warns once |

Sizing: 20 photos/day × ~300 KB × 6 months ≈ 1.1 GB of photos; database copies ≈ 25 MB at today's size.

## 7. Testing and checks

- `make check` = `gofmt -l` (must be empty) + `go vet` (also the Windows desktop build) + `staticcheck` +
  `govulncheck` + `go test -race ./...`. CI runs it, then `make scan` (Trivy on SAF.exe, blocking).
- **Windows CI** (`windows.yml`, every push): `go test ./...` on Windows, the smoke test in Chrome against SAF on
  Windows, and SAF.exe on a fresh machine (window reaches SAF, key and database appear, one running copy).
- Tests run on **real SQLite** in `t.TempDir()`, real files and real HTTPS test servers; no mocks of our own code.
- `stock`: FEFO, merge + expiry conflict, exit checks, estorno, archive/delete, change log, purge, CSV rows.
- `seal`: round trip; wrong key or wrong id fails. `auth`: password rules, lock, sessions, first-run admin.
- `db`: migrations, pre-migration snapshot, hourly backup (only on change, 30-day pruning).
- `update`: signature (tampered file, other key, plain http), swap keeping the previous, bad download refused,
  minor installs / major waits / never older. `release`: keygen + sign verify.
- `web` (httptest, full stack): roles, CSRF, headers, Host, rate limits, body caps, first-run setup with the key,
  update banners.
- `make smoke` (`test/browser/smoke.mjs`): fresh data dir, the first-run setup, then every screen in headless
  Chrome over the DevTools protocol (Node 23+, no npm); fails on any console error, CSP included. In CI on Windows.

---

## 8. Phases

Each phase ends with `make check` output, a screenshot next to the prototype, and your approval before the next one.

| # | Phase | Delivers |
|---|---|---|
| 1 | Skeleton | `go.mod`, `cmd/saf serve`, `internal/db` + `001_init.sql`, layout template with tabs + theme, CSS from the prototype, Makefile, Dockerfile, CI with govulncheck + Trivy (blocking) + Dependabot. `git init` + private GitHub repo (with your OK). |
| 2 | Auth + users + hardening | `create-admin`, login/logout, sessions, lock, roles, CSRF, response headers + request-header checks, rate limits, server timeouts, body caps, Usuários screen, own password. |
| 3 | Catálogo | Medicamentos: create, edit (logged), duplicate check, archive, delete. |
| 4 | Entrada + Estoque | Multi-line entry, lot merge/conflict, `MM/AAAA`, Saldo inicial; Estoque grouped + filters + expiry flags + lot edit + CSV. |
| 5 | Saída + encryption | `keygen`, `seal`, FEFO pre-fill + server checks, expired rule, sealed first name. |
| 6 | Histórico | List, filters, pagination, detail, observação edit, estorno, CSV. |
| 7 | Photos | Browser shrink, sealed files, view (blurred until clicked), add later, remove. |
| 8 | Retention | Daily 6-month purge; "Foto removida (prazo de 6 meses)"; pre-migration snapshot. |
| 9 | Cloud deploy | *Dropped 2026-11-05* (VM, HTTPS, firewall, Litestream built in 9a, then removed). |
| 10a | Desktop spike | Wails window over SAF's own HTTP on 127.0.0.1 + launch token; Windows CI. |
| 10b | Cleanup | VM, HTTPS, Litestream, Docker and trial mode removed; Trivy scans SAF.exe. |
| 10c | Desktop app | SAF.exe / SAF.app, portable folder, key on the setup page, `admin` first run, Windows CI. |
| 10d | Backups | Hourly database copies for Google Drive, restore steps. |
| 10f | Updates | Signed releases, auto-update (minor/patch) and admin-accepted major versions; v0.9.0. |
| 10e | Docs | LEIA-ME for the pharmacy, this plan. |
| 1.0.0 | Handover | Release v1.0.0; install on the pharmacy's PC; restore drill on Windows; Saldo inicial import. |

---

## 9. Open items

| # | Item | Owner | Default until answered |
|---|---|---|---|
| 1 | Is a Google Drive copy acceptable under LGPD (names and photos encrypted, stock and staff names not)? | UNILAB encarregado | Drive |
| 2 | Should the CSV export include first names? (CSV files leave the system unencrypted and are never purged) | Thiago / pharmacist | **Omitted** |
| 3 | Should the viewer see first names and photos? | Thiago / pharmacist | Yes, as in the prototype |
| 4 | Where is the printed key kept? | Pharmacy | Locked drawer away from the PC |
| 5 | Sample of the current stock spreadsheet (for the Saldo inicial import) | Pharmacist | Manual entry |
| 6 | Permission to use the UNILAB logo | Pharmacist / UNILAB | Text-only header |
| 7 | Validate the prototype-only rules listed in §1 | Pharmacist | As in the prototype |
| 8 | Which Google account holds the backups? | Pharmacy | A pharmacy or UNILAB account, not a personal one |
| 9 | Will UNILAB's IT allow an unsigned SAF.exe, and github.com through any proxy (updates)? | UNILAB IT | Ask before the handover |
| 10 | The PC's Windows version (WebView2 is built into Windows 11 and updated Windows 10) | Pharmacy | Windows 10 or 11 |

**Deferred (2026-10-07):** audit log of logins, photo views, CSV exports and user changes (LGPD art. 37 record of processing).

## 10. Risks

- **One PC.** Losing it costs the work since the last hourly copy, if Google Drive was signed in and syncing.
- **Unsigned SAF.exe.** SmartScreen warns once; a locked-down PC or the university's IT may block it (open item 9).
- **Updates need github.com.** Go ignores Windows' proxy settings; behind a forced proxy, updates don't arrive.
- **The signing key lives only in a GitHub secret.** If lost: a new key, a new `ReleaseKey`, and SAF.exe installed
  by hand once more.
- **Key custody.** Losing the key and the printout loses ≤ 6 months of names and photos; leaking the key with a backup
  exposes them.
- **Observação is free text** and is not purged; the field hint is the only control.
- **Login lock** can be triggered by someone guessing a login (15 min lockout); accepted for 2 users.
- **No log file** in the desktop app: a problem at the pharmacy is diagnosed by reproducing it.
