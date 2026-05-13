<script>
  import { onMount, onDestroy } from 'svelte';

  let state = $state(null);
  let error = $state('');
  let loading = $state(true);
  let timer;

  let query = $state('');
  let tagFilter = $state(null);
  let expanded = $state(new Set());
  let searchEl;

  async function load() {
    try {
      const r = await fetch('/api/state');
      if (!r.ok) throw new Error('http ' + r.status);
      state = await r.json();
      error = '';
    } catch (e) {
      error = String(e);
    } finally {
      loading = false;
    }
  }

  function handleKey(e) {
    const tag = document.activeElement?.tagName;
    if (e.key === '/' && tag !== 'INPUT' && tag !== 'TEXTAREA') {
      e.preventDefault();
      searchEl?.focus();
      searchEl?.select();
    } else if (e.key === 'Escape' && document.activeElement === searchEl) {
      query = '';
      searchEl?.blur();
    }
  }

  onMount(() => {
    try {
      const saved = localStorage.getItem('grail.expanded');
      if (saved) expanded = new Set(JSON.parse(saved));
    } catch {}
    load();
    timer = setInterval(load, 10_000);
    window.addEventListener('keydown', handleKey);
  });

  onDestroy(() => {
    clearInterval(timer);
    window.removeEventListener('keydown', handleKey);
  });

  function persist() {
    try { localStorage.setItem('grail.expanded', JSON.stringify([...expanded])); } catch {}
  }

  function toggle(appId) {
    const next = new Set(expanded);
    if (next.has(appId)) next.delete(appId); else next.add(appId);
    expanded = next;
    persist();
  }

  function expandAll(tags) {
    const next = new Set(expanded);
    for (const c of tags) for (const a of c.applications) next.add(a.id);
    expanded = next;
    persist();
  }

  function collapseAll() {
    expanded = new Set();
    persist();
  }

  function urlsOf(app) {
    const out = [];
    for (const s of app.services ?? []) {
      for (const u of s.urls ?? []) out.push(u);
    }
    return out;
  }

  function appStatus(app) {
    const urls = urlsOf(app);
    let hasCheck = false, anyOk = false, anyBad = false, anyPending = false;
    for (const u of urls) {
      if (!u.check) continue;
      hasCheck = true;
      if (u.ok === true) anyOk = true;
      else if (u.ok === false) anyBad = true;
      else anyPending = true;
    }
    if (!hasCheck) return 'off';
    if (anyOk) return 'ok';
    if (anyBad) return 'bad';
    return 'pending';
  }

  function appStatusTitle(app) {
    const kind = appStatus(app);
    const total = urlsOf(app).length;
    if (kind === 'off') return `${total} URLs · no checks`;
    if (kind === 'pending') return `${total} URLs · checking…`;
    if (kind === 'ok') {
      const okCount = urlsOf(app).filter(u => u.ok === true).length;
      return `${okCount}/${total} URLs reachable`;
    }
    return `0/${total} URLs reachable — all down`;
  }

  function statusKindOf(u) {
    if (!u.check) return 'off';
    if (u.ok === undefined || u.ok === null) return 'pending';
    return u.ok ? 'ok' : 'bad';
  }

  function statusTextOf(u) {
    if (!u.check) return '';
    if (u.ok === undefined || u.ok === null) return 'checking…';
    if (u.ok) return `${u.status_code} · ${u.latency_ms}ms`;
    return u.error ? truncate(u.error, 80) : `down (${u.status_code || '?'})`;
  }

  function truncate(s, n) {
    return s && s.length > n ? s.slice(0, n - 1) + '…' : s;
  }

  function appHost(app) {
    const urls = urlsOf(app);
    if (!urls.length) return '';
    try { return new URL(urls[0].url).host; } catch { return ''; }
  }

  function scheme(u) {
    try { return new URL(u).protocol.replace(':', ''); } catch { return 'http'; }
  }
  function rest(u) {
    return u.replace(/^https?:\/\//, '');
  }

  let filteredTags = $derived.by(() => {
    if (!state) return [];
    const q = query.trim().toLowerCase();
    const tags = [];
    for (const col of state.columns ?? []) {
      if (tagFilter && col.id !== tagFilter) continue;
      const apps = [];
      for (const a of col.applications ?? []) {
        if (q) {
          const appMatches = a.name.toLowerCase().includes(q) || col.name.toLowerCase().includes(q);
          const urlMatches = urlsOf(a).some(u =>
            (u.name?.toLowerCase().includes(q)) ||
            (u.url?.toLowerCase().includes(q)) ||
            (u.alt && u.alt.toLowerCase().includes(q))
          );
          if (!appMatches && !urlMatches) continue;
        }
        apps.push(a);
      }
      if (apps.length) tags.push({ ...col, applications: apps });
    }
    return tags;
  });

  let allTagChips = $derived.by(() => {
    if (!state) return [];
    return (state.columns ?? []).map(c => ({
      id: c.id,
      name: c.name,
      apps: (c.applications ?? []).length,
    }));
  });

  let totalApps = $derived((state?.columns ?? []).reduce((n, c) => n + (c.applications ?? []).length, 0));
  let totalURLs = $derived((state?.columns ?? []).reduce((n, c) =>
    n + (c.applications ?? []).reduce((m, a) => m + urlsOf(a).length, 0), 0));
  let visibleApps = $derived(filteredTags.reduce((n, c) => n + c.applications.length, 0));
  let visibleURLs = $derived(filteredTags.reduce((n, c) =>
    n + c.applications.reduce((m, a) => m + urlsOf(a).length, 0), 0));
</script>

<div class="search-bar">
  <div class="search-wrap">
    <span class="search-icon" aria-hidden="true">⌕</span>
    <input
      bind:this={searchEl}
      class="search"
      type="search"
      placeholder="Search app or URL —  press  /  to focus"
      bind:value={query}
    />
    {#if query}
      <button class="clear" onclick={() => (query = '')} aria-label="Clear">×</button>
    {/if}
  </div>
  <div class="toolbar-meta">
    <span class="counts">
      <span class="big">{visibleApps}</span>
      {#if visibleApps !== totalApps}<span class="dim">/ {totalApps}</span>{/if}
      <span class="dim">apps · {visibleURLs} URLs</span>
    </span>
    <button class="ghost xs" onclick={() => expandAll(filteredTags)}>Expand</button>
    <button class="ghost xs" onclick={collapseAll}>Collapse</button>
  </div>
</div>

<div class="chips">
  <button class="chip" class:active={!tagFilter} onclick={() => (tagFilter = null)}>
    All <span class="chip-count">{totalApps}</span>
  </button>
  {#each allTagChips as t}
    <button class="chip" class:active={tagFilter === t.id} onclick={() => (tagFilter = tagFilter === t.id ? null : t.id)}>
      {t.name} <span class="chip-count">{t.apps}</span>
    </button>
  {/each}
</div>

{#if loading}
  <p class="empty">Loading…</p>
{:else if error}
  <p class="empty" style="color: var(--bad)">{error}</p>
{:else if filteredTags.length === 0}
  <p class="empty">No matches{query ? ` for "${query}"` : ''}.</p>
{:else}
  {#each filteredTags as col (col.id)}
    <section class="tag-section">
      <header class="tag-header">
        <span class="tag-name">{col.name}</span>
        <span class="tag-meta">
          {col.applications.length} {col.applications.length === 1 ? 'app' : 'apps'} ·
          {col.applications.reduce((n, a) => n + urlsOf(a).length, 0)} URLs
        </span>
      </header>
      <div class="apps-grid">
        {#each col.applications as app (app.id)}
          {@const kind = appStatus(app)}
          {@const isOpen = expanded.has(app.id)}
          {@const urls = urlsOf(app)}
          <div class="app-card status-{kind}" class:open={isOpen}>
            <button class="app-summary" type="button" onclick={() => toggle(app.id)} aria-expanded={isOpen} title={appStatusTitle(app)}>
              <span class="dot dot-{kind}">
                {#if kind === 'ok' || kind === 'bad'}<span class="halo halo-{kind}"></span>{/if}
              </span>
              <div class="app-info">
                <div class="app-title">{app.name}</div>
                <div class="app-subtitle">{appHost(app)}</div>
              </div>
              <span class="app-counter">{urls.length}</span>
              <span class="app-caret" class:rot={!isOpen}>▾</span>
            </button>
            {#if isOpen}
              <div class="app-urls">
                {#each urls as u (u.id)}
                  {@const ukind = statusKindOf(u)}
                  <a class="url-row" href={u.url} target="_blank" rel="noopener noreferrer" title={u.url + (statusTextOf(u) ? ' — ' + statusTextOf(u) : '')}>
                    <span class="dot dot-{ukind} small">
                      {#if ukind === 'ok' || ukind === 'bad'}<span class="halo halo-{ukind}"></span>{/if}
                    </span>
                    <div class="url-meta">
                      <div class="url-path">{u.name}</div>
                      <div class="url-full">
                        <span class="scheme s-{scheme(u.url)}">{scheme(u.url)}://</span><span class="hostpath">{rest(u.url)}</span>
                      </div>
                    </div>
                    <div class="url-status">
                      {#if ukind === 'ok'}
                        <span class="latency">{u.latency_ms}ms</span>
                      {:else if ukind === 'bad'}
                        <span class="down" title={u.error}>{u.error ? truncate(u.error, 26) : 'DOWN'}</span>
                      {:else if ukind === 'pending'}
                        <span class="pending-text">…</span>
                      {/if}
                    </div>
                  </a>
                {/each}
              </div>
            {/if}
          </div>
        {/each}
      </div>
    </section>
  {/each}
{/if}
