# Adding a new QA site to grail

This is the playbook for "I have a new app / new URL, where do I add it?".

There are **two workflows** depending on whether the change is permanent
(go into the golden copy + repo) or quick (just live-edit via the admin UI).

---

## Files at a glance

| Path | What it is |
|---|---|
| `data/config.toml` | **Live config**, what grail is currently running. Container writes to it. Editable via `/admin`. |
| `data/config.golden.toml` | **Golden copy** — your local canonical snapshot. **Gitignored by default** (contains internal hostnames). Move into a private companion repo if you want it versioned. |
| `data/config.example.toml` | Static example (Google + Servers stub) — left alone, kept as a reference. |
| `scripts/gen_ttsme_toml.py` | Generator that reads `TTSME_URL_Routing_CSV.csv` and emits a TOML matching grail's schema. Source of truth when adding things in bulk. |
| `TTSME_URL_Routing_CSV.csv` | Source data — one row per (server, app, URL). |

---

## Workflow 1 — quick: add via the admin UI

Best when you're adding one or two URLs to an existing app.

1. Open <http://localhost:6093/admin>
2. Sign in (password = `ADMIN_PASSWORD`)
3. In the textarea, find the `[[application]]` block you want to extend
4. Add a new `[[application.url]]` entry below it
5. Hit **Save** (or `Ctrl+S`)
6. Server validates the TOML. If invalid, an error appears at the bottom — fix and save again.
7. Dashboard reloads within ~300 ms

> Heads-up: the live edit goes only into `data/config.toml`. To make it permanent, **also copy the same block into `data/config.golden.toml`** and commit. See Workflow 2.

---

## Workflow 2 — permanent: edit the golden TOML

Best when the change should survive a re-deploy or be reviewed in git.

```bash
# 1. Edit the golden copy
$EDITOR data/config.golden.toml

# 2. Push it through grail's admin API (validates + atomic write + reload)
ADMIN_PASSWORD=changeme  # adjust if you changed it
GRAIL=http://localhost:6093

curl -s -c /tmp/c -X POST "$GRAIL/admin/login" \
  -H 'Content-Type: application/json' \
  -d "{\"password\":\"$ADMIN_PASSWORD\"}" > /dev/null
CSRF=$(grep grail_csrf /tmp/c | awk '{print $7}')

python3 -c "import json; print(json.dumps({'toml': open('data/config.golden.toml').read()}))" \
  | curl -s -b /tmp/c -X POST "$GRAIL/admin/api/config" \
    -H 'Content-Type: application/json' \
    -H "X-CSRF-Token: $CSRF" \
    --data-binary @-
rm -f /tmp/c
```

If the server replies `{"status":"ok"}` you're done — fsnotify will reload within ~300 ms.

If it replies `{"error":"..."}` the TOML didn't validate. Fix the message and retry. Live config is **untouched** on validation failure.

---

## Workflow 3 — bulk: edit the CSV, regenerate

Best when the underlying TTSME spreadsheet has changed (new server, new app group, multiple new URLs).

1. Edit `TTSME_URL_Routing_CSV.csv` with the new rows
2. `python3 scripts/gen_ttsme_toml.py TTSME_URL_Routing_CSV.csv > data/config.golden.toml`
3. Push it (see Workflow 2 step 2)

The generator handles:
- Slash dedupe (`X` dropped when `X/` exists)
- `http://*.ttsdubai.com` → `https://`
- Exclusions (see knobs below)
- Tag derivation from app name
- Tag merges (QIB / MAR collapsed into single apps)
- Custom-health URLs (e.g. Mock → ping endpoint)

---

## TOML schema cheat-sheet

```toml
[site]
title  = "TTSME · QA URL Index"
footer = "...shown at the bottom of the dashboard..."

# Tag — top-level group, becomes a chip at the top of the dashboard.
# id must be unique; lowercase letters/digits/dashes/underscores.
[[column]]
id   = "adx"          # stable id (don't rename)
name = "ADX"          # display label

# Application — one card in its tag's section.
# Stable id + name. column = "..." picks the tag.
[[application]]
column = "adx"
id     = "adx-ipo-caddy"
name   = "ADX IPO"
icon   = ""           # optional, shown as [icon] next to name (free-form for now)

  # Direct URLs on the application — easiest form, no service grouping.
  [[application.url]]
  id       = "adx-ipo-adxipo"          # stable id
  name     = "/ADXIPO/"                # display label (cyan monospace path)
  url      = "https://adxipo.ttsdubai.com/ADXIPO/"
  alt      = ""                        # optional second URL (e.g. http counterpart)
  check    = true                      # default true; the checker pings this URL
  interval = "60s"                     # default "60s"

  # Repeat as many [[application.url]] blocks as needed.

# Service-nested URLs (alternative form when you want subheadings inside one card)
[[application]]
column = "google"
id     = "google-x"
name   = "Google"

  [[application.service]]
  id   = "google-workspace"
  name = "Workspace"

    [[application.service.url]]
    id   = "gmail"
    name = "Gmail"
    url  = "https://mail.google.com"
```

### Rules to remember

- **IDs must be unique** across the whole file. Per entity: `[[column]].id`, `[[application]].id`, every URL's `id`.
- **IDs are stable handles** — renaming `name` is safe (check history survives). Renaming `id` is *not* — it acts like deleting and re-adding (history lost).
- **Soft delete** — if you remove an entry from the TOML, grail soft-deletes it (sets `deleted_at`). History is preserved. Re-adding the same `id` resurrects it.
- **Column field is required on applications** when at least one `[[column]]` block exists.

