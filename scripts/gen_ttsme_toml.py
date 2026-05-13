#!/usr/bin/env python3
"""Generate a grail config.toml from TTSME_URL_Routing_CSV.csv.

Apps are grouped by **tag** (ADX, ADIB, ADCB, ...) derived from the app name,
not by source server. Same-named apps on different servers stay distinct
(disambiguated by the dashboard's host subtitle).
"""
import csv
import re
import sys
from collections import defaultdict


def slug(s, max_len=42):
    s = (s or "").lower()
    s = re.sub(r"[^a-z0-9]+", "-", s)
    s = s.strip("-")
    if not s:
        s = "x"
    return s[:max_len].rstrip("-")


SERVER_TO_COL = {"S1", "S2", "S3", "CADDY", "INFRA"}

# Whole-app exclusions (case-insensitive match on App / Group column).
EXCLUDED_APPS = {
    name.lower() for name in [
        "EFG", "DFM Book", "VAPT Investor Portal", "Monitoring",
        "ENBD", "SFC", "YAQEEN", "ARBAH", "AKC", "ARB-SUKUK",
        # Round 2 (2026-05-13):
        "VAPT AIWS", "VAPT Config",   # whole VAPT tag gone
        "RE-IWS AKC",                 # whole AKC tag gone
    ]
}

# Specific (server, app) combinations to drop — leaves the same-named app on the
# other server in place. Lets us remove "Investor Onboarding" from S1 only.
EXCLUDED_SERVER_APP = {
    ("S1", "Investor Onboarding"),
}

# Drop specific URL paths from specific apps (case-sensitive path match).
EXCLUDED_PATHS = {
    "TADAWUL CMS": {"/MM-IPO-19/", "/MM-TADAWUL-19/"},
}

# After tagging + grouping, collapse all apps in the named tag into a single
# merged app with the supplied display name. The merged app keeps every URL
# from every source app.
TAG_MERGE = {
    "qib": "QIB",
    "mar": "MAR",
}

# Replace an app's URLs with a custom health-check URL list. Use when the
# documented paths don't return 200 (no UI) but a ping endpoint does.
APP_HEALTH_OVERRIDE = {
    "Mock Services": [
        ("ping", "https://qa1.ttsme.com:18002/mock-service/MockService/rest/mockData/ping"),
    ],
}

# Tag rules. First regex match wins.
TAG_RULES = [
    ("infra",      "Infrastructure",       r"\b(GRAFANA|CLOUDBEAVER|FORGEJO|GITLAB|DBMGMT|MONITORING)\b"),
    ("adx",        "ADX",                  r"\bADX\b|ADX[-\s]"),
    ("adib",       "ADIB",                 r"\bADIB\b"),
    ("adcb",       "ADCB",                 r"\bADCB\b"),
    ("dfm",        "DFM",                  r"\bDFM\b"),
    ("tadawul",    "TADAWUL",              r"\bTADAWUL\b"),
    ("qib",        "QIB",                  r"\bQIB(RI)?\b"),
    ("mar",        "MAR",                  r"\bMAR\b"),
    ("arc",        "ARC",                  r"\bARC([-\s]|$)"),
    ("brhub",      "BR-HUB",               r"\bBR[-]HUB\b|\bBRH\b"),
    ("onboarding", "Investor Onboarding",  r"\b(INVESTOR|ONBOARDING)\b"),
    ("mock",       "Mock",                 r"\bMOCK\b"),
    ("tabadul",    "TABADUL",              r"\bTABADUL\b"),
    ("mbank",      "MBANK",                r"\bMBANK\b"),
    ("ruya",       "RUYA",                 r"\bRUYA\b"),
    ("ttsre",      "TTS-RE",               r"\bTTS-RE\b"),
]

TAG_ORDER = [
    "adx", "adib", "adcb", "dfm", "tadawul", "qib", "mar",
    "arc", "brhub", "onboarding", "mock", "tabadul", "mbank",
    "ruya", "ttsre", "infra", "other",
]


def tag_for(app_name):
    n = app_name.upper()
    for tid, label, pattern in TAG_RULES:
        if re.search(pattern, n):
            return tid, label
    return "other", "Other"


def force_https_ttsdubai(u):
    if "ttsdubai.com" in u and u.startswith("http://"):
        return "https://" + u[len("http://"):]
    return u


def parse_rows(path):
    rows = []
    with open(path, newline="") as f:
        reader = csv.reader(f)
        next(reader, None)
        for row in reader:
            if not row or not row[0] or row[0] not in SERVER_TO_COL:
                continue
            while len(row) < 11:
                row.append("")
            rows.append({
                "server": row[0],
                "app":    row[3].strip(),
                "path":   row[4].strip(),
                "full":   force_https_ttsdubai(row[5].strip()),
                "notes":  row[10].strip(),
            })
    return rows


