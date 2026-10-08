<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { goto } from '$app/navigation';
  import { fade } from 'svelte/transition';
  import { tweened } from 'svelte/motion';
  import { cubicOut } from 'svelte/easing';
  import { authStore } from '$lib/stores/auth';
  import { publishers, users, groups, audit, clients, diagnostics } from '$lib/api/client';
  import { Plug, User } from 'lucide-svelte';

  function resolveApiUrl(): string {
    return '';
  }

  let stats = { publishers: 0, publishersOnline: 0, users: 0, groups: 0, clientsConnected: 0 };
  let publisherList: any[] = [];
  let connectedClients: any[] = [];

  // Animated counters
  const tweenOpts = { duration: 600, easing: cubicOut };
  const animPublishersOnline = tweened(0, tweenOpts);
  const animPublishers = tweened(0, tweenOpts);
  const animClients = tweened(0, tweenOpts);
  const animUsers = tweened(0, tweenOpts);
  const animGroups = tweened(0, tweenOpts);

  $: {
    animPublishersOnline.set(stats.publishersOnline);
    animPublishers.set(stats.publishers);
    animClients.set(stats.clientsConnected);
    animUsers.set(stats.users);
    animGroups.set(stats.groups);
  }
  let userMap: Record<string, string> = {};
  let recentLogs: any[] = [];
  let loading = true;
  let refreshInterval: ReturnType<typeof setInterval>;
  let healthStatus: { status: string; issues_count: number } | null = null;
  let brokerMetrics: any = null;
  let handshakeMap: Record<string, number | null> = {};

  async function loadDashboard() {
    const token = $authStore.token;
    if (!token) {
      loading = false;
      return;
    }
    try {
      const [pubList, userList, groupList, logs, connectedList, health] = await Promise.all([
        publishers.list(token),
        users.list(token),
        groups.list(token),
        audit.logs(token, { limit: 20 }),
        clients.connected(token).catch(() => []),
        diagnostics.quick().catch(() => null),
      ]);

      // Fetch broker metrics (non-blocking)
      brokerMetrics = await fetch(
        `${resolveApiUrl()}/api/v1/debug/system-metrics`,
        { headers: { Authorization: `Bearer ${token}` } }
      ).then(r => r.ok ? r.json() : null).catch(() => null);

      // Fetch real-time WG handshake ages (non-blocking)
      handshakeMap = await fetch(
        `${resolveApiUrl()}/api/v1/publishers/handshakes`,
        { headers: { Authorization: `Bearer ${token}` } }
      ).then(r => r.ok ? r.json() : {}).catch(() => ({}));

      publisherList = pubList;
      connectedClients = connectedList;
      healthStatus = health;

      // Build user ID → username map for audit log display
      userMap = {};
      for (const u of userList) {
        userMap[u.id] = u.username;
      }

      stats = {
        publishers: pubList.length,
        publishersOnline: pubList.filter((p: any) => p.status === 'online').length,
        users: userList.length,
        groups: groupList.length,
        clientsConnected: connectedList.filter((c: any) => c.is_connected).length,
      };
      recentLogs = logs;
    } catch (e) {
      console.error(e);
    } finally {
      loading = false;
    }
  }

  onMount(async () => {
    await loadDashboard();
    refreshInterval = setInterval(loadDashboard, 15000);
  });

  onDestroy(() => {
    if (refreshInterval) clearInterval(refreshInterval);
  });

  function actionColor(action: string): string {
    if (action.includes('renew')) return 'badge-green';
    if (action.includes('revoke') || action.includes('denied')) return 'badge-red';
    if (action.includes('health') || action.includes('change')) return 'badge-orange';
    return 'badge-blue';
  }

  function smartTime(timestamp: string): string {
    const date = new Date(timestamp);
    if (isNaN(date.getTime())) return '—';
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();

    // Very recent: show relative
    if (diffMs < 60_000) return 'just now';

    const time = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });

    // Today
    const isToday = date.toDateString() === now.toDateString();
    if (isToday) return `Today ${time}`;

    // Yesterday
    const yesterday = new Date(now);
    yesterday.setDate(yesterday.getDate() - 1);
    if (date.toDateString() === yesterday.toDateString()) return `Yesterday ${time}`;

    // This year: "18 Jul 14:32"
    const day = date.getDate();
    const month = date.toLocaleString('en', { month: 'short' });
    if (date.getFullYear() === now.getFullYear()) return `${day} ${month} ${time}`;

    // Older: "18 Jul 2025"
    return `${day} ${month} ${date.getFullYear()}`;
  }

  function relativeTime(timestamp: string): string {
    const now = Date.now();
    const t = new Date(timestamp).getTime();
    if (isNaN(t)) return '—';
    const diff = Math.floor((now - t) / 1000);

    if (diff < 0) return 'just now';
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  function formatBytes(bytes: number): string {
    if (bytes === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return `${(bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
  }

  function formatUptime(seconds: number): string {
    if (!seconds) return '—';
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    if (days > 0) return `${days}d ${hours}h`;
    const mins = Math.floor((seconds % 3600) / 60);
    return `${hours}h ${mins}m`;
  }

  function humanDetail(action: string, detail: string | null): string {
    if (!detail) {
      // Derive from action when no detail provided
      const fallbacks: Record<string, string> = {
        'session_renew': 'Session started',
        'session_revoke': 'Session revoked',
        'publisher_reset': 'Publisher reset',
        'publisher_disabled': 'Publisher disabled',
        'publisher_enabled': 'Publisher enabled',
        'publisher_health_change': 'Health status changed',
      };
      return fallbacks[action] || '—';
    }

    // session_renew: "Session a3f2b81c... created (TTL: 8h, group=proj...)"
    if (action === 'session_renew') {
      const groupMatch = detail.match(/group=([^)]+)\.\.\./);
      if (groupMatch) return `New session · group ${groupMatch[1]}…`;
      const ttlMatch = detail.match(/TTL:\s*(\d+h)/);
      return ttlMatch ? `New session · ${ttlMatch[1]}` : 'New session';
    }

    // session_revoke: "Session a3f2b81c... revoked by admin"
    if (action === 'session_revoke') return 'Session revoked by admin';

    // publisher_health_change: "Publisher 'name' is now healthy/OFFLINE" (new format)
    // or: "Publisher abc12345... in ns-xxx is now healthy/OFFLINE" (legacy format)
    if (action === 'publisher_health_change') {
      const nameMatch = detail.match(/Publisher '([^']+)'/);
      const legacyMatch = detail.match(/Publisher (\S+)\.{3}\s+in\s+(ns-\S+)/);
      const pubName = nameMatch ? nameMatch[1] : (legacyMatch ? legacyMatch[2] : null);
      if (detail.includes('OFFLINE')) return pubName ? `${pubName} went offline` : 'Went offline';
      if (detail.includes('healthy')) return pubName ? `${pubName} back online` : 'Back online';
      return pubName ? `${pubName} health changed` : 'Health changed';
    }

    // publisher_reset/disabled/enabled: "Publisher 'name' ..."
    const pubNameMatch = detail.match(/Publisher '([^']+)'/);
    if (pubNameMatch) {
      const name = pubNameMatch[1];
      if (action === 'publisher_reset') return `${name} — forced reconnect`;
      if (action === 'publisher_disabled') return `${name} disabled`;
      if (action === 'publisher_enabled') return `${name} re-enabled`;
    }

    // Fallback: truncate long details
    return detail.length > 50 ? detail.slice(0, 47) + '…' : detail;
  }

  function resolveUser(userId: string | null): string {
    if (!userId) return '—';
    return userMap[userId] || userId.slice(0, 8) + '...';
  }
</script>

<header class="page-header dashboard-header">
  <div class="page-header-content">
    <h1>Dashboard</h1>
    <p class="page-subtitle">Zero Trust network monitoring. Auto-refreshes every 15s.</p>
  </div>
</header>

{#if loading}
  <div class="loading-placeholder">
    <div class="skeleton-bar wide"></div>
    <div class="skeleton-grid">
      <div class="skeleton-card"></div>
      <div class="skeleton-card"></div>
      <div class="skeleton-card"></div>
      <div class="skeleton-card"></div>
    </div>
    <div class="skeleton-bar"></div>
    <div class="skeleton-bar"></div>
    <div class="skeleton-bar narrow"></div>
  </div>
{:else}
  <div in:fade={{ duration: 150 }}>
  <!-- Health Status Banner -->
  {#if healthStatus}
    <div class="health-banner health-{healthStatus.status}">
      <span class="health-dot"></span>
      <span class="health-label">System Health:</span>
      <span class="health-value">{healthStatus.status.toUpperCase()}</span>
      {#if healthStatus.issues_count > 0}
        <span class="health-issues">{healthStatus.issues_count} issue{healthStatus.issues_count > 1 ? 's' : ''} detected</span>
      {/if}
    </div>
  {/if}

  <!-- Broker Resources Bar -->
  {#if brokerMetrics}
    <div class="broker-metrics-bar">
      <a href="/debug" class="metric-pill" class:metric-warn={brokerMetrics.cpu.usage_percent > 70} class:metric-crit={brokerMetrics.cpu.usage_percent > 90}>
        <span class="metric-label">CPU</span>
        <span class="metric-value">{brokerMetrics.cpu.usage_percent}%</span>
      </a>
      <a href="/debug" class="metric-pill" class:metric-warn={brokerMetrics.memory.usage_percent > 70} class:metric-crit={brokerMetrics.memory.usage_percent > 90}>
        <span class="metric-label">RAM</span>
        <span class="metric-value">{brokerMetrics.memory.usage_percent}%</span>
      </a>
      <a href="/debug" class="metric-pill" class:metric-warn={brokerMetrics.conntrack.usage_percent > 50} class:metric-crit={brokerMetrics.conntrack.usage_percent > 80}>
        <span class="metric-label">Conntrack</span>
        <span class="metric-value">{brokerMetrics.conntrack.current.toLocaleString()}/{(brokerMetrics.conntrack.max/1000).toFixed(0)}k</span>
      </a>
      <a href="/debug" class="metric-pill">
        <span class="metric-label">Peers</span>
        <span class="metric-value">{brokerMetrics.wireguard.client_peers}/{brokerMetrics.capacity.estimated_max_users}</span>
      </a>
      <a href="/debug" class="metric-pill">
        <span class="metric-label">Uptime</span>
        <span class="metric-value">{formatUptime(brokerMetrics.host.uptime_seconds)}</span>
      </a>
      {#if brokerMetrics.capacity.bottleneck}
        <span class="metric-pill metric-crit">
          <span class="metric-label">Bottleneck</span>
          <span class="metric-value">{brokerMetrics.capacity.bottleneck}</span>
        </span>
      {/if}
    </div>
  {/if}

  <!-- Mesh 2.1: a live, navigable representation of the existing access data. -->
  <section class="fabric-hero mesh-only" aria-labelledby="fabric-hero-title">
    <div class="fabric-hero-copy">
      <span class="fabric-eyebrow"><i></i> Realtime access graph</span>
      <h2 id="fabric-hero-title">Your private network,<br /><em>visibly alive.</em></h2>
      <p>Identities, policies and private resources converge into one observable access fabric.</p>
      <div class="fabric-facts" aria-label="Fabric summary">
        <span><strong>{stats.publishersOnline}</strong> publishers online</span>
        <span><strong>{stats.clientsConnected}</strong> active clients</span>
        <span><strong>{stats.groups}</strong> policy groups</span>
      </div>
    </div>

    <nav class="fabric-stage" aria-label="Access fabric navigation">
      <svg class="fabric-paths" viewBox="0 0 660 360" aria-hidden="true">
        <defs>
          <linearGradient id="fabric-line-a" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0" stop-color="var(--mesh-cyan, #6f9bf0)" stop-opacity="0.08" />
            <stop offset="0.48" stop-color="var(--mesh-cyan, #6f9bf0)" stop-opacity="0.9" />
            <stop offset="1" stop-color="var(--mesh-cyan, #6f9bf0)" stop-opacity="0.08" />
          </linearGradient>
          <linearGradient id="fabric-line-b" x1="0" y1="1" x2="1" y2="0">
            <stop offset="0" stop-color="var(--mesh-electric, #3b6fe0)" stop-opacity="0.06" />
            <stop offset="0.52" stop-color="var(--mesh-electric, #3b6fe0)" stop-opacity="0.75" />
            <stop offset="1" stop-color="var(--mesh-cyan, #6f9bf0)" stop-opacity="0.1" />
          </linearGradient>
          <radialGradient id="fabric-core-glow">
            <stop offset="0" stop-color="var(--mesh-electric, #3b6fe0)" stop-opacity="0.3" />
            <stop offset="1" stop-color="var(--mesh-electric, #3b6fe0)" stop-opacity="0" />
          </radialGradient>
        </defs>
        <circle cx="330" cy="180" r="132" class="fabric-orbit orbit-outer" />
        <circle cx="330" cy="180" r="82" class="fabric-orbit orbit-inner" />
        <circle cx="330" cy="180" r="122" fill="url(#fabric-core-glow)" opacity="0.25" />
        <path class="fabric-route route-a" d="M102 92 C190 92 208 174 330 180 S474 88 570 92" />
        <path class="fabric-route route-b" d="M104 278 C196 278 222 192 330 180 S474 270 568 270" />
        <path class="fabric-route route-c" d="M330 180 C330 112 382 58 460 42" />
        <path class="fabric-packet packet-a" d="M102 92 C190 92 208 174 330 180 S474 88 570 92" />
        <path class="fabric-packet packet-b" d="M104 278 C196 278 222 192 330 180 S474 270 568 270" />
      </svg>

      <span class="fabric-core-node">
        <i></i>
        <strong>WireZTNA</strong>
        <small>broker fabric</small>
      </span>
      <a href="/publishers" class="fabric-node fabric-node-publishers">
        <span>{stats.publishersOnline}/{stats.publishers}</span>
        <small>Publishers</small>
      </a>
      <a href="/users" class="fabric-node fabric-node-clients">
        <span>{stats.clientsConnected}</span>
        <small>Clients</small>
      </a>
      <a href="/users" class="fabric-node fabric-node-users">
        <span>{stats.users}</span>
        <small>Identities</small>
      </a>
      <a href="/groups" class="fabric-node fabric-node-policies">
        <span>{stats.groups}</span>
        <small>Policies</small>
      </a>
    </nav>
  </section>

  <!-- Getting Started Wizard (shown when setup is incomplete) -->
  {#if stats.publishers === 0 || stats.users <= 1 || stats.groups === 0}
    <div class="getting-started-card">
      <div class="gs-header">
        <h3>Getting Started</h3>
        <span class="gs-subtitle">Complete these steps to get your Zero Trust network running.</span>
      </div>
      <div class="gs-steps">
        <a href="/publishers" class="gs-step" class:done={stats.publishers > 0}>
          <span class="gs-step-num">{stats.publishers > 0 ? '✓' : '1'}</span>
          <div class="gs-step-content">
            <strong>Deploy a Publisher</strong>
            <p>Go to Publishers and click "+ Add Publisher" to get a one-liner install command for your server.</p>
          </div>
        </a>
        <a href="/users" class="gs-step" class:done={stats.users > 1}>
          <span class="gs-step-num">{stats.users > 1 ? '✓' : '2'}</span>
          <div class="gs-step-content">
            <strong>Create Users</strong>
            <p>Add user accounts in Users. They'll connect using the WireZTNA client.</p>
          </div>
        </a>
        <a href="/groups" class="gs-step" class:done={stats.groups > 0}>
          <span class="gs-step-num">{stats.groups > 0 ? '✓' : '3'}</span>
          <div class="gs-step-content">
            <strong>Create a Group & Link Access</strong>
            <p>In Groups, create a group, add users as members, and assign publishers to define who can reach what.</p>
          </div>
        </a>
        <a href="/users" class="gs-step" class:done={stats.clientsConnected > 0}>
          <span class="gs-step-num">{stats.clientsConnected > 0 ? '✓' : '4'}</span>
          <div class="gs-step-content">
            <strong>Enroll a Client</strong>
            <p>Select a user → Enrollment tab → Generate Token. Send the URL to your user — they run <code>wireztna enroll "URL"</code>.</p>
          </div>
        </a>
      </div>
    </div>
  {/if}

  <!-- Network Topology Summary -->
  <div class="topology-summary">
    <span>{stats.publishersOnline} publisher{stats.publishersOnline !== 1 ? 's' : ''} online</span>
    <span class="sep">·</span>
    <span>{stats.clientsConnected} client{stats.clientsConnected !== 1 ? 's' : ''} connected</span>
    <span class="sep">·</span>
    <span>{stats.groups} group{stats.groups !== 1 ? 's' : ''} configured</span>
  </div>

  <!-- Stats Grid -->
  <div class="stats-grid">
    <a href="/publishers" class="stat-card clickable">
      <span class="stat-value">{Math.round($animPublishersOnline)}/{Math.round($animPublishers)}</span>
      <span class="stat-label">Publishers Online</span>
    </a>
    <a href="/users" class="stat-card highlight-green clickable">
      <span class="stat-value">{Math.round($animClients)}</span>
      <span class="stat-label">Clients Connected</span>
    </a>
    <a href="/users" class="stat-card clickable">
      <span class="stat-value">{Math.round($animUsers)}</span>
      <span class="stat-label">Users</span>
    </a>
    <a href="/groups" class="stat-card clickable">
      <span class="stat-value">{Math.round($animGroups)}</span>
      <span class="stat-label">Groups</span>
    </a>
  </div>

  <!-- Publishers Health -->
  <h2>Publishers Health</h2>
  {#if publisherList.length > 0}
    <div class="publisher-grid">
      {#each publisherList as pub}
        <a href="/publishers?selected={pub.id}" class="publisher-card {pub.status === 'online' ? 'pub-online' : 'pub-offline'}">
          <div class="pub-header">
            <span class="pub-status-dot {pub.status === 'online' ? 'dot-green' : 'dot-red'}"></span>
            <span class="pub-name">{pub.name}</span>
            <span class="badge {pub.status === 'online' ? 'badge-green' : 'badge-red'}">{pub.status || 'unknown'}</span>
          </div>
          <div class="pub-details">
            {#if handshakeMap[pub.id] !== undefined}
              {@const age = handshakeMap[pub.id]}
              <div class="pub-detail">
                <span class="detail-label">Last handshake:</span>
                {#if age !== null}
                  <span class={age > 150 ? 'last-seen-stale' : ''}>{age}s ago</span>
                  {#if age > 150}
                    <span class="stale-warning" title="WG handshake stale — tunnel may be down">⚠</span>
                  {/if}
                {:else}
                  <span class="text-muted">no handshake</span>
                {/if}
              </div>
            {:else if pub.last_heartbeat}
              <div class="pub-detail"><span class="detail-label">Last handshake:</span> {relativeTime(pub.last_heartbeat)}</div>
            {:else}
              <div class="pub-detail"><span class="detail-label">Last handshake:</span> <span class="text-muted">never</span></div>
            {/if}
            {#if pub.allowed_cidrs && pub.allowed_cidrs.length > 0}
              <div class="pub-detail"><span class="detail-label">CIDRs:</span> <code>{pub.allowed_cidrs.join(', ')}</code></div>
            {/if}
          </div>
        </a>
      {/each}
    </div>
  {:else}
    <div class="empty-state">
      <Plug size={44} strokeWidth={1.2} style="color: var(--text-muted); opacity: 0.6;" />
      <h3>No publishers registered</h3>
      <p>Publishers connect your private networks to the broker. Add one to start exposing resources.</p>
      <a href="/publishers" class="btn-primary">Go to Publishers</a>
    </div>
  {/if}

  <!-- Connected Clients -->
  <h2>Connected Clients</h2>
  {#if connectedClients.length > 0}
    <table class="data-table">
      <thead>
        <tr>
          <th>User</th>
          <th>Overlay IP</th>
          <th>Last Handshake</th>
          <th>Session Expiry</th>
          <th>RX</th>
          <th>TX</th>
          <th>Status</th>
        </tr>
      </thead>
      <tbody>
        {#each connectedClients as client}
          <tr class="clickable-row" on:click={() => goto(`/users?selected=${client.user_id}`)}>
            <td>{client.username || resolveUser(client.user_id)}</td>
            <td class="ip-cell">{client.overlay_ip || client.allowed_ips || '—'}</td>
            <td class="time-cell">
              {#if client.last_handshake_at}
                {relativeTime(client.last_handshake_at)}
              {:else}
                <span class="text-muted">—</span>
              {/if}
            </td>
            <td class="time-cell">
              {#if client.session_expires_at}
                {relativeTime(client.session_expires_at)}
              {:else}
                <span class="text-muted">—</span>
              {/if}
            </td>
            <td class="mono-cell">{formatBytes(client.rx_bytes || 0)}</td>
            <td class="mono-cell">{formatBytes(client.tx_bytes || 0)}</td>
            <td>
              <span class="badge {client.is_connected ? 'badge-green' : 'badge-red'}">
                {client.is_connected ? 'connected' : 'disconnected'}
              </span>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <div class="empty-state">
      <User size={44} strokeWidth={1.2} style="color: var(--text-muted); opacity: 0.6;" />
      <h3>No clients connected</h3>
      <p>Clients will appear here when they establish a VPN tunnel to the broker.</p>
    </div>
  {/if}

  <!-- Recent Activity -->
  <h2>Recent Activity</h2>
  {#if recentLogs.length > 0}
    <table class="data-table">
      <thead>
        <tr><th>Time</th><th>User</th><th>Action</th><th>Detail</th><th>Source IP</th></tr>
      </thead>
      <tbody>
        {#each recentLogs as log}
          <tr>
            <td class="time-cell" title={new Date(log.timestamp).toLocaleString()}>
              {smartTime(log.timestamp)}
            </td>
            <td>{resolveUser(log.user_id)}</td>
            <td><span class="badge {actionColor(log.action)}">{log.action}</span></td>
            <td class="detail-cell" title={log.detail || ''}>{humanDetail(log.action, log.detail)}</td>
            <td class="ip-cell">{log.client_ip || '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <div class="empty-activity">
      <p>No activity recorded yet.</p>
      <p class="hint">Events will appear here as users renew sessions, publishers go online/offline, and access is evaluated.</p>
    </div>
  {/if}
  </div>
{/if}

<style>
  h1 { margin: 0; color: var(--text-primary); }

  /* Skeleton loading */
  .loading-placeholder {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
    padding-top: 0.5rem;
  }

  .skeleton-bar {
    height: 14px;
    background: var(--bg-elevated);
    border-radius: 6px;
    width: 60%;
    animation: shimmer 1.5s ease-in-out infinite;
  }

  .skeleton-bar.wide { width: 85%; height: 40px; border-radius: 8px; }
  .skeleton-bar.narrow { width: 35%; }

  .skeleton-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 1rem;
  }

  .skeleton-card {
    height: 80px;
    background: var(--bg-elevated);
    border-radius: var(--radius-md, 8px);
    animation: shimmer 1.5s ease-in-out infinite;
  }

  .skeleton-card:nth-child(2) { animation-delay: 0.1s; }
  .skeleton-card:nth-child(3) { animation-delay: 0.2s; }
  .skeleton-card:nth-child(4) { animation-delay: 0.3s; }

  @keyframes shimmer {
    0%, 100% { opacity: 0.4; }
    50% { opacity: 0.7; }
  }

  /* Getting Started Wizard */
  .getting-started-card {
    background: var(--bg-surface);
    border: 1px solid var(--accent-blue);
    border-radius: var(--radius-lg, 12px);
    padding: 1.25rem 1.5rem;
    margin-bottom: 1.5rem;
    box-shadow: 0 0 0 1px var(--accent-blue-dim), var(--shadow-sm);
  }

  .gs-header {
    margin-bottom: 1rem;
  }

  .gs-header h3 {
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 0.2rem;
  }

  .gs-subtitle {
    font-size: 0.8rem;
    color: var(--text-muted);
  }

  .gs-steps {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.75rem;
  }

  .gs-step {
    display: flex;
    gap: 0.75rem;
    padding: 0.75rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md, 8px);
    text-decoration: none;
    transition: all 0.15s ease;
  }

  .gs-step:hover {
    border-color: var(--border-default);
    background: var(--bg-hover);
  }

  .gs-step.done {
    border-color: var(--accent-green);
    background: var(--accent-green-dim);
  }

  .gs-step-num {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    min-width: 28px;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: 50%;
    font-size: 0.78rem;
    font-weight: 700;
    color: var(--text-secondary);
  }

  .gs-step.done .gs-step-num {
    background: var(--accent-green-dim);
    border-color: var(--accent-green);
    color: var(--accent-green);
  }

  .gs-step-content {
    flex: 1;
  }

  .gs-step-content strong {
    display: block;
    font-size: 0.82rem;
    color: var(--text-primary);
    margin-bottom: 0.15rem;
  }

  .gs-step-content p {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin: 0;
    line-height: 1.4;
  }

  .gs-step-content code {
    font-size: 0.7rem;
    background: var(--bg-surface);
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
  }

  .stat-card {
    background: var(--bg-surface);
    border: 1px solid color-mix(in srgb, var(--border-subtle) 50%, transparent);
    padding: 1.5rem;
    border-radius: var(--radius-md);
    text-align: center;
    box-shadow: var(--shadow-sm);
    transition: all 0.15s ease;
  }

  .stat-card:hover {
    border-color: var(--border-default);
    background: var(--bg-elevated);
    box-shadow: var(--shadow-md);
  }

  .stat-card.clickable { text-decoration: none; cursor: pointer; display: block; }

  .stat-card.highlight-green { border-left: 3px solid var(--accent-green); }
  .stat-value { display: block; font-size: 1.875rem; font-weight: 700; color: var(--text-primary); letter-spacing: -0.02em; }
  .stat-label { display: block; color: var(--text-muted); font-size: 0.75rem; margin-top: 0.375rem; font-weight: 500; }
  .health-banner { display: flex; align-items: center; gap: 0.6rem; padding: 0.65rem 1.1rem; border-radius: var(--radius-md); margin-bottom: 1rem; font-size: 0.85rem; }
  .health-banner.health-healthy { background: rgba(52, 211, 153, 0.08); border: 1px solid rgba(52, 211, 153, 0.2); }
  .health-banner.health-degraded { background: rgba(251, 191, 36, 0.08); border: 1px solid rgba(251, 191, 36, 0.2); }
  .health-banner.health-unhealthy { background: rgba(248, 113, 113, 0.08); border: 1px solid rgba(248, 113, 113, 0.2); }
  .health-dot { width: 8px; height: 8px; border-radius: 50%; }
  .health-healthy .health-dot { background: var(--accent-green); }
  .health-degraded .health-dot { background: var(--accent-orange); }
  .health-unhealthy .health-dot { background: var(--accent-red); }
  .health-label { color: var(--text-muted); font-weight: 500; }
  .health-value { color: var(--text-primary); font-weight: 700; }
  .health-issues { margin-left: auto; color: var(--text-secondary); font-size: 0.8rem; }

  /* Broker Metrics Bar */
  .broker-metrics-bar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.6rem 1rem;
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    margin-bottom: 1rem;
    flex-wrap: wrap;
    box-shadow: var(--shadow-xs);
  }

  .metric-pill {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.3rem 0.7rem;
    border-radius: 9999px;
    font-size: 0.7rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    color: var(--text-primary);
    text-decoration: none;
    transition: all 0.15s ease;
    cursor: pointer;
  }

  .metric-pill:hover {
    border-color: var(--accent-blue);
    background: var(--accent-blue-dim);
  }

  .metric-pill .metric-label {
    color: var(--text-muted);
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    font-size: 0.625rem;
  }

  .metric-pill .metric-value {
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .metric-pill.metric-warn {
    background: var(--accent-orange-dim);
    border-color: rgba(251, 191, 36, 0.3);
  }
  .metric-pill.metric-warn .metric-value { color: var(--accent-orange); }

  .metric-pill.metric-crit {
    background: var(--accent-red-dim);
    border-color: rgba(248, 113, 113, 0.3);
  }
  .metric-pill.metric-crit .metric-value { color: var(--accent-red); }

  .topology-summary {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    padding: 0.75rem 1.25rem;
    border-radius: var(--radius-md);
    margin-bottom: 1.5rem;
    font-size: 0.875rem;
    color: var(--text-primary);
    font-weight: 500;
    display: flex;
    align-items: center;
    gap: 0.25rem;
  }
  .topology-summary .sep { margin: 0 0.5rem; color: var(--text-muted); }

  .stats-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  h2 { margin: 2.5rem 0 1rem; color: var(--text-primary); font-size: 1.1rem; font-weight: 600; }

  /* Publisher Health Cards */
  .publisher-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .publisher-card {
    background: var(--bg-surface);
    border: 1px solid color-mix(in srgb, var(--border-subtle) 60%, transparent);
    padding: 1rem 1.25rem;
    border-radius: var(--radius-md);
    border-left: 3px solid var(--text-muted);
    box-shadow: var(--shadow-sm);
    transition: all 0.15s ease;
    text-decoration: none;
    cursor: pointer;
    display: block;
  }

  .publisher-card:hover {
    background: var(--bg-elevated);
  }

  .publisher-card.pub-online { border-left-color: var(--accent-green); }
  .publisher-card.pub-offline { border-left-color: var(--accent-red); }

  .pub-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .pub-status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
    position: relative;
  }

  .dot-green {
    background: var(--accent-green);
    box-shadow: 0 0 6px var(--accent-green);
  }

  .dot-green::before {
    content: '';
    position: absolute;
    inset: -3px;
    border-radius: 50%;
    background: var(--accent-green);
    opacity: 0.4;
    animation: pulse 2s ease-in-out infinite;
  }

  @keyframes pulse {
    0%, 100% { transform: scale(1); opacity: 0.4; }
    50% { transform: scale(1.8); opacity: 0; }
  }

  .dot-red { background: var(--accent-red); }

  .pub-name { font-weight: 600; font-size: 0.9rem; flex: 1; color: var(--text-primary); }

  .pub-details { font-size: 0.8rem; color: var(--text-secondary); }
  .pub-detail { margin: 0.25rem 0; }
  .detail-label { color: var(--text-muted); }
  .last-seen-stale { color: var(--color-warning, #e67e22); }
  .stale-warning { margin-left: 0.3rem; color: var(--color-warning, #e67e22); font-size: 0.85rem; cursor: help; }

  .time-cell { font-size: 0.8rem; color: var(--text-secondary); white-space: nowrap; }
  .detail-cell { font-size: 0.8rem; color: var(--text-secondary); max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ip-cell { font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; color: var(--text-secondary); }
  .mono-cell { font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; color: var(--text-secondary); white-space: nowrap; }

  .text-muted { color: var(--text-muted); }

  .empty-activity {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    padding: 2rem;
    border-radius: var(--radius-md);
    text-align: center;
  }
  .empty-activity p { margin: 0.25rem 0; color: var(--text-secondary); }
  .empty-hint { color: var(--text-muted); font-size: 0.875rem; margin: 0.5rem 0 2rem; }
  .hint { color: var(--text-secondary); font-size: 0.85rem; }

  /* Clickable rows */
  .clickable-row { cursor: pointer; transition: background 0.15s; }
  .clickable-row:hover { background: var(--bg-elevated); }

  /* ─── Responsive: tablet ─── */
  @media (max-width: 1024px) {
    .stats-grid {
      grid-template-columns: repeat(2, 1fr);
    }

    .publisher-grid {
      grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
    }

    .gs-steps {
      grid-template-columns: 1fr 1fr;
    }

    .skeleton-grid {
      grid-template-columns: repeat(2, 1fr);
    }

    .broker-metrics-bar {
      gap: 0.4rem;
      padding: 0.5rem 0.75rem;
    }

    .topology-summary {
      flex-wrap: wrap;
      font-size: 0.8125rem;
    }
  }

  /* ─── Responsive: phone ─── */
  @media (max-width: 640px) {
    .stats-grid {
      grid-template-columns: repeat(2, 1fr);
      gap: 0.75rem;
    }

    .publisher-grid {
      grid-template-columns: 1fr;
    }

    .gs-steps {
      grid-template-columns: 1fr;
    }

    .skeleton-grid {
      grid-template-columns: repeat(2, 1fr);
    }

    .broker-metrics-bar {
      gap: 0.3rem;
      padding: 0.4rem 0.5rem;
    }

    .metric-pill {
      padding: 0.25rem 0.5rem;
      font-size: 0.65rem;
    }

    .stat-card {
      padding: 1rem;
    }

    .stat-value {
      font-size: 1.5rem;
    }

    h2 {
      margin-top: 2rem;
      font-size: 1rem;
    }

    .topology-summary {
      padding: 0.625rem 1rem;
      font-size: 0.75rem;
    }

    .data-table {
      display: block;
      overflow-x: auto;
      -webkit-overflow-scrolling: touch;
    }
  }
</style>