---

## Tags — auto-derived from the app name

The generator assigns each app to a tag based on a regex match against the app name (case-insensitive). The current rules live in `scripts/gen_ttsme_toml.py` (`TAG_RULES`):

| Tag | Matches names containing |
|---|---|
| Infrastructure | `GRAFANA / CLOUDBEAVER / FORGEJO / GITLAB / DBMGMT / MONITORING` |
| ADX | `ADX` (including `ADX AIBAN`, `ADX-SIP`, etc.) |
| ADIB | `ADIB` |
| ADCB | `ADCB` |
| DFM | `DFM` |
| TADAWUL | `TADAWUL` |
| QIB | `QIB` (or `QIBRI`) |
| MAR | `MAR` (word-boundary, won't match `MARRI` substring) |
| ARC | `ARC` (matches `ARC`, `ARC-QA`) |
| BR-HUB | `BR-HUB` or `BRH` |
| Investor Onboarding | `INVESTOR` or `ONBOARDING` |
| Mock | `MOCK` |
| TABADUL | `TABADUL` |
| MBANK | `MBANK` |
| RUYA | `RUYA` |
| TTS-RE | `TTS-RE` |

If a name matches none of the rules, the app falls into the `Other` tag.

**To add a new tag** (e.g. a new exchange tomorrow): edit `TAG_RULES` in `scripts/gen_ttsme_toml.py`. Add a tuple `(id, label, regex)` in priority order — first match wins. Also append the `id` to `TAG_ORDER` so the chip appears where you want.

---

## Generator knobs — when you don't want what the CSV says

Five dicts/sets in `scripts/gen_ttsme_toml.py` control trimming and shape:

```python
# 1. Drop whole apps by name (case-insensitive).
EXCLUDED_APPS = {
    "EFG", "DFM Book", "VAPT AIWS", ...
}

# 2. Drop a (server, app) combination — keeps the same-named app on the other server.
EXCLUDED_SERVER_APP = {
    ("S1", "Investor Onboarding"),
}

# 3. Drop specific paths inside an app.
EXCLUDED_PATHS = {
    "TADAWUL CMS": {"/MM-IPO-19/", "/MM-TADAWUL-19/"},
}

# 4. Merge all apps inside a tag into a single application.
TAG_MERGE = {
    "qib": "QIB",      # all QIB-tagged apps collapse into one called "QIB"
    "mar": "MAR",
}

# 5. Replace an app's URLs with a custom health-check endpoint.
APP_HEALTH_OVERRIDE = {
    "Mock Services": [
        ("ping", "https://qa1.ttsme.com:18002/mock-service/MockService/rest/mockData/ping"),
    ],
}
```

Edit, regenerate, push.

---

## Common scenarios

### "Add a new URL to an existing app"
Workflow 1 (admin UI) is fastest. Or in `data/config.golden.toml` find the `[[application]]` block, append a `[[application.url]]` block, push.

### "Add a brand-new app to an existing tag"
Add a new `[[application]]` block with `column = "<existing-tag-id>"`. Don't forget to give it a unique `id` and at least one URL.

### "Add a brand-new tag"
1. Add a `[[column]]` block at the top (after `[site]`)
2. Add `[[application]]` blocks that reference it via `column = "..."`
3. If you regenerate from CSV later, **add a TAG_RULES entry** so the generator knows where to put it; otherwise it'll fall into "Other".

### "Set up live health monitoring on a new internal hostname"
Make sure the container has `INSECURE_SKIP_VERIFY=true` if the hostname uses a private CA (most QA hosts do — see runtime notes). `check = true` on the URL turns on the green/red dot.

### "This app has no UI — use a different URL for the health check"
Use `APP_HEALTH_OVERRIDE` in the generator. The Mock app does this with the `/ping` endpoint.

### "Two apps share a name but are on different servers"
They stay as separate cards automatically (keyed by server). The dashboard's **host subtitle** disambiguates them — no need to rename either one.

---

## Verifying after a change

```bash
# 1. Quick: are columns / apps / URLs the shape you expected?
curl -s http://localhost:6093/api/state | python3 -c "
import sys, json
d = json.load(sys.stdin)
for c in d['columns']:
    apps = c['applications']
    urls = sum(len(s['urls']) for a in apps for s in a['services'])
    print(f'{c[\"name\"]:<22} {len(apps)} app(s)  {urls} URLs')
"

# 2. Wait ~60s for the first check on new URLs, then check status
curl -s http://localhost:6093/api/state | python3 -c "
import sys, json
d = json.load(sys.stdin)
ok=bad=pen=0
for c in d['columns']:
  for a in c['applications']:
    for s in a['services']:
      for u in s['urls']:
        if u.get('ok') is True: ok+=1
        elif u.get('ok') is False: bad+=1
        else: pen+=1
print(f'ok={ok} bad={bad} pending={pen}')
"

# 3. Drill into a specific app's URL statuses on the dashboard
# Open http://localhost:6093 and click the app card to expand.
```

---

## Reference

- Live dashboard: <http://localhost:6093>
- Admin: <http://localhost:6093/admin>
- Container: `docker logs grail` (slog JSON)
- Permanent restart cycle: see `data/config.example.toml` header comments or the project README
- Schema migrations: `internal/db/migrations/*.sql`
