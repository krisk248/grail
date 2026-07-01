<script>
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import '../app.css';

  let { children } = $props();
  let title = $state('');
  let footer = $state('');

  let isAdmin = $derived(page.url.pathname.startsWith('/admin'));

  async function loadSite() {
    try {
      const r = await fetch('/api/site');
      if (r.ok) {
        const j = await r.json();
        title = j.title || '';
        footer = j.footer || '';
        if (title && typeof document !== 'undefined') document.title = title;
        injectUmami(j.umami_script_url, j.umami_website_id);
      }
    } catch {}
  }

  function injectUmami(scriptUrl, websiteId) {
    if (!scriptUrl || !websiteId) return;
    if (typeof document === 'undefined') return;
    if (document.querySelector('script[data-umami-injected]')) return; // already loaded
    const s = document.createElement('script');
    s.defer = true;
    s.src = scriptUrl;
    s.setAttribute('data-website-id', websiteId);
    s.setAttribute('data-umami-injected', 'true');
    document.head.appendChild(s);
  }

  onMount(loadSite);
</script>

<header class="topbar">
  <a class="brand" href="/">
    <span class="logo">●</span>
    <span>{title || ' '}</span>
  </a>
  <nav>
    <a href="/admin">admin</a>
  </nav>
</header>

<main class:full={isAdmin}>
  {@render children?.()}
</main>

{#if !isAdmin && footer}
  <footer class="page-footer">{footer}</footer>
{/if}
