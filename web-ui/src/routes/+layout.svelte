<script lang="ts">
  import { authStore } from '$lib/stores/auth';
  import { orgStore } from '$lib/stores/org';
  import { themeStore } from '$lib/stores/theme';
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { LayoutDashboard, Plug, User, Users, LogOut, Activity, Download, Cpu, Bell, Sun, Moon, Menu, X, Building2, Code, ExternalLink, MessageSquare, Zap } from 'lucide-svelte';
  import Toast from '$lib/components/Toast.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { confirmDialog } from '$lib/stores/confirm';
  import { organizations } from '$lib/api/client';
  import { onMount } from 'svelte';

  let sidebarOpen = false;

  $: activeScopeName = $authStore.isAdmin
    ? ($orgStore.organizations.find(org => org.id === $orgStore.selectedOrgId)?.name || 'All organizations')
    : 'Personal workspace';

  $: if (!$authStore.isAuthenticated && $page.url.pathname !== '/login') {
    goto('/login');
  }

  // Redirect non-admin away from admin pages (but allow /downloads for everyone)
  $: if ($authStore.isAuthenticated && !$authStore.isAdmin && !$page.url.pathname.startsWith('/portal') && !$page.url.pathname.startsWith('/downloads') && $page.url.pathname !== '/login') {
    goto('/portal');
  }

  // Load organizations on mount (admin only)
  onMount(async () => {
    if ($authStore.isAdmin && $authStore.token) {
      try {
        const orgs = await organizations.list($authStore.token);
        orgStore.setOrganizations(orgs);
        // org_admin: auto-lock to their org (no selector shown)
        if ($authStore.isOrgAdmin && $authStore.orgId) {
          orgStore.select($authStore.orgId);
        }
      } catch (e) {
        // Ignore — orgs endpoint may not exist on older API
      }
    }
  });

  // Global items — super_admin only
  const superAdminNavItems = [
    { href: '/dashboard', label: 'Dashboard', icon: LayoutDashboard },
    { href: '/debug', label: 'Debug', icon: Cpu },
  ];

  // Shared admin items (both super_admin and org_admin)
  const sharedAdminNavItems = [
    { href: '/downloads', label: 'Downloads', icon: Download },
    { href: '/api-reference', label: 'API Reference', icon: Code },
    { href: '/announcements', label: 'Announcements', icon: Bell },
  ];

  // Org-scoped items (filtered by selected org)
  const orgNavItems = [
    { href: '/publishers', label: 'Publishers', icon: Plug },
    { href: '/users', label: 'Users', icon: User },
    { href: '/groups', label: 'Groups', icon: Users },
  ];

  // Admin-only management items
  const adminNavExtra = [
    { href: '/organizations', label: 'Organizations', icon: Building2 },
  ];

  const userNavItems = [
    { href: '/portal', label: 'My Connection', icon: Activity },
    { href: '/downloads', label: 'Downloads', icon: Download },
  ];

  // Build nav based on role
  $: globalNavItems = $authStore.isSuperAdmin
    ? [...superAdminNavItems, ...sharedAdminNavItems]
    : $authStore.isOrgAdmin
      ? sharedAdminNavItems
      : [];

  $: navItems = $authStore.isAdmin ? globalNavItems : userNavItems;
  $: activeViewName = [...superAdminNavItems, ...sharedAdminNavItems, ...orgNavItems, ...adminNavExtra, ...userNavItems]
    .find(item => $page.url.pathname.startsWith(item.href))?.label || 'Workspace';

  function logout() {
    authStore.logout();
    goto('/login');
  }

  // Guard: redirect org_admin away from super_admin-only pages
  $: if ($authStore.isOrgAdmin && !$authStore.isSuperAdmin) {
    const superOnlyPaths = ['/dashboard', '/debug', '/organizations'];
    if (superOnlyPaths.some(p => $page.url.pathname.startsWith(p))) {
      goto('/publishers');
    }
  }

  function closeSidebar() {
    sidebarOpen = false;
  }

  // Close sidebar when route changes (mobile nav click)
  $: $page.url.pathname, closeSidebar();
</script>

