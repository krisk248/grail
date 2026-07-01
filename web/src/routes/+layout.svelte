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

<div class="enter-flash" aria-hidden="true"></div>
<div class="app-enter">
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
</div>

<style>
  /* Classy arrival from the gateway — CSS-only (opacity), most efficient. */
  .app-enter{animation:appEnter .6s ease both}
  @keyframes appEnter{from{opacity:0}to{opacity:1}}

  /* Teal flash that dissipates, matching the gate's warp-out. */
  .enter-flash{
    position:fixed;inset:0;z-index:90;pointer-events:none;
    background:radial-gradient(circle at 50% 42%,rgba(210,255,247,.55),rgba(94,234,212,.22) 30%,transparent 62%);
    animation:flashOut .85s ease forwards;
  }
  @keyframes flashOut{from{opacity:1}to{opacity:0}}

  @media (prefers-reduced-motion: reduce){
    .app-enter{animation:none}
    .enter-flash{display:none}
  }
</style>
