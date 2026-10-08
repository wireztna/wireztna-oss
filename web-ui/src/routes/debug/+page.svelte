<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { authStore } from '$lib/stores/auth';

  const API_URL = '';

  let activeTab: 'firewall' | 'tunnels' | 'routing' | 'logs' | 'dns' | 'system' = 'firewall';
  let loading = false;
  let autoRefresh = false;
  let refreshInterval: ReturnType<typeof setInterval> | null = null;

  // Data
  let nftData: any = null;
  let tunnelsData: any = null;
  let routingData: any = null;
  let logsData: any = null;
  let dnsData: any = null;
  let systemData: any = null;

  // Logs filter
  let logService = 'wireztna-reconciler';
  let logLines = 50;

  // Firewall filter
  let filterClient = '';

  onMount(() => { loadTab(); });
  onDestroy(() => { if (refreshInterval) clearInterval(refreshInterval); });

  function toggleAutoRefresh() {
    autoRefresh = !autoRefresh;
    if (autoRefresh) {
      refreshInterval = setInterval(loadTab, 5000);
    } else {
      if (refreshInterval) clearInterval(refreshInterval);
      refreshInterval = null;
    }
  }

  async function fetchDebug(endpoint: string) {
    const resp = await fetch(`${API_URL}/api/v1/debug/${endpoint}`, {
      headers: { Authorization: `Bearer ${$authStore.token}` },
    });
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
  }

  async function loadTab() {
    loading = true;
    try {
      switch (activeTab) {
        case 'firewall': nftData = await fetchDebug('nftables'); break;
        case 'tunnels': tunnelsData = await fetchDebug('tunnels'); break;
        case 'routing': routingData = await fetchDebug('routing'); break;
        case 'logs': logsData = await fetchDebug(`logs?service=${logService}&lines=${logLines}`); break;
        case 'dns': dnsData = await fetchDebug('dns-proxy'); break;
        case 'system': systemData = await fetchDebug('system-metrics'); break;
      }
    } catch (e: any) {
      console.error(e);
    }
    loading = false;
  }

  function switchTab(tab: typeof activeTab) {
    activeTab = tab;
    loadTab();
  }

  function formatBytes(b: number): string {
    if (b > 1073741824) return (b / 1073741824).toFixed(1) + ' GB';
    if (b > 1048576) return (b / 1048576).toFixed(1) + ' MB';
    if (b > 1024) return (b / 1024).toFixed(1) + ' KB';
    return b + ' B';
  }

  function formatUptime(seconds: number): string {
    if (!seconds) return '—';
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    if (days > 0) return `${days}d ${hours}h`;
    const mins = Math.floor((seconds % 3600) / 60);
    return `${hours}h ${mins}m`;
  }

  function filteredMangle() {
    if (!nftData?.mangle_rules) return [];
    if (!filterClient) return nftData.mangle_rules;
    return nftData.mangle_rules.filter((r: any) => r.client_ip.includes(filterClient));
  }

  function filteredForward() {
    if (!nftData?.forward_rules) return [];
    if (!filterClient) return nftData.forward_rules;
    return nftData.forward_rules.filter((r: any) => r.client_ip.includes(filterClient) || r.raw.includes(filterClient));
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>Debug Panel</h1>
    <p class="page-subtitle">Real-time broker state, firewall rules, WireGuard peers, and service logs.</p>
  </div>
  <div class="page-header-actions">
    <button class="btn-action" class:active-refresh={autoRefresh} on:click={toggleAutoRefresh}>
      {autoRefresh ? '⏸ Auto-refresh ON' : '▶ Auto-refresh'}
    </button>
    <button class="btn-secondary" on:click={loadTab}>↻ Refresh</button>
  </div>
</div>

<div class="debug-tabs">
  <button class:active={activeTab === 'firewall'} on:click={() => switchTab('firewall')}>Firewall</button>
  <button class:active={activeTab === 'tunnels'} on:click={() => switchTab('tunnels')}>Tunnels</button>
  <button class:active={activeTab === 'routing'} on:click={() => switchTab('routing')}>Routing</button>
  <button class:active={activeTab === 'logs'} on:click={() => switchTab('logs')}>Logs</button>
  <button class:active={activeTab === 'dns'} on:click={() => switchTab('dns')}>DNS Proxy</button>
  <button class:active={activeTab === 'system'} on:click={() => switchTab('system')}>System</button>
</div>

<div class="debug-content">
  {#if loading}
    <p class="loading-msg">Loading...</p>

  {:else if activeTab === 'firewall' && nftData}
    <div class="filter-bar">
      <input type="text" bind:value={filterClient} placeholder="Filter by client IP (e.g., 10.200.1.0)" class="filter-input" />
      <span class="stats">{nftData.total_mangle} mangle · {nftData.total_forward} forward rules</span>
    </div>

    <div class="section">
      <h3>Mangle (Routing Marks)</h3>
      <div class="rules-list">
        {#each filteredMangle() as rule}
          <div class="rule-line">
            <span class="rule-client">{rule.client_ip || '—'}</span>
            <span class="rule-dst">{rule.destination}</span>
            <span class="rule-action mark">{rule.action}</span>
          </div>
        {:else}
          <p class="empty">No mangle rules{filterClient ? ` for ${filterClient}` : ''}</p>
        {/each}
      </div>
    </div>

    <div class="section">
      <h3>Forward (Firewall)</h3>
      <div class="rules-list">
        {#each filteredForward() as rule}
          <div class="rule-line">
            <span class="rule-client">{rule.client_ip || '—'}</span>
            <span class="rule-dst">{rule.destination || '—'}</span>
            <span class="rule-action" class:accept={rule.action === 'accept'} class:drop={rule.action === 'drop'}>{rule.action}</span>
          </div>
        {:else}
          <p class="empty">No forward rules{filterClient ? ` for ${filterClient}` : ''}</p>
        {/each}
      </div>
    </div>

    <details class="raw-section">
      <summary>Raw nftables output</summary>
      <pre class="raw-output">{nftData.raw}</pre>
    </details>

  {:else if activeTab === 'tunnels' && tunnelsData}
    <div class="section">
      <h3>Client Peers ({tunnelsData.total_clients})</h3>
      {#if tunnelsData.client_peers.length > 0}
        <table class="debug-table">
          <thead><tr><th>User</th><th>Overlay IP</th><th>Endpoint</th><th>Handshake</th><th>Traffic</th><th>Health</th></tr></thead>
          <tbody>
            {#each tunnelsData.client_peers as peer}
              <tr>
                <td><strong>{peer.username || 'unknown'}</strong> <code class="hash">{peer.public_key_short}</code></td>
                <td><code>{peer.overlay_ip}</code></td>
                <td class="endpoint-cell">{peer.endpoint || '—'}</td>
                <td class:stale={!peer.healthy}>{peer.handshake_age_seconds !== null ? peer.handshake_age_seconds + 's ago' : 'never'}</td>
                <td>↓{formatBytes(peer.rx_bytes)} ↑{formatBytes(peer.tx_bytes)}</td>
                <td><span class="health-dot" class:green={peer.healthy} class:red={!peer.healthy}></span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="empty">No client peers connected</p>
      {/if}
    </div>

    <div class="section">
      <h3>Publisher Tunnels ({tunnelsData.total_publishers})</h3>
      {#if tunnelsData.publisher_tunnels.length > 0}
        <div class="publisher-tunnels-grid">
          {#each tunnelsData.publisher_tunnels as tunnel}
            <div class="tunnel-card" class:tunnel-healthy={tunnel.peer?.healthy} class:tunnel-unhealthy={tunnel.peer && !tunnel.peer.healthy} class:tunnel-dead={!tunnel.peer}>
              <div class="tunnel-header">
                <div class="tunnel-name">
                  <span class="health-dot" class:green={tunnel.peer?.healthy} class:red={!tunnel.peer?.healthy}></span>
                  <strong>{tunnel.publisher_name}</strong>
                  {#if tunnel.exit_node}<span class="exit-badge">EXIT</span>{/if}
                </div>
                <span class="tunnel-status-badge" class:badge-online={tunnel.publisher_status === 'online'} class:badge-offline={tunnel.publisher_status !== 'online'}>{tunnel.publisher_status || '—'}</span>
              </div>

              <div class="tunnel-details">
                <div class="tunnel-detail-row">
                  <span class="detail-label">Namespace</span>
                  <code>{tunnel.namespace}</code>
                </div>
                <div class="tunnel-detail-row">
                  <span class="detail-label">Interface</span>
                  <code>{tunnel.wg_interface}</code> port <code>{tunnel.listen_port || '—'}</code>
                </div>
                <div class="tunnel-detail-row">
                  <span class="detail-label">Index</span>
                  <span>{tunnel.publisher_index} (fwmark={tunnel.publisher_index}, table {100 + tunnel.publisher_index})</span>
                </div>
                <div class="tunnel-detail-row">
                  <span class="detail-label">Veth</span>
                  <code>{tunnel.veth_host}</code>
                  <span class="health-dot small" class:green={tunnel.veth_active} class:red={!tunnel.veth_active}></span>
                </div>
                {#if tunnel.exposed_cidrs.length > 0}
                  <div class="tunnel-detail-row">
                    <span class="detail-label">CIDRs</span>
                    <span class="cidrs-list">{tunnel.exposed_cidrs.join(', ')}</span>
                  </div>
                {/if}
                {#if tunnel.location}
                  <div class="tunnel-detail-row">
                    <span class="detail-label">Location</span>
                    <span>{tunnel.location}</span>
                  </div>
                {/if}
                {#if tunnel.public_endpoint}
                  <div class="tunnel-detail-row">
                    <span class="detail-label">Public IP</span>
                    <code>{tunnel.public_endpoint}</code>
                  </div>
                {/if}
              </div>

              {#if tunnel.peer}
                <div class="tunnel-peer-stats">
                  <span>Handshake: <strong class:stale={!tunnel.peer.healthy}>{tunnel.peer.handshake_age_seconds !== null ? tunnel.peer.handshake_age_seconds + 's ago' : 'never'}</strong></span>
                  <span>↓{formatBytes(tunnel.peer.rx_bytes)} ↑{formatBytes(tunnel.peer.tx_bytes)}</span>
                  {#if tunnel.peer.endpoint}
                    <span class="peer-endpoint">→ {tunnel.peer.endpoint}</span>
                  {/if}
                </div>
              {:else}
                <div class="tunnel-peer-stats tunnel-no-peer">No WG peer data — interface may not exist</div>
              {/if}

              {#if tunnel.routes.length > 0}
                <details class="tunnel-routes">
                  <summary>Routes ({tunnel.routes.length})</summary>
                  <div class="routes-list">
                    {#each tunnel.routes as route}
                      <code class="route-line">{route}</code>
                    {/each}
                  </div>
                </details>
              {/if}
            </div>
          {/each}
        </div>
      {:else}
        <p class="empty">No publisher tunnels configured</p>
      {/if}
    </div>

  {:else if activeTab === 'routing' && routingData}
    <div class="section">
      <h3>Policy Routing ({routingData.total_rules} rules)</h3>
      {#each routingData.tables as table}
        <div class="routing-entry">
          <div class="routing-rule"><code>{table.rule}</code></div>
          <div class="routing-detail">
            <span class="label">Table {table.table_id}</span>
            <span class="label">Mark: {table.fwmark}</span>
            <code class="route">{table.routes}</code>
          </div>
        </div>
      {:else}
        <p class="empty">No policy routing rules</p>
      {/each}
    </div>

  {:else if activeTab === 'logs' && logsData}
    <div class="logs-controls">
      <select bind:value={logService} on:change={loadTab}>
        <option value="wireztna-reconciler">Reconciler</option>
        <option value="wireztna-health">Health Monitor</option>
        <option value="wireztna-dns">DNS Proxy</option>
        <option value="wireztna-api">API</option>
        <option value="wireztna-ui">UI</option>
        <option value="nginx">Nginx</option>
      </select>
      <select bind:value={logLines} on:change={loadTab}>
        <option value={25}>25 lines</option>
        <option value={50}>50 lines</option>
        <option value={100}>100 lines</option>
        <option value={200}>200 lines</option>
      </select>
      <span class="stats">{logsData.total} lines from {logsData.service}</span>
    </div>
    <div class="log-output">
      {#each logsData.lines as entry}
        <div class="log-line" class:log-error={entry.level === 'error'} class:log-warn={entry.level === 'warning'} class:log-debug={entry.level === 'debug'}>
          {entry.line}
        </div>
      {:else}
        <p class="empty">No logs available</p>
      {/each}
    </div>

  {:else if activeTab === 'dns' && dnsData}
    <div class="section">
      <h3>DNS Proxy Status</h3>
      <p>Socat proxies running: <strong>{dnsData.socat_processes}</strong></p>
      {#if dnsData.socat_details?.length}
        <div class="socat-list">
          {#each dnsData.socat_details as proc}
            <code class="socat-line">{proc}</code>
          {/each}
        </div>
      {/if}
    </div>
    {#if dnsData.config?.clients}
      <div class="section">
        <h3>Client DNS Mappings</h3>
        <table class="debug-table">
          <thead><tr><th>Client IP</th><th>Zones</th></tr></thead>
          <tbody>
            {#each Object.entries(dnsData.config.clients) as [ip, client]}
              <tr>
                <td><code>{ip}</code></td>
                <td>
                  {#each Object.entries(client?.zones || {}) as [zone, info]}
                    <span class="dns-zone-chip">{zone} → port {info?.proxy_port}</span>
                  {/each}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

  {:else if activeTab === 'system' && systemData}
    <div class="system-grid">
      <!-- CPU -->
      <div class="system-card">
        <div class="system-card-header">CPU</div>
        <div class="gauge-bar">
          <div class="gauge-fill" class:gauge-warn={systemData.cpu.usage_percent > 70} class:gauge-crit={systemData.cpu.usage_percent > 90} style="width: {Math.min(systemData.cpu.usage_percent, 100)}%"></div>
        </div>
        <div class="gauge-stats">
          <span class="gauge-value">{systemData.cpu.usage_percent}%</span>
          <span class="gauge-detail">{systemData.cpu.cores} cores · load {systemData.cpu.load_1}/{systemData.cpu.load_5}/{systemData.cpu.load_15}</span>
        </div>
      </div>

      <!-- Memory -->
      <div class="system-card">
        <div class="system-card-header">Memory</div>
        <div class="gauge-bar">
          <div class="gauge-fill" class:gauge-warn={systemData.memory.usage_percent > 70} class:gauge-crit={systemData.memory.usage_percent > 90} style="width: {systemData.memory.usage_percent}%"></div>
        </div>
        <div class="gauge-stats">
          <span class="gauge-value">{systemData.memory.usage_percent}%</span>
          <span class="gauge-detail">{systemData.memory.used_mb} MB / {systemData.memory.total_mb} MB · {systemData.memory.available_mb} MB free</span>
        </div>
      </div>

      <!-- Disk -->
      <div class="system-card">
        <div class="system-card-header">Disk</div>
        <div class="gauge-bar">
          <div class="gauge-fill" class:gauge-warn={systemData.disk.root_usage_percent > 70} class:gauge-crit={systemData.disk.root_usage_percent > 90} style="width: {systemData.disk.root_usage_percent}%"></div>
        </div>
        <div class="gauge-stats">
          <span class="gauge-value">{systemData.disk.root_usage_percent}%</span>
          <span class="gauge-detail">{systemData.disk.root_used_gb} GB / {systemData.disk.root_total_gb} GB · DB: {systemData.disk.db_size_mb} MB</span>
        </div>
      </div>

      <!-- Conntrack -->
      <div class="system-card">
        <div class="system-card-header">Conntrack</div>
        <div class="gauge-bar">
          <div class="gauge-fill" class:gauge-warn={systemData.conntrack.usage_percent > 50} class:gauge-crit={systemData.conntrack.usage_percent > 80} style="width: {Math.min(systemData.conntrack.usage_percent, 100)}%"></div>
        </div>
        <div class="gauge-stats">
          <span class="gauge-value">{systemData.conntrack.current.toLocaleString()}</span>
          <span class="gauge-detail">of {systemData.conntrack.max.toLocaleString()} max ({systemData.conntrack.usage_percent}%)</span>
        </div>
      </div>
    </div>

    <!-- WireGuard & Services -->
    <div class="system-grid system-grid-2">
      <div class="system-card">
        <div class="system-card-header">WireGuard</div>
        <div class="system-stats-list">
          <div class="system-stat-row"><span>Client peers</span><strong>{systemData.wireguard.client_peers}</strong></div>
          <div class="system-stat-row"><span>Publisher namespaces</span><strong>{systemData.wireguard.publisher_namespaces}</strong></div>
          <div class="system-stat-row"><span>Total RX</span><strong>{formatBytes(systemData.wireguard.wg_clients_rx_bytes)}</strong></div>
          <div class="system-stat-row"><span>Total TX</span><strong>{formatBytes(systemData.wireguard.wg_clients_tx_bytes)}</strong></div>
        </div>
      </div>

      <div class="system-card">
        <div class="system-card-header">Services</div>
        <div class="services-grid">
          {#each Object.entries(systemData.services) as [name, status]}
            <div class="service-item">
              <span class="service-dot" class:dot-green={status === 'active'} class:dot-red={status !== 'active'}></span>
              <span class="service-name">{name.replace('wireztna-', '')}</span>
              <span class="service-status" class:status-ok={status === 'active'} class:status-bad={status !== 'active'}>{status}</span>
            </div>
          {/each}
        </div>
      </div>
    </div>

    <!-- Capacity -->
    <div class="system-card capacity-card">
      <div class="system-card-header">Capacity Assessment</div>
      <div class="capacity-bar-container">
        <div class="capacity-bar">
          <div class="capacity-fill" style="width: {Math.min((systemData.capacity.current_users / systemData.capacity.estimated_max_users) * 100, 100)}%"></div>
        </div>
        <div class="capacity-label">{systemData.capacity.current_users} / {systemData.capacity.estimated_max_users} estimated max peers</div>
      </div>
      <div class="capacity-meta">
        <span>Headroom: <strong>{systemData.capacity.headroom_percent}%</strong></span>
        <span>Hostname: <strong>{systemData.host.hostname}</strong></span>
        <span>Kernel: <strong>{systemData.host.kernel}</strong></span>
        <span>Uptime: <strong>{formatUptime(systemData.host.uptime_seconds)}</strong></span>
      </div>
      {#if systemData.capacity.warnings.length > 0}
        <div class="capacity-warnings">
          {#each systemData.capacity.warnings as warning}
            <div class="capacity-warning">⚠ {warning}</div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .active-refresh { background: var(--accent-green-dim) !important; color: var(--accent-green) !important; border-color: var(--accent-green) !important; }

  .debug-tabs { display: flex; gap: 0; border-bottom: 1px solid var(--border-subtle); margin-bottom: 1.25rem; overflow-x: auto; }
  .debug-tabs button { padding: 0.65rem 1rem; background: none; border: none; color: var(--text-secondary); font-size: 0.82rem; cursor: pointer; border-bottom: 2px solid transparent; transition: all 0.15s; white-space: nowrap; }
  .debug-tabs button.active { color: var(--accent-blue); border-bottom-color: var(--accent-blue); font-weight: 600; }
  .debug-tabs button:hover { color: var(--text-primary); }

  .debug-content { min-height: 400px; }
  .loading-msg { color: var(--text-muted); }
  .empty { color: var(--text-muted); font-size: 0.85rem; font-style: italic; }

  .section { margin-bottom: 1.5rem; }
  .section h3 { margin: 0 0 0.6rem; font-size: 0.88rem; color: var(--text-primary); font-weight: 600; }

  /* Filter bar */
  .filter-bar { display: flex; align-items: center; gap: 1rem; margin-bottom: 1rem; }
  .filter-input { width: 220px; padding: 0.4rem 0.6rem; font-size: 0.82rem; }
  .stats { font-size: 0.78rem; color: var(--text-muted); }

  /* Rules list */
  .rules-list { display: flex; flex-direction: column; gap: 2px; max-height: 300px; overflow-y: auto; }
  .rule-line { display: grid; grid-template-columns: 110px 1fr 100px; gap: 0.5rem; padding: 0.3rem 0.6rem; background: var(--bg-elevated); border-radius: 3px; font-size: 0.78rem; font-family: 'JetBrains Mono', monospace; align-items: center; }
  .rule-client { color: var(--accent-blue); }
  .rule-dst { color: var(--text-primary); }
  .rule-action { padding: 0.1rem 0.4rem; border-radius: 3px; font-size: 0.72rem; text-align: center; }
  .rule-action.mark { background: var(--accent-orange-dim); color: var(--accent-orange); }
  .rule-action.accept { background: var(--accent-green-dim); color: var(--accent-green); }
  .rule-action.drop { background: var(--accent-red-dim); color: var(--accent-red); }

  /* Raw output */
  .raw-section { margin-top: 1rem; }
  .raw-section summary { font-size: 0.8rem; color: var(--text-muted); cursor: pointer; }
  .raw-output { background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); padding: 0.75rem; font-size: 0.72rem; font-family: 'JetBrains Mono', monospace; color: var(--text-secondary); overflow-x: auto; white-space: pre; max-height: 400px; overflow-y: auto; }

  /* Debug table */
  .debug-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
  .debug-table th { text-align: left; padding: 0.4rem 0.6rem; color: var(--text-muted); font-size: 0.72rem; text-transform: uppercase; border-bottom: 1px solid var(--border-subtle); }
  .debug-table td { padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--border-subtle); }
  .debug-table code { font-size: 0.75rem; }
  td.stale { color: var(--accent-orange); }

  .health-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; }
  .health-dot.green { background: var(--accent-green); box-shadow: 0 0 4px var(--accent-green); }
  .health-dot.red { background: var(--accent-red); }
  .hash { color: var(--text-muted); font-size: 0.7rem; }

  /* Routing */
  .routing-entry { background: var(--bg-elevated); border-radius: var(--radius-sm); padding: 0.6rem 0.8rem; margin-bottom: 0.5rem; }
  .routing-rule code { font-size: 0.78rem; color: var(--accent-blue); }
  .routing-detail { display: flex; gap: 1rem; margin-top: 0.3rem; align-items: center; }
  .routing-detail .label { font-size: 0.72rem; color: var(--text-muted); }
  .routing-detail .route { font-size: 0.75rem; color: var(--accent-green); }

  /* Logs */
  .logs-controls { display: flex; gap: 0.5rem; align-items: center; margin-bottom: 0.75rem; }
  .logs-controls select { padding: 0.35rem 0.5rem; font-size: 0.82rem; }
  .log-output { background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); padding: 0.5rem; max-height: 500px; overflow-y: auto; font-family: 'JetBrains Mono', monospace; font-size: 0.72rem; }
  .log-line { padding: 0.15rem 0.4rem; border-radius: 2px; color: var(--text-secondary); white-space: pre-wrap; word-break: break-all; }
  .log-line.log-error { background: rgba(248, 113, 113, 0.08); color: var(--accent-red); }
  .log-line.log-warn { background: rgba(251, 191, 36, 0.06); color: var(--accent-orange); }
  .log-line.log-debug { color: var(--text-muted); }

  /* DNS */
  .socat-list { display: flex; flex-direction: column; gap: 0.2rem; margin-top: 0.5rem; }
  .socat-line { display: block; font-size: 0.72rem; color: var(--text-secondary); padding: 0.2rem 0.4rem; background: var(--bg-elevated); border-radius: 3px; }
  .dns-zone-chip { display: inline-block; font-size: 0.72rem; padding: 0.1rem 0.4rem; background: var(--accent-green-dim); color: var(--accent-green); border-radius: 3px; margin: 0.1rem; }

  /* System tab */
  .system-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 1rem; margin-bottom: 1rem; }
  .system-grid-2 { grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); }
  .system-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 1.25rem; box-shadow: var(--shadow-xs); }
  .system-card-header { font-size: 0.75rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); margin-bottom: 0.75rem; }

  .gauge-bar { height: 8px; background: var(--bg-elevated); border-radius: 4px; overflow: hidden; margin-bottom: 0.5rem; }
  .gauge-fill { height: 100%; background: var(--accent-green); border-radius: 4px; transition: width 0.5s ease; }
  .gauge-fill.gauge-warn { background: var(--accent-orange); }
  .gauge-fill.gauge-crit { background: var(--accent-red); }
  .gauge-stats { display: flex; justify-content: space-between; align-items: baseline; }
  .gauge-value { font-size: 1.25rem; font-weight: 700; color: var(--text-primary); }
  .gauge-detail { font-size: 0.7rem; color: var(--text-muted); }

  .system-stats-list { display: flex; flex-direction: column; gap: 0.5rem; }
  .system-stat-row { display: flex; justify-content: space-between; align-items: center; font-size: 0.8125rem; padding: 0.3rem 0; border-bottom: 1px solid var(--border-subtle); }
  .system-stat-row:last-child { border-bottom: none; }
  .system-stat-row span { color: var(--text-secondary); }
  .system-stat-row strong { color: var(--text-primary); font-weight: 600; }

  .services-grid { display: flex; flex-direction: column; gap: 0.4rem; }
  .service-item { display: flex; align-items: center; gap: 0.5rem; font-size: 0.8125rem; padding: 0.3rem 0; }
  .service-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
  .service-dot.dot-green { background: var(--accent-green); box-shadow: 0 0 4px var(--accent-green); }
  .service-dot.dot-red { background: var(--accent-red); }
  .service-name { color: var(--text-primary); font-weight: 500; flex: 1; }
  .service-status { font-size: 0.7rem; font-weight: 500; }
  .service-status.status-ok { color: var(--accent-green); }
  .service-status.status-bad { color: var(--accent-red); }

  .capacity-card { margin-top: 0; }
  .capacity-bar-container { margin-bottom: 0.75rem; }
  .capacity-bar { height: 12px; background: var(--bg-elevated); border-radius: 6px; overflow: hidden; margin-bottom: 0.4rem; }
  .capacity-fill { height: 100%; background: linear-gradient(90deg, var(--accent-green) 0%, var(--accent-blue) 100%); border-radius: 6px; transition: width 0.5s ease; }
  .capacity-label { font-size: 0.75rem; color: var(--text-secondary); text-align: center; }
  .capacity-meta { display: flex; flex-wrap: wrap; gap: 1rem; font-size: 0.75rem; color: var(--text-muted); margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px solid var(--border-subtle); }
  .capacity-meta strong { color: var(--text-primary); }
  .capacity-warnings { margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px solid var(--border-subtle); }
  .capacity-warning { font-size: 0.75rem; color: var(--accent-orange); padding: 0.25rem 0; }

  /* Tunnels tab */
  .endpoint-cell { font-size: 0.75rem; color: var(--text-muted); }
  .publisher-tunnels-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 1rem; }
  .tunnel-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 1rem; box-shadow: var(--shadow-xs); transition: border-color 0.2s; }
  .tunnel-card.tunnel-healthy { border-left: 3px solid var(--accent-green); }
  .tunnel-card.tunnel-unhealthy { border-left: 3px solid var(--accent-orange); }
  .tunnel-card.tunnel-dead { border-left: 3px solid var(--accent-red); }

  .tunnel-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem; }
  .tunnel-name { display: flex; align-items: center; gap: 0.5rem; font-size: 0.9rem; }
  .tunnel-name strong { color: var(--text-primary); }
  .exit-badge { font-size: 0.6rem; font-weight: 700; padding: 0.1rem 0.35rem; background: var(--accent-purple-dim, rgba(168,85,247,0.12)); color: var(--accent-purple, #a855f7); border-radius: 3px; text-transform: uppercase; letter-spacing: 0.05em; }
  .tunnel-status-badge { font-size: 0.7rem; font-weight: 500; padding: 0.15rem 0.4rem; border-radius: 3px; }
  .tunnel-status-badge.badge-online { background: var(--accent-green-dim); color: var(--accent-green); }
  .tunnel-status-badge.badge-offline { background: var(--accent-red-dim); color: var(--accent-red); }

  .tunnel-details { display: flex; flex-direction: column; gap: 0.3rem; margin-bottom: 0.75rem; }
  .tunnel-detail-row { display: flex; align-items: center; gap: 0.5rem; font-size: 0.78rem; }
  .tunnel-detail-row .detail-label { color: var(--text-muted); min-width: 70px; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.03em; }
  .tunnel-detail-row code { font-size: 0.73rem; color: var(--accent-blue); }
  .tunnel-detail-row .cidrs-list { color: var(--text-secondary); font-size: 0.75rem; }
  .health-dot.small { width: 6px; height: 6px; }

  .tunnel-peer-stats { display: flex; flex-wrap: wrap; gap: 0.75rem; padding: 0.5rem 0.6rem; background: var(--bg-elevated); border-radius: var(--radius-sm); font-size: 0.75rem; color: var(--text-secondary); }
  .tunnel-peer-stats strong { color: var(--text-primary); font-weight: 600; }
  .tunnel-peer-stats strong.stale { color: var(--accent-orange); }
  .tunnel-peer-stats .peer-endpoint { color: var(--text-muted); }
  .tunnel-no-peer { color: var(--accent-red); font-style: italic; }

  .tunnel-routes { margin-top: 0.5rem; }
  .tunnel-routes summary { font-size: 0.72rem; color: var(--text-muted); cursor: pointer; }
  .routes-list { display: flex; flex-direction: column; gap: 2px; margin-top: 0.3rem; }
  .route-line { display: block; font-size: 0.7rem; color: var(--text-secondary); padding: 0.15rem 0.4rem; background: var(--bg-elevated); border-radius: 2px; }
</style>