{#if $authStore.isAuthenticated}
  <div class="app-layout">
    <div class="mesh-atmosphere mesh-only" aria-hidden="true">
      <div class="mesh-aurora mesh-aurora-one"></div>
      <div class="mesh-aurora mesh-aurora-two"></div>
      <svg class="mesh-atmosphere-lines" viewBox="0 0 1600 1000" preserveAspectRatio="xMidYMid slice">
        <defs>
          <linearGradient id="ambient-path" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0" stop-color="currentColor" stop-opacity="0" />
            <stop offset="0.5" stop-color="currentColor" stop-opacity="0.42" />
            <stop offset="1" stop-color="currentColor" stop-opacity="0" />
          </linearGradient>
        </defs>
        <path d="M180 780 C 440 620, 480 250, 820 290 S 1180 680, 1510 390" />
        <path d="M320 80 C 520 260, 780 120, 950 390 S 1280 760, 1580 690" />
        <path d="M540 990 C 690 720, 980 760, 1110 520 S 1330 90, 1590 160" />
        <g class="mesh-atmosphere-nodes">
          <circle cx="820" cy="290" r="4" />
          <circle cx="950" cy="390" r="5" />
          <circle cx="1110" cy="520" r="4" />
          <circle cx="1280" cy="760" r="3" />
          <circle cx="1510" cy="390" r="4" />
        </g>
      </svg>
    </div>
    <!-- Mobile top bar (visible < 1024px) -->
    <header class="mobile-topbar">
      <button class="hamburger" on:click={() => sidebarOpen = !sidebarOpen} aria-label="Toggle navigation">
        {#if sidebarOpen}
          <X size={22} strokeWidth={1.8} />
        {:else}
          <Menu size={22} strokeWidth={1.8} />
        {/if}
      </button>
      <div class="topbar-brand">
        <img src="/img/logo-wireztna.png" alt="WireZTNA" style="height: 20px; width: auto;" />
        <span>WireZTNA</span>
      </div>
      <div class="topbar-spacer"></div>
    </header>

    <!-- Backdrop (mobile only) -->
    {#if sidebarOpen}
      <div class="sidebar-backdrop" on:click={closeSidebar} role="presentation"></div>
    {/if}

    <nav class="sidebar" class:open={sidebarOpen}>
      <div class="logo">
        <div class="logo-icon">
          <img src="/img/logo-wireztna.png" alt="WireZTNA" class="logo-img" />
        </div>
        <div class="logo-text">
          <span class="logo-name">WireZTNA</span>
          <span class="logo-version">v1.0.5</span>
        </div>
        <span class="mesh-edition mesh-only">MESH / LIVE</span>
      </div>

      <!-- Global navigation (not org-scoped) -->
      {#if $authStore.isAdmin}
        <span class="mesh-nav-label mesh-only">Observe</span>
        <ul class="nav-global">
          {#each globalNavItems as item}
            <li>
              <a href={item.href} class:active={$page.url.pathname.startsWith(item.href)}>
                <svelte:component this={item.icon} size={18} strokeWidth={1.5} />
                {item.label}
              </a>
            </li>
          {/each}
        </ul>

        <!-- Org-scoped section with selector as header -->
        {#if $orgStore.loaded && $orgStore.organizations.length > 0}
          <span class="mesh-nav-label mesh-only">Access fabric</span>
          <div class="org-section">
            <div class="org-section-header">
              {#if $authStore.isSuperAdmin}
              <select
                class="org-select"
                value={$orgStore.selectedOrgId || ''}
                on:change={(e) => {
                  const val = e.currentTarget.value;
                  orgStore.select(val || null);
                }}
              >
                {#if $orgStore.organizations.length > 1}
                  <option value="">All organizations</option>
                {/if}
                {#each $orgStore.organizations as org}
                  <option value={org.id}>{org.name}</option>
                {/each}
              </select>
              {:else}
              <!-- org_admin: show org name as static label (no selector) -->
              <span class="org-label">{$orgStore.organizations.find(o => o.id === $authStore.orgId)?.name || 'My Organization'}</span>
              {/if}
            </div>
            <ul class="nav-org">
              {#each orgNavItems as item}
                <li>
                  <a href={item.href} class:active={$page.url.pathname.startsWith(item.href)}>
                    <svelte:component this={item.icon} size={18} strokeWidth={1.5} />
                    {item.label}
                  </a>
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <!-- Admin management (super_admin only) -->
        {#if $authStore.isSuperAdmin}
        <span class="mesh-nav-label mesh-only">System</span>
        <ul class="nav-admin">
          {#each adminNavExtra as item}
            <li>
              <a href={item.href} class:active={$page.url.pathname.startsWith(item.href)}>
                <svelte:component this={item.icon} size={18} strokeWidth={1.5} />
                {item.label}
              </a>
            </li>
          {/each}
        </ul>
        {/if}
      {:else}
        <!-- Non-admin: simple nav -->
        <span class="mesh-nav-label mesh-only">Workspace</span>
        <ul class="nav-global">
          {#each navItems as item}
            <li>
              <a href={item.href} class:active={$page.url.pathname.startsWith(item.href)}>
                <svelte:component this={item.icon} size={18} strokeWidth={1.5} />
                {item.label}
              </a>
            </li>
          {/each}
        </ul>
      {/if}
      <div class="sidebar-footer">
        <div class="sidebar-user">
          <div class="user-avatar">{($authStore.username || 'U')[0].toUpperCase()}</div>
          <div class="user-info">
            <span class="user-name">{$authStore.username || 'User'}</span>
            <span class="user-role">{$authStore.isAdmin ? 'Administrator' : 'User'}</span>
          </div>
        </div>
        <div class="sidebar-actions">
          <button class="theme-toggle" on:click={() => themeStore.toggle()} aria-label={$themeStore === 'dark' ? 'Use light theme' : 'Use dark theme'} title={$themeStore === 'dark' ? 'Use light theme' : 'Use dark theme'}>
            {#if $themeStore === 'dark'}
              <Sun size={14} strokeWidth={1.8} />
            {:else}
              <Moon size={14} strokeWidth={1.8} />
            {/if}
          </button>
          <button class="logout-btn" on:click={logout} aria-label="Log out" title="Log out">
            <LogOut size={15} strokeWidth={1.8} />
          </button>
        </div>
        <a href="mailto:support@wireztna.com" class="support-link">
          support@wireztna.com
        </a>
        <div class="sidebar-external-links">
          <a href="https://wireztna.com/proxy/" target="_blank" rel="noopener" class="external-link">
            <Zap size={13} strokeWidth={1.8} />
            <span>Proxy for AI Agents</span>
            <ExternalLink size={11} strokeWidth={1.8} />
          </a>
          <a href="https://wireztna.com/feedback/" target="_blank" rel="noopener" class="external-link">
            <MessageSquare size={13} strokeWidth={1.8} />
            <span>Feedback</span>
            <ExternalLink size={11} strokeWidth={1.8} />
          </a>
        </div>
      </div>
    </nav>
    <main class="content" data-route={$page.url.pathname}>
      <div class="mesh-context-bar mesh-only" aria-label="Mesh interface context">
        <div class="mesh-command-identity">
          <span class="mesh-command-symbol" aria-hidden="true">
            <span></span><span></span><span></span>
          </span>
          <div class="mesh-command-view">
            <span>Active surface</span>
            <strong>{activeViewName}</strong>
          </div>
        </div>
        <div class="mesh-context-copy">
          <span class="mesh-live">Fabric live</span>
          <span class="mesh-context-divider" aria-hidden="true"></span>
          <span class="mesh-context-scope">{activeScopeName}</span>
        </div>
        <span class="mesh-context-note">Zero Trust control plane</span>
      </div>
      <slot />
    </main>
  </div>
{:else}
  <slot />
{/if}

<Toast />
<ConfirmDialog
  open={$confirmDialog.open}
  title={$confirmDialog.title}
  message={$confirmDialog.message}
  confirmLabel={$confirmDialog.confirmLabel}
  variant={$confirmDialog.variant}
  requireInput={$confirmDialog.requireInput}
  on:confirm={() => { $confirmDialog.onConfirm(); confirmDialog.close(); }}
  on:cancel={() => confirmDialog.close()}
/>

<style>
  :global(*) {
    box-sizing: border-box;
    transition: background-color 0.2s ease, border-color 0.2s ease, color 0.2s ease, box-shadow 0.2s ease;
  }

  /* Opt out of theme transition for elements that need instant response */
  :global(input),
  :global(button),
  :global(a) {
    transition: background-color 0.15s ease, border-color 0.15s ease, color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease, opacity 0.15s ease;
  }

  :global(body) {
    margin: 0;
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    background: var(--bg-primary);
    color: var(--text-primary);
    -webkit-font-smoothing: antialiased;
    -moz-osx-font-smoothing: grayscale;
    transition: background-color 0.2s ease, color 0.2s ease;
  }

  /* Global dark theme tokens */
  :global(:root), :global([data-theme="dark"]) {
    --bg-primary: #0c0d12;
    --bg-surface: #14151a;
    --bg-elevated: #1c1d24;
    --bg-hover: #22232b;
    --border-subtle: #2a2b35;
    --border-default: #33343e;
    --text-primary: #e2e4e9;
    --text-secondary: #8b8d97;
    --text-muted: #5c5e69;
    /* Primary accent — single brand blue, matches wireztna.com (theme.css).
       Tokenized so buttons, focus rings, active nav, avatars share one hue. */
    --accent-primary: #5b8def;
    --accent-primary-strong: #3b6fe0;
    --accent-primary-dim: rgba(91, 141, 239, 0.12);
    --accent-green: #34d399;
    --accent-green-dim: rgba(52, 211, 153, 0.08);
    --accent-red: #f87171;
    --accent-red-dim: rgba(248, 113, 113, 0.08);
    --accent-orange: #fbbf24;
    --accent-orange-dim: rgba(251, 191, 36, 0.08);
    --accent-blue: #60a5fa;
    --accent-blue-dim: rgba(96, 165, 250, 0.08);
    --accent-purple: #a78bfa;
    --accent-purple-dim: rgba(167, 139, 250, 0.12);
    --radius-sm: 6px;
    --radius-md: 8px;
    --radius-lg: 12px;
    --transition: 0.15s ease;

    /* Shadows (enterprise depth) */
    --shadow-xs: 0 1px 2px rgba(0, 0, 0, 0.3);
    --shadow-sm: 0 2px 4px rgba(0, 0, 0, 0.2), 0 1px 2px rgba(0, 0, 0, 0.15);
    --shadow-md: 0 4px 12px rgba(0, 0, 0, 0.25), 0 2px 4px rgba(0, 0, 0, 0.15);
    --shadow-lg: 0 8px 24px rgba(0, 0, 0, 0.3), 0 4px 8px rgba(0, 0, 0, 0.15);

    /* Spacing scale */
    --space-xs: 0.25rem;
    --space-sm: 0.5rem;
    --space-md: 1rem;
    --space-lg: 1.5rem;
    --space-xl: 2rem;
    --space-2xl: 3rem;
  }

  /* Light theme — editorial "twin" of wireztna.com (see wireztna-web/assets/theme.css).
     White canvas, ink text, hairline borders (#e6e6ea), single blue accent (#3b6fe0),
     soft shadows. Semantic status colors kept clear (green/red/amber) for legibility. */
  :global([data-theme="light"]) {
    --bg-primary: #ffffff;
    --bg-surface: #ffffff;
    --bg-elevated: #f7f7f8;
    --bg-hover: #f1f1f3;
    --border-subtle: #ececee;
    --border-default: #e0e0e4;
    --text-primary: #0e0e12;
    --text-secondary: #3a3a44;
    --text-muted: #5c5c67;
    /* Primary accent — same brand blue as the public site. */
    --accent-primary: #3b6fe0;
    --accent-primary-strong: #2f59c4;
    --accent-primary-dim: rgba(59, 111, 224, 0.08);
    --accent-green: #099268;
    --accent-green-dim: rgba(9, 146, 104, 0.08);
    --accent-red: #e03131;
    --accent-red-dim: rgba(224, 49, 49, 0.08);
    --accent-orange: #c2410c;
    --accent-orange-dim: rgba(194, 65, 12, 0.08);
    --accent-blue: #2f59c4;
    --accent-blue-dim: rgba(47, 89, 196, 0.08);
    --accent-purple: #6d4bd8;
    --accent-purple-dim: rgba(109, 75, 216, 0.10);
    /* Soft, editorial shadows (barely-there depth on white). */
    --shadow-xs: 0 1px 2px rgba(14, 14, 18, 0.04);
    --shadow-sm: 0 1px 3px rgba(14, 14, 18, 0.05);
    --shadow-md: 0 4px 12px rgba(14, 14, 18, 0.06);
    --shadow-lg: 0 8px 24px rgba(14, 14, 18, 0.08);
  }

  :global([data-theme="light"] body) {
    background: var(--bg-primary);
    color: var(--text-primary);
  }

  /* ─── Typography Scale ─── */
  :global(h1) {
    font-size: 1.625rem;
    font-weight: 700;
    letter-spacing: -0.025em;
    line-height: 1.2;
    margin: 0;
    color: var(--text-primary);
  }

  :global(h2) {
    font-size: 1.125rem;
    font-weight: 600;
    letter-spacing: -0.015em;
    line-height: 1.3;
    color: var(--text-primary);
    margin: var(--space-xl) 0 var(--space-md);
  }

  :global(h3) {
    font-size: 0.9375rem;
    font-weight: 600;
    letter-spacing: -0.01em;
    color: var(--text-secondary);
    margin: var(--space-lg) 0 var(--space-sm);
  }

  /* ─── Page Header Pattern ─── */
  :global(.page-header) {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    margin-bottom: var(--space-xl);
    padding-bottom: var(--space-lg);
    border-bottom: 1px solid var(--border-subtle);
  }

  :global(.page-header-content) {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  :global(.page-header-actions) {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    flex-shrink: 0;
  }

  :global(.page-subtitle) {
    font-size: 0.8125rem;
    color: var(--text-muted);
    margin: 0;
    font-weight: 400;
    line-height: 1.4;
  }

  /* Global reusable styles */
  :global(.data-table) {
    width: 100%;
    border-collapse: collapse;
    background: var(--bg-surface);
    border: 1px solid color-mix(in srgb, var(--border-subtle) 60%, transparent);
    border-radius: var(--radius-md);
    overflow: hidden;
    box-shadow: var(--shadow-sm);
  }

  /* Ensure tables are scrollable on overflow */
  :global(.data-table-scroll) {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
    border-radius: var(--radius-md);
  }

  :global(.data-table thead) {
    background: var(--bg-elevated);
  }

  :global(.data-table th) {
    padding: 0.875rem 1rem;
    text-align: left;
    font-size: 0.75rem;
    font-weight: 500;
    color: var(--text-muted);
    border-bottom: 1px solid var(--border-subtle);
  }

  :global(.data-table td) {
    padding: 0.875rem 1rem;
    font-size: 0.875rem;
    border-bottom: 1px solid color-mix(in srgb, var(--border-subtle) 50%, transparent);
    color: var(--text-primary);
  }

  :global(.data-table tbody tr) {
    transition: background 0.12s ease, box-shadow 0.12s ease;
  }

  :global(.data-table tbody tr:hover) {
    background: var(--bg-hover);
  }

  :global(.data-table tbody tr.clickable-row) {
    cursor: pointer;
  }

  :global(.data-table tbody tr.clickable-row:hover) {
    background: var(--bg-elevated);
  }

  :global(.data-table tbody tr:last-child td) {
    border-bottom: none;
  }

  :global(.data-table-wrapper) {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-xs);
  }

  /* Make all data-tables scrollable on small screens */
  :global(.data-table) {
    min-width: 600px;
  }

  @media (max-width: 1024px) {
    :global(.data-table) {
      min-width: 500px;
    }
  }

  /* Badges */
  :global(.badge) {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.25rem 0.625rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 500;
    line-height: 1;
  }

  :global(.badge.online), :global(.badge.active), :global(.badge-green) {
    background: var(--accent-green-dim);
    color: var(--accent-green);
  }

  :global(.badge.offline), :global(.badge.expired), :global(.badge-red) {
    background: var(--accent-red-dim);
    color: var(--accent-red);
  }

  :global(.badge-orange) {
    background: var(--accent-orange-dim);
    color: var(--accent-orange);
  }

  :global(.badge-blue) {
    background: var(--accent-blue-dim);
    color: var(--accent-blue);
  }

  :global(.badge.pending) {
    background: var(--accent-orange-dim);
    color: var(--accent-orange);
  }

  /* Live pulse dot */
  :global(.live-dot) {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    position: relative;
  }

  :global(.live-dot.green) {
    background: var(--accent-green);
    box-shadow: 0 0 6px var(--accent-green);
  }

  :global(.live-dot.green::before) {
    content: '';
    position: absolute;
    inset: -3px;
    border-radius: 50%;
    background: var(--accent-green);
    opacity: 0.4;
    animation: pulse 2s ease-in-out infinite;
  }

  :global(.live-dot.red) {
    background: var(--accent-red);
  }

  @keyframes pulse {
    0%, 100% { transform: scale(1); opacity: 0.4; }
    50% { transform: scale(1.8); opacity: 0; }
  }

  /* Buttons */
  :global(.btn-primary) {
    background: var(--accent-primary);
    color: white;
    border: none;
    padding: 0.5rem 1.125rem;
    border-radius: var(--radius-sm);
    cursor: pointer;
    font-size: 0.8125rem;
    font-weight: 500;
    box-shadow: var(--shadow-xs);
    transition: all 0.15s ease;
  }

  :global(.btn-primary:hover) {
    background: var(--accent-primary-strong);
    box-shadow: var(--shadow-sm);
  }

  :global(.btn-primary:active) {
    background: var(--accent-primary-strong);
    box-shadow: var(--shadow-xs);
  }

  :global(.btn-secondary) {
    background: var(--bg-elevated);
    color: var(--text-primary);
    border: 1px solid var(--border-default);
    padding: 0.5rem 1.125rem;
    border-radius: var(--radius-sm);
    cursor: pointer;
    font-size: 0.8125rem;
    font-weight: 500;
    box-shadow: var(--shadow-xs);
    transition: all 0.2s ease;
  }

  :global(.btn-secondary:hover) {
    background: var(--bg-hover);
    border-color: var(--text-muted);
    box-shadow: var(--shadow-sm);
  }

  :global(.btn-danger) {
    background: var(--accent-red-dim);
    color: var(--accent-red);
    border: 1px solid rgba(248, 113, 113, 0.3);
    padding: 0.375rem 0.875rem;
    border-radius: var(--radius-sm);
    cursor: pointer;
    font-size: 0.8rem;
    font-weight: 500;
    transition: all 0.2s ease;
  }

  :global(.btn-danger:hover) {
    background: rgba(248, 113, 113, 0.18);
    box-shadow: 0 2px 8px rgba(248, 113, 113, 0.15);
  }

  :global(.btn-action) {
    background: var(--bg-elevated);
    color: var(--text-secondary);
    border: 1px solid var(--border-subtle);
    padding: 0.375rem 0.75rem;
    border-radius: var(--radius-sm);
    cursor: pointer;
    font-size: 0.8rem;
    transition: all 0.15s ease;
  }

  :global(.btn-action:hover) {
    background: var(--bg-hover);
    color: var(--text-primary);
    border-color: var(--border-default);
    box-shadow: var(--shadow-xs);
  }

  /* Form elements */
  :global(input[type="text"]),
  :global(input[type="password"]),
  :global(input[type="email"]),
  :global(input[type="number"]),
  :global(select),
  :global(textarea) {
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    color: var(--text-primary);
    padding: 0.5rem 0.75rem;
    border-radius: var(--radius-sm);
    font-size: 0.875rem;
    font-family: inherit;
    transition: border-color var(--transition);
  }

  :global(input:focus),
  :global(select:focus),
  :global(textarea:focus) {
    outline: none;
    border-color: var(--accent-primary);
    box-shadow: 0 0 0 3px var(--accent-primary-dim);
  }

  :global(input::placeholder) {
    color: var(--text-muted);
  }

  :global(code) {
    background: var(--bg-elevated);
    padding: 0.15rem 0.4rem;
    border-radius: 4px;
    font-size: 0.8rem;
    color: var(--accent-blue);
    font-family: 'JetBrains Mono', 'Fira Code', monospace;
  }

  /* Cards */
  :global(.form-card) {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    padding: 1.5rem;
    border-radius: var(--radius-md);
    margin-bottom: 1.5rem;
    box-shadow: var(--shadow-xs);
  }

  :global(.form-card form) {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    max-width: 500px;
  }

  :global(.form-card label) {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    font-size: 0.8125rem;
    font-weight: 500;
    color: var(--text-secondary);
  }

  /* Section divider */
  :global(.section-divider) {
    height: 1px;
    background: var(--border-subtle);
    margin: var(--space-xl) 0;
  }

  /* Empty state */
  :global(.empty-state) {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: var(--space-2xl) var(--space-lg);
    text-align: center;
    color: var(--text-muted);
  }

  :global(.empty-state h3) {
    margin: var(--space-md) 0 var(--space-xs);
    color: var(--text-secondary);
    font-size: 0.9375rem;
  }

  :global(.empty-state p) {
    margin: 0 0 var(--space-lg);
    font-size: 0.8125rem;
    max-width: 320px;
  }

  /* Scrollbar */
  :global(::-webkit-scrollbar) {
    width: 6px;
    height: 6px;
  }

  :global(::-webkit-scrollbar-track) {
    background: transparent;
  }

  :global(::-webkit-scrollbar-thumb) {
    background: var(--border-default);
    border-radius: 3px;
  }

  /* Layout */
  .app-layout {
    display: flex;
    min-height: 100vh;
  }

  .sidebar {
    width: 250px;
    background: var(--bg-surface);
    border-right: 1px solid var(--border-subtle);
    padding: 0;
    display: flex;
    flex-direction: column;
    position: fixed;
    top: 0;
    left: 0;
    bottom: 0;
    z-index: 50;
    overflow-y: auto;
    overflow-x: hidden;
    box-shadow: var(--shadow-sm);
  }

  .logo {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 1.25rem 1.5rem;
    border-bottom: 1px solid var(--border-subtle);
  }

  .logo-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    border-radius: 8px;
    flex-shrink: 0;
  }

  .logo-icon .logo-img {
    width: 28px;
    height: 28px;
    object-fit: contain;
  }

  .logo-text {
    display: flex;
    flex-direction: column;
  }

  .logo-name {
    font-weight: 700;
    font-size: 0.9375rem;
    color: var(--text-primary);
    letter-spacing: -0.02em;
  }

  .logo-version {
    font-size: 0.6875rem;
    color: var(--text-muted);
    font-weight: 400;
  }

  .org-section {
    margin: 0.25rem 0;
    border-top: 1px solid var(--border-subtle);
    border-bottom: 1px solid var(--border-subtle);
  }

  .org-section-header {
    padding: 0.625rem 1.25rem 0.25rem;
  }

  .org-select {
    width: 100%;
    padding: 0.375rem 0.5rem;
    font-size: 0.8rem;
    font-weight: 600;
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    background: var(--bg-secondary);
    color: var(--text-primary);
    cursor: pointer;
    outline: none;
    transition: border-color 0.15s;
  }

  .org-select:focus {
    border-color: var(--accent);
  }

  .nav-global, .nav-org, .nav-admin {
    list-style: none;
    padding: 0.25rem 0;
    margin: 0;
  }

  .nav-org {
    padding-bottom: 0.5rem;
  }

  .nav-admin {
    padding-top: 0.25rem;
  }

  .nav-section-label {
    padding: 0.75rem 0 0.25rem;
  }

  .sidebar ul {
    list-style: none;
    padding: 0.25rem 0;
    margin: 0;
  }

  .sidebar li a {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.625rem 1.5rem;
    color: var(--text-secondary);
    text-decoration: none;
    font-size: 0.8125rem;
    font-weight: 450;
    transition: all 0.15s ease;
    border-left: 3px solid transparent;
    margin: 1px 0;
  }

  .sidebar li a:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
  }

  .sidebar li a.active {
    background: var(--accent-primary-dim);
    color: var(--accent-primary);
    border-left-color: var(--accent-primary);
    font-weight: 500;
  }

  .sidebar-footer {
    padding: 0.875rem 1.25rem;
    border-top: 1px solid var(--border-subtle);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .support-link {
    width: 100%;
    text-align: center;
    font-size: 0.6875rem;
    color: var(--text-muted);
    text-decoration: none;
    padding-top: 0.5rem;
    border-top: 1px solid var(--border-subtle);
    transition: color 0.15s ease;
  }

  .support-link:hover {
    color: var(--accent-blue);
  }

  .sidebar-external-links {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    width: 100%;
    padding-top: 0.5rem;
  }

  .external-link {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.375rem 0.5rem;
    border-radius: 6px;
    font-size: 0.6875rem;
    color: var(--text-muted);
    text-decoration: none;
    transition: background-color 0.15s ease, color 0.15s ease;
  }

  .external-link span {
    flex: 1;
  }

  .external-link:hover {
    background: var(--bg-hover);
    color: var(--text-secondary);
  }

  .sidebar-user {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    min-width: 0;
  }

  .user-avatar {
    width: 30px;
    height: 30px;
    border-radius: 8px;
    background: linear-gradient(135deg, var(--accent-primary) 0%, var(--accent-primary-strong) 100%);
    color: white;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.75rem;
    font-weight: 600;
    flex-shrink: 0;
  }

  .user-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .user-name {
    font-size: 0.8125rem;
    font-weight: 500;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .user-role {
    font-size: 0.6875rem;
    color: var(--text-muted);
  }

  .sidebar-actions {
    display: flex;
    gap: 0.375rem;
    flex-shrink: 0;
  }

  .theme-toggle {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    background: var(--bg-elevated);
    color: var(--text-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-sm);
    cursor: pointer;
    font-size: 0.75rem;
    transition: all 0.15s ease;
  }

  .theme-toggle:hover {
    background: var(--bg-hover);
    border-color: var(--border-default);
  }

  .logout-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    background: var(--accent-red-dim);
    color: var(--accent-red);
    border: 1px solid rgba(248, 113, 113, 0.2);
    border-radius: var(--radius-sm);
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .logout-btn:hover {
    background: rgba(248, 113, 113, 0.25);
    border-color: rgba(248, 113, 113, 0.4);
  }

  .content {
    flex: 1;
    margin-left: 250px;
    padding: var(--space-xl) var(--space-2xl);
    overflow-y: auto;
    overflow-x: hidden;
    min-height: 100vh;
    max-width: 100vw;
  }

  .content > :global(*) {
    max-width: 1280px;
  }

  /* ─── Mobile top bar ─── */
  .mobile-topbar {
    display: none;
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    height: 56px;
    background: var(--bg-surface);
    border-bottom: 1px solid var(--border-subtle);
    z-index: 60;
    align-items: center;
    padding: 0 1rem;
    gap: 0.75rem;
    box-shadow: var(--shadow-sm);
  }

  .hamburger {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 36px;
    height: 36px;
    background: none;
    border: none;
    color: var(--text-primary);
    cursor: pointer;
    border-radius: var(--radius-sm);
    transition: background 0.15s ease;
  }

  .hamburger:hover {
    background: var(--bg-hover);
  }

  .topbar-brand {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--text-primary);
    font-weight: 600;
    font-size: 0.9375rem;
  }

  .topbar-spacer { flex: 1; }

  .sidebar-backdrop {
    display: none;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    backdrop-filter: blur(2px);
    -webkit-backdrop-filter: blur(2px);
    z-index: 45;
  }

  /* ─── Responsive: tablet and below ─── */
  @media (max-width: 1024px) {
    .mobile-topbar {
      display: flex;
    }

    .sidebar-backdrop {
      display: block;
    }

    .sidebar {
      transform: translateX(-100%);
      transition: transform 0.25s ease;
      z-index: 50;
    }

    .sidebar.open {
      transform: translateX(0);
    }

    .content {
      margin-left: 0;
      padding: calc(56px + var(--space-lg)) var(--space-lg) var(--space-lg);
    }
  }

  /* ─── Responsive: phone ─── */
  @media (max-width: 640px) {
    .content {
      padding: calc(56px + var(--space-md)) var(--space-md) var(--space-md);
    }

    .sidebar {
      width: 280px;
    }
  }
</style>
