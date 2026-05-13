# grail

A tiny single-container homepage + URL directory + uptime check, built for a homelab or an internal QA dashboard. URLs are grouped by **tag**, each application is an expandable card, and a live pulsing dot tells you what's up and what's down.

- **Backend**: Go, stdlib `net/http`, pure-Go SQLite (`modernc.org/sqlite`), no CGO.
- **Frontend**: SvelteKit static bundle, embedded into the Go binary via `go:embed`.
- **Config**: a single TOML file. Hot-reloaded on save (fsnotify + atomic write + admin UI editor).
- **Distribution**: one container image. Distroless final stage. Pulled from `ghcr.io/krisk248/grail`.

![grail dashboard](docs/screenshot.png)
*(screenshot lives at `docs/screenshot.png` if you commit one — optional)*

---

## Deploy on a server (QA / office / homelab)

The image is auto-built by GitHub Actions on every push to `main` and pushed to `ghcr.io/krisk248/grail:latest`. To run grail on any machine:

```bash
git clone https://github.com/krisk248/grail.git
cd grail
cp .env.example .env             # edit ADMIN_PASSWORD and any optional knobs
docker compose up -d             # pulls ghcr.io/krisk248/grail:latest and starts
```

- Dashboard: <http://localhost:6093>
- Admin TOML editor: <http://localhost:6093/admin>  *(password = `ADMIN_PASSWORD` from `.env`)*

On first boot grail seeds `data/config.toml` with an example. Edit it (in `/admin` or directly on disk) — the dashboard reloads in ~300 ms.

### Update later

```bash
docker compose pull
docker compose up -d
```

---

## Configure (`.env`)

| Var | Default | Notes |
|---|---|---|
| `ADMIN_PASSWORD` | `changeme` | Plain password; bcrypt-hashed in memory at boot. Change for prod. |
| `GRAIL_HOST_PORT` | `6093` | Host port grail listens on. |
| `INSECURE_SKIP_VERIFY` | `true` | Skip TLS cert verification on health checks. Set `false` only if your targets all chain to a public CA. |
| `UMAMI_SCRIPT_URL` | empty | Optional. Set to the URL of a Umami instance's `script.js` to enable analytics. |
| `UMAMI_WEBSITE_ID` | empty | Optional. The website UUID issued by Umami's "Add Website" dialog. |
| `GRAIL_TAG` | `latest` | Image tag to run. Pin to `v0.1.0` (or a `sha-...`) for reproducible deploys. |

Both `UMAMI_*` vars must be set together to enable analytics. Leave blank to disable — no script loaded, no requests, no cookies.

---

## TOML config schema

```toml
[site]
title  = "Internal Dashboard"
footer = "shown at the bottom of every page"

# Tag — top-level group, becomes a chip at the top of the dashboard.
[[column]]
id   = "infra"
name = "Infrastructure"

# Application — one card under its tag. Click to expand and see all URLs.
[[application]]
column = "infra"
id     = "grafana"
name   = "Grafana"

  # Direct URLs (simplest form, no sub-grouping).
  [[application.url]]
  id    = "grafana-main"
  name  = "Dashboards"
  url   = "https://grafana.internal/"
  alt   = "http://grafana.internal/"   # optional second URL (e.g. http counterpart)
  check = true                          # default true — green/red status dot
```

- IDs are **stable handles** — renames of `name` are safe, but renaming `id` loses check history.
- A URL's **aggregate** is shown on the parent app card: green if **any** URL inside is up.
- Soft-delete: removing an entry from the TOML preserves its history; re-adding the same `id` restores it.

Full schema with services, intervals, validation rules, and worked examples: **[`docs/adding-a-qa-site.md`](docs/adding-a-qa-site.md)**.

---

## Local development

You only need this section if you're changing grail's code.

```bash
make build       # builds frontend + embeds + builds docker image
docker compose up -d
```

Or run grail directly without docker:

```bash
make frontend                                  # build the SvelteKit bundle
DATA_DIR=./data ADMIN_PASSWORD=changeme go run ./cmd/grail
```

With the SvelteKit dev server (HMR on the frontend, Go API on :8080):

```bash
# terminal 1
go run ./cmd/grail

# terminal 2 (vite dev with proxy to :8080)
cd web && npm install && npm run dev
```

### Project layout

```
cmd/grail/             entrypoint, wiring, graceful shutdown
internal/db/           SQLite open + migrations + queries
internal/config/       TOML parse, validate, atomic write, fsnotify, reconcile
internal/checker/      supervisor + per-URL health-check goroutines
internal/session/      cookie + DB-backed sessions + CSRF
internal/http/         handlers + middleware + SPA fallback
internal/web/          go:embed of the SvelteKit build output
web/                   SvelteKit source
docs/                  documentation (start with adding-a-qa-site.md)
scripts/               CSV → TOML generator
data/                  bind-mounted at runtime; holds config.toml + grail.db
```

---

## Endpoints

```
GET  /                              SvelteKit dashboard
GET  /api/state                     tags → apps → services → URLs + latest check
GET  /api/site                      title, footer, optional umami config
GET  /api/url/{id}/history          last 100 checks for a URL
POST /api/url/{id}/check-now        admin only — force a recheck
POST /admin/login                   { password } → session + CSRF cookies
POST /admin/logout
GET  /admin/api/me                  { authenticated: bool }
GET  /admin/api/config              current TOML text
POST /admin/api/config              { toml } → validate + atomic write + reload
```

All mutating admin endpoints require both the session cookie and the
`X-CSRF-Token` header (double-submit pattern, value comes from the
`grail_csrf` cookie issued at login).

---

## License

MIT.
