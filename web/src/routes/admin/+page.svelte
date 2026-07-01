<script>
  import { onMount } from 'svelte';

  let authed = $state(false);
  let booted = $state(false);
  let password = $state('');
  let loginErr = $state('');
  let toml = $state('');
  let saveErr = $state('');
  let saveOk = $state('');
  let busy = $state(false);

  function csrfToken() {
    const m = document.cookie.match(/(?:^|;\s*)grail_csrf=([^;]+)/);
    return m ? decodeURIComponent(m[1]) : '';
  }

  async function checkMe() {
    try {
      const r = await fetch('/admin/api/me');
      const j = await r.json();
      authed = !!j.authenticated;
      if (authed) await loadConfig();
    } catch {}
    booted = true;
  }

  async function loadConfig() {
    const r = await fetch('/admin/api/config');
    if (!r.ok) {
      saveErr = 'Failed to load config: ' + r.status;
      return;
    }
    const j = await r.json();
    toml = j.toml;
  }

  async function login() {
    loginErr = '';
    busy = true;
    try {
      const r = await fetch('/admin/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password })
      });
      if (!r.ok) {
        const j = await r.json().catch(() => ({}));
        loginErr = j.error || ('Login failed (' + r.status + ')');
        return;
      }
      password = '';
      authed = true;
      await loadConfig();
    } catch (e) {
      loginErr = String(e);
    } finally {
      busy = false;
    }
  }

  async function logout() {
    await fetch('/admin/logout', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() } });
    authed = false;
    toml = '';
  }

  async function logoutAll() {
    await fetch('/admin/logout-all', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() } });
    authed = false;
    toml = '';
  }

  async function save() {
    saveErr = '';
    saveOk = '';
    busy = true;
    try {
      const r = await fetch('/admin/api/config', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': csrfToken()
        },
        body: JSON.stringify({ toml })
      });
      if (!r.ok) {
        const j = await r.json().catch(() => ({}));
        saveErr = j.error || ('Save failed (' + r.status + ')');
        return;
      }
      saveOk = 'Saved · dashboard will reload.';
      setTimeout(() => (saveOk = ''), 3000);
    } catch (e) {
      saveErr = String(e);
    } finally {
      busy = false;
    }
  }

  function handleTab(e) {
    if (e.key === 'Tab') {
      e.preventDefault();
      const ta = e.target;
      const s = ta.selectionStart, end = ta.selectionEnd;
      const insert = '  ';
      toml = toml.substring(0, s) + insert + toml.substring(end);
      // restore caret after re-render
      queueMicrotask(() => { ta.selectionStart = ta.selectionEnd = s + insert.length; });
    }
    // Ctrl/Cmd+S
    if ((e.ctrlKey || e.metaKey) && (e.key === 's' || e.key === 'S')) {
      e.preventDefault();
      save();
    }
  }

  onMount(checkMe);
</script>

{#if !booted}
  <p class="empty">Loading…</p>
{:else if !authed}
  <div class="admin-shell">
    <div class="admin-card">
      <h1>Admin login</h1>
      <p style="color: var(--muted); margin-top: -8px;">
        Enter the admin password (set via <code>ADMIN_PASSWORD</code>).
      </p>
      <label for="pw">Password</label>
      <input id="pw" type="password" bind:value={password} on:keydown={(e) => e.key === 'Enter' && login()} />
      <div class="row" style="margin-top: 14px; justify-content: flex-end;">
        <button on:click={login} disabled={busy || !password}>Sign in</button>
      </div>
      {#if loginErr}<p class="error">{loginErr}</p>{/if}
    </div>
  </div>
{:else}
  <div class="admin-fullpage">
    <header class="admin-toolbar">
      <div class="title-block">
        <h1>config.toml</h1>
        <span class="muted">Edits validate on save · dashboard reloads automatically · <kbd>Ctrl</kbd>+<kbd>S</kbd> to save</span>
      </div>
      <div class="actions">
        <a class="ghost-link" href="/"><button class="ghost">Dashboard</button></a>
        <button class="ghost" on:click={logoutAll} title="Invalidate all admin sessions everywhere">Sign out all</button>
        <button class="ghost" on:click={logout}>Sign out</button>
        <button class="primary" on:click={save} disabled={busy}>Save</button>
      </div>
    </header>
    <textarea
      bind:value={toml}
      spellcheck="false"
      autocorrect="off"
      autocapitalize="off"
      wrap="off"
      on:keydown={handleTab}
    ></textarea>
    {#if saveErr || saveOk}
      <footer class="admin-statusbar" class:bad={saveErr}>
        {#if saveErr}<span class="error-text">{saveErr}</span>{/if}
        {#if saveOk}<span class="success-text">{saveOk}</span>{/if}
      </footer>
    {/if}
  </div>
{/if}