def dedupe_slash(urls):
    by_norm = {}
    order = []
    for r in urls:
        norm = r["full"].rstrip("/")
        if norm in by_norm:
            existing = by_norm[norm]
            if r["full"].endswith("/") and not existing["full"].endswith("/"):
                by_norm[norm] = r
        else:
            by_norm[norm] = r
            order.append(norm)
    return [by_norm[n] for n in order]


def main():
    rows = parse_rows(sys.argv[1])

    grouped = defaultdict(lambda: defaultdict(list))  # tag -> (server, app_name) -> [rows]
    apps_by_tag = defaultdict(list)                   # tag -> [(server, app_name), ...]
    tag_labels = {}

    for r in rows:
        if r["app"].lower() in EXCLUDED_APPS:
            continue
        if (r["server"], r["app"]) in EXCLUDED_SERVER_APP:
            continue
        if r["app"] in EXCLUDED_PATHS and r["path"] in EXCLUDED_PATHS[r["app"]]:
            continue
        tid, label = tag_for(r["app"])
        tag_labels[tid] = label
        key = (r["server"], r["app"])
        if key not in grouped[tid]:
            apps_by_tag[tid].append(key)
        grouped[tid][key].append(r)

    # Slash dedupe per (tag, server, app)
    for tid in grouped:
        for key in list(grouped[tid].keys()):
            grouped[tid][key] = dedupe_slash(grouped[tid][key])

    # Replace URLs for apps that have a custom health-check override.
    for tid in grouped:
        for key in list(grouped[tid].keys()):
            server, app_name = key
            if app_name in APP_HEALTH_OVERRIDE:
                custom = APP_HEALTH_OVERRIDE[app_name]
                grouped[tid][key] = [
                    {"server": server, "app": app_name, "path": cname, "full": curl, "notes": "(health check)"}
                    for (cname, curl) in custom
                ]

    # Merge all apps within a tag into ONE app (TAG_MERGE).
    for tid, merged_name in list(TAG_MERGE.items()):
        if tid not in grouped:
            continue
        all_rows = []
        first_server = None
        for key, rows_list in grouped[tid].items():
            for r in rows_list:
                if first_server is None:
                    first_server = r["server"]
                all_rows.append({**r, "app": merged_name})
        if not all_rows:
            continue
        new_key = (first_server or "S1", merged_name)
        grouped[tid] = {new_key: all_rows}
        apps_by_tag[tid] = [new_key]

    # Materialise ordered tag list
    ordered_tags = []
    seen = set()
    for tid in TAG_ORDER:
        if tid in grouped:
            ordered_tags.append((tid, tag_labels[tid]))
            seen.add(tid)
    for tid in grouped:
        if tid not in seen:
            ordered_tags.append((tid, tag_labels[tid]))

    out = []
    out.append("# Generated from TTSME_URL_Routing_CSV.csv.")
    out.append("# Apps grouped by TAG (not server). Exclusions, merges, and Mock ping")
    out.append("# override applied in generator.")
    out.append("")
    out.append("[site]")
    out.append('title  = "TTSME · QA URL Index"')
    out.append('footer = "TTSME QA Reverse-Proxy Routing · Apache mod_jk + Caddy"')
    out.append("")

    for tid, label in ordered_tags:
        out.append("[[column]]")
        out.append(f'id   = "{tid}"')
        out.append(f'name = "{label}"')
        out.append("")

    used_app_ids = set()
    used_url_ids = set()

    SERVER_SLUG = {"S1": "s1", "S2": "s2", "S3": "s3", "CADDY": "caddy", "INFRA": "infra"}

    for tid, _ in ordered_tags:
        for (server, app_name) in apps_by_tag[tid]:
            urls = grouped[tid][(server, app_name)]
            if not urls:
                continue
            server_slug = SERVER_SLUG.get(server, "x")
            base_aid = f"{tid}-{slug(app_name, 28)}-{server_slug}"
            aid = base_aid
            n = 2
            while aid in used_app_ids:
                aid = f"{base_aid}-{n}"
                n += 1
            used_app_ids.add(aid)
            out.append("[[application]]")
            out.append(f'column = "{tid}"')
            out.append(f'id     = "{aid}"')
            out.append(f'name   = "{app_name}"')
            out.append("")
            for r in urls:
                uname = r["path"] or "/"
                base_uid = f"{aid}-{slug(uname, 28)}"
                uid = base_uid
                n = 2
                while uid in used_url_ids:
                    uid = f"{base_uid}-{n}"
                    n += 1
                used_url_ids.add(uid)
                safe_name = uname.replace('"', '\\"')
                out.append("  [[application.url]]")
                out.append(f'  id    = "{uid}"')
                out.append(f'  name  = "{safe_name}"')
                out.append(f'  url   = "{r["full"]}"')
                out.append("  check = true")
                out.append("")

    print("\n".join(out))


if __name__ == "__main__":
    main()
