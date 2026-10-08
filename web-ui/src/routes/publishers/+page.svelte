<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { authStore } from '$lib/stores/auth';
  import { orgStore } from '$lib/stores/org';
  import { publishers, diagnostics } from '$lib/api/client';
  import Modal from '$lib/components/Modal.svelte';
  import { toasts } from '$lib/stores/toast';
  import { confirmDialog } from '$lib/stores/confirm';
  import { Plug } from 'lucide-svelte';

  let publisherList: any[] = [];
  let loading = true;
  let handshakeMap: Record<string, number | null> = {};
  let searchQuery = '';

  $: filteredPublishers = searchQuery
    ? publisherList.filter(p =>
        p.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
        (p.location || '').toLowerCase().includes(searchQuery.toLowerCase()) ||
        (p.endpoint || '').toLowerCase().includes(searchQuery.toLowerCase()) ||
        (p.exposed_cidrs || []).some((c: string) => c.includes(searchQuery))
      )
    : publisherList;
  let showTokenForm = false;
  let tokenForm = { expires_in_hours: 24 };
  let generatedToken = '';
  // Compute the base URL for the install command — mirrors the same logic as the API client.
  // Publisher install commands use the public origin visible to the admin.
  // Browser API calls remain relative and Vite/nginx proxy /api internally.
  let installBaseUrl = window.location.origin;
  let editingId: string | null = null;
  let editCidrs = '';
  let editingDnsId: string | null = null;
  let editDnsServer = '';
  let editDnsZones = '';
  let diagnosingId: string | null = null;
  let diagResult: any = null;
  let diagLoading = false;
  let editingAppsId: string | null = null;
  let editApps: Array<{target: string, port: number, protocol: string, name: string}> = [];
  let newApp = { target: '', port: 443, protocol: 'tcp', name: '' };
  // Inline editing for name/location
  let editingNameId: string | null = null;
  let editName = '';
  let editLocation = '';
  let editDescription = '';

  onMount(async () => {
    await loadAll();
    // Auto-expand publisher if navigated with ?selected=publisherId
    const selectedParam = $page.url.searchParams.get('selected');
    if (selectedParam) {
      const pub = publisherList.find(p => p.id === selectedParam);
      if (pub) diagnosePublisher(pub);
    }
  });

  // Reload when org selection changes
  $: $orgStore.selectedOrgId, (() => { if ($authStore.token) loadAll(); })();

  async function loadAll() {
    loading = true;
    publisherList = await publishers.list($authStore.token!, $orgStore.selectedOrgId || undefined);
    handshakeMap = await publishers.handshakes($authStore.token!).catch(() => ({}));
    loading = false;
  }

  async function deletePublisher(id: string) {
    const pub = publisherList.find(p => p.id === id);
    confirmDialog.show({
      title: 'Remove publisher',
      message: `Remove ${pub?.name || 'this publisher'}? Clients will permanently lose access to its resources. This action cannot be undone.`,
      confirmLabel: 'Remove',
      variant: 'danger',
      requireInput: 'eliminar',
      onConfirm: async () => {
        await publishers.delete(id, $authStore.token!);
        toasts.success(`Publisher "${pub?.name || id}" removed`);
        await loadAll();
      },
    });
  }

  async function resetPublisher(id: string) {
    confirmDialog.show({
      title: 'Reset publisher',
      message: 'Force this publisher to reconnect? It will be briefly offline.',
      confirmLabel: 'Reset',
      variant: 'warning',
      onConfirm: async () => {
        await publishers.reset(id, $authStore.token!);
        toasts.info('Publisher reset — reconnecting...');
        await loadAll();
      },
    });
  }

  async function disablePublisher(id: string) {
    await publishers.disable(id, $authStore.token!);
    toasts.warning('Publisher disabled');
    await loadAll();
  }

  async function enablePublisher(id: string) {
    await publishers.enable(id, $authStore.token!);
    toasts.success('Publisher enabled');
    await loadAll();
  }

  async function toggleExitNode(pub: any) {
    const newValue = !pub.exit_node;
    await publishers.update(pub.id, { exit_node: newValue }, $authStore.token!);
    toasts.success(newValue ? `${pub.name} marked as VPN exit node` : `${pub.name} removed as exit node`);
    await loadAll();
  }

  async function createEnrollmentToken() {
    const result = await publishers.createToken(tokenForm, $authStore.token!);
    generatedToken = result.token;
  }

  function startEditName(pub: any) {
    editingNameId = pub.id;
    editName = pub.name || '';
    editLocation = pub.location || '';
    editDescription = pub.description || '';
  }

  async function saveEditName(pub: any) {
    await publishers.update(pub.id, { name: editName, location: editLocation || null, description: editDescription || null }, $authStore.token!);
    editingNameId = null;
    toasts.success('Publisher details updated');
    await loadAll();
  }

  function cancelEditName() {
    editingNameId = null;
  }

  function startEditCidrs(pub: any) {
    editingId = pub.id;
    editCidrs = (pub.exposed_cidrs || []).join(', ');
  }

  async function saveEditCidrs(pub: any) {
    const newCidrs = editCidrs.split(',').map(s => s.trim()).filter(Boolean);
    await publishers.update(pub.id, { exposed_cidrs: newCidrs }, $authStore.token!);
    editingId = null;
    await loadAll();
  }

  function cancelEditCidrs() {
    editingId = null;
  }

  function startEditDns(pub: any) {
    editingDnsId = pub.id;
    editDnsServer = pub.dns_server || '';
    editDnsZones = (pub.dns_zones || []).join(', ');
  }

  async function saveEditDns(pub: any) {
    const newZones = editDnsZones.split(',').map(s => s.trim()).filter(Boolean);
    await publishers.update(pub.id, { dns_server: editDnsServer || null, dns_zones: newZones.length ? newZones : null }, $authStore.token!);
    editingDnsId = null;
    await loadAll();
  }

  function cancelEditDns() {
    editingDnsId = null;
  }

  function startEditApps(pub: any) {
    editingAppsId = pub.id;
    editApps = (pub.published_apps || []).map((a: any) => ({ ...a }));
    newApp = { target: '', port: 443, protocol: 'tcp', name: '' };
  }

  function addApp() {
    if (!newApp.target) return;
    if (newApp.protocol !== 'icmp' && !newApp.port) return;
    editApps = [...editApps, { ...newApp, port: newApp.protocol === 'icmp' ? 0 : newApp.port }];
    newApp = { target: '', port: 443, protocol: 'tcp', name: '' };
  }

  function removeApp(index: number) {
    editApps = editApps.filter((_, i) => i !== index);
  }

  async function saveEditApps(pub: any) {
    const apps = editApps.map(a => ({
      target: a.target,
      port: a.port,
      protocol: a.protocol,
      name: a.name || undefined,
    }));
    await publishers.update(pub.id, { published_apps: apps }, $authStore.token!);
    editingAppsId = null;
    await loadAll();
  }

  function cancelEditApps() {
    editingAppsId = null;
  }

  function timeSince(dateStr: string | null): string {
    if (!dateStr) return 'never';
    const diff = Date.now() - new Date(dateStr).getTime();
    const mins = Math.floor(diff / 60000);
    if (mins < 1) return 'just now';
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.floor(hours / 24)}d ago`;
  }

  async function diagnosePublisher(pub: any) {
    if (diagnosingId === pub.id) {
      diagnosingId = null;
      diagResult = null;
      return;
    }
    diagnosingId = pub.id;
    diagLoading = true;
    diagResult = null;
    try {
      diagResult = await diagnostics.diagnosePublisher(pub.id, $authStore.token!);
    } catch (e: any) {
      diagResult = { overall_status: 'critical', checks: [{ name: 'API Error', status: 'fail', detail: e.message }], recommendation: 'Check API connectivity.' };
    }
    diagLoading = false;
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>Publishers</h1>
    <p class="page-subtitle">Publishers are agents deployed in your network that create WireGuard tunnels to the broker, exposing internal CIDRs to authorized groups.</p>
  </div>
  <div class="page-header-actions">
    <button class="btn-primary" on:click={() => { showTokenForm = !showTokenForm; generatedToken = ''; }}>
      {showTokenForm ? 'Cancel' : '+ Add Publisher'}
    </button>
  </div>
</div>

{#if !loading}
  <section class="mesh-domain-summary mesh-only mesh-publishers-summary" aria-labelledby="mesh-publishers-title">
    <div class="mesh-domain-copy">
      <span class="mesh-domain-eyebrow"><i></i> Private edge fabric</span>
      <h2 id="mesh-publishers-title">Your network edge,<br /><em>in motion.</em></h2>
      <p>Publishers turn private infrastructure into observable, policy-ready resources.</p>
      <div class="mesh-domain-status">
        <span><i class="status-live"></i><strong>{publisherList.filter(p => p.status === 'online').length}</strong> online</span>
        <span><strong>{publisherList.length}</strong> total nodes</span>
      </div>
    </div>
    <div class="mesh-domain-map" aria-hidden="true">
      <span class="domain-core"><i></i><strong>Broker</strong><small>secure fabric</small></span>
      <span class="domain-node node-a"><strong>{publisherList.filter(p => p.exit_node).length}</strong><small>exit nodes</small></span>
      <span class="domain-node node-b"><strong>{publisherList.reduce((sum, p) => sum + (p.exposed_cidrs || []).length, 0)}</strong><small>private routes</small></span>
      <span class="domain-node node-c"><strong>{publisherList.reduce((sum, p) => sum + (p.published_apps || []).length, 0)}</strong><small>published apps</small></span>
      <span class="domain-path path-a"></span>
      <span class="domain-path path-b"></span>
      <span class="domain-path path-c"></span>
    </div>
  </section>
{/if}

{#if showTokenForm}
  <Modal title="Add Publisher" open={showTokenForm} on:close={() => { showTokenForm = false; generatedToken = ''; }}>
    {#if generatedToken}
      <div class="token-display">
        <strong>Publisher Enrollment Token</strong>
        <p class="token-hint">SSH into the target machine and run this command (as root or with sudo):</p>
        <div class="install-command">
          <code>curl -sf {installBaseUrl}/api/v1/publishers/install.sh | sudo ENROLLMENT_TOKEN={generatedToken} bash</code>
        </div>
        <p class="token-hint" style="margin-top: 0.5rem;">The publisher will auto-register and appear in this list within seconds. You can then edit its name, CIDRs, DNS zones, and apps inline.</p>

        <div class="next-steps-hint">
          <strong>What happens next?</strong>
          <ol>
            <li>The publisher appears in the list below automatically (~15s)</li>
            <li>Edit its <strong>name</strong>, <strong>CIDRs</strong>, and <strong>DNS zones</strong> inline</li>
            <li>Go to <strong>Groups</strong> and assign this publisher to grant user access</li>
          </ol>
        </div>

        <details class="token-details">
          <summary>Advanced: manual token</summary>
          <code class="token-value">{generatedToken}</code>
        </details>
        <small>One-time use. Expires in {tokenForm.expires_in_hours}h.</small>
      </div>
    {:else}
      <form on:submit|preventDefault={createEnrollmentToken}>
        <label>Expires in (hours) <input type="number" bind:value={tokenForm.expires_in_hours} min="1" max="168" /></label>
        <div class="modal-actions">
          <button type="button" class="btn-secondary" on:click={() => { showTokenForm = false; }}>Cancel</button>
          <button type="submit" class="btn-primary">Generate Token</button>
        </div>
      </form>
    {/if}
  </Modal>
{/if}

{#if loading}
  <p>Loading...</p>
{:else}
  <div class="table-toolbar">
    <input
      type="text"
      class="search-input"
      placeholder="Search by name, location, or CIDR..."
      bind:value={searchQuery}
    />
    <span class="result-count">{filteredPublishers.length} of {publisherList.length}</span>
  </div>
  <table class="data-table">
    <thead>
      <tr><th>Publisher</th><th>Status</th><th>Endpoint</th><th>Exit Node</th><th>Exposed CIDRs</th><th>Apps</th><th>DNS</th><th>Version</th><th>Last Handshake</th><th>Actions</th></tr>
    </thead>
    <tbody>
      {#each filteredPublishers as pub}
        <tr>
          <td>
            {#if editingNameId === pub.id}
              <div class="edit-name">
                <input type="text" bind:value={editName} class="name-input" placeholder="Publisher name" />
                <input type="text" bind:value={editLocation} class="name-input" placeholder="Location" />
                <input type="text" bind:value={editDescription} class="name-input" placeholder="Description" />
                <div class="edit-name-actions">
                  <button class="btn-sm btn-save" on:click={() => saveEditName(pub)}>Save</button>
                  <button class="btn-sm btn-cancel" on:click={cancelEditName}>✕</button>
                </div>
              </div>
            {:else}
              <div class="name-cell" on:click={() => startEditName(pub)} on:keydown={e => e.key === 'Enter' && startEditName(pub)} role="button" tabindex="0" title="Click to edit name, location & description">
                <strong>{pub.name}</strong>
                {#if pub.location}
                  <small class="pub-location">{pub.location}</small>
                {/if}
                {#if pub.description}
                  <small class="pub-description">{pub.description}</small>
                {/if}
              </div>
            {/if}
          </td>
          <td>
            <span class="badge {pub.status === 'online' ? 'online' : 'offline'}">
              {pub.status}
            </span>
          </td>
          <td>
            {#if pub.endpoint}
              <span class="endpoint-ip" title={pub.endpoint}>{pub.endpoint.split(':')[0]}</span>
            {:else}
              <span class="hint">—</span>
            {/if}
          </td>
          <td>
            <label class="toggle-switch" title="Mark as VPN exit node (routes all client traffic through this publisher)">
              <input type="checkbox" checked={pub.exit_node} on:change={() => toggleExitNode(pub)} />
              <span class="toggle-slider"></span>
            </label>
          </td>
          <td>
            {#if editingId === pub.id}
              <div class="edit-cidrs">
                <input type="text" bind:value={editCidrs} class="cidr-input" />
                <button class="btn-sm btn-save" on:click={() => saveEditCidrs(pub)}>Save</button>
                <button class="btn-sm btn-cancel" on:click={cancelEditCidrs}>✕</button>
              </div>
            {:else}
              <div class="cidr-tags" on:click={() => startEditCidrs(pub)} on:keydown={e => e.key === 'Enter' && startEditCidrs(pub)} role="button" tabindex="0" title="Click to edit">
                {#each pub.exposed_cidrs || [] as cidr}
                  <span class="cidr-chip">{cidr}</span>
                {:else}
                  <em class="hint">none</em>
                {/each}
              </div>
            {/if}
          </td>
          <td>
            {#if editingAppsId === pub.id}
              <div class="edit-apps">
                {#each editApps as app, i}
                  <div class="app-row">
                    <code class="app-summary">{app.protocol}/{app.target}{app.protocol !== 'icmp' ? ':' + app.port : ''}</code>
                    {#if app.name}<small class="app-name">{app.name}</small>{/if}
                    <button class="btn-sm btn-cancel" on:click={() => removeApp(i)}>✕</button>
                  </div>
                {/each}
                <div class="app-add-row">
                  <input type="text" bind:value={newApp.target} placeholder="IP, CIDR or FQDN" class="app-input app-target" />
                  {#if newApp.protocol !== 'icmp'}
                    <input type="number" bind:value={newApp.port} min="1" max="65535" class="app-input app-port" />
                  {/if}
                  <select bind:value={newApp.protocol} class="app-input app-proto" on:change={() => { if (newApp.protocol === 'icmp') newApp.port = 0; else if (newApp.port === 0) newApp.port = 443; }}>
                    <option value="tcp">TCP</option>
                    <option value="udp">UDP</option>
                    <option value="icmp">ICMP</option>
                  </select>
                  <input type="text" bind:value={newApp.name} placeholder="label" class="app-input app-name-input" />
                  <button class="btn-sm btn-save" on:click={addApp}>+</button>
                </div>
                <div class="edit-apps-actions">
                  <button class="btn-sm btn-save" on:click={() => saveEditApps(pub)}>Save</button>
                  <button class="btn-sm btn-cancel" on:click={cancelEditApps}>Cancel</button>
                </div>
              </div>
            {:else}
              <div class="apps-cell" on:click={() => startEditApps(pub)} on:keydown={e => e.key === 'Enter' && startEditApps(pub)} role="button" tabindex="0" title="Click to manage apps">
                {#if pub.published_apps && pub.published_apps.length > 0}
                  {#each pub.published_apps as app}
                    <span class="app-chip" title={app.name || ''}>{app.protocol}/{app.target}{app.protocol !== 'icmp' ? ':' + app.port : ''}</span>
                  {/each}
                {:else}
                  <em class="hint">none</em>
                {/if}
              </div>
            {/if}
          </td>
          <td>
            {#if editingDnsId === pub.id}
              <div class="edit-dns">
                <input type="text" bind:value={editDnsServer} class="dns-input" placeholder="DNS server IP" />
                <input type="text" bind:value={editDnsZones} class="dns-input" placeholder="zone1, zone2" />
                <div class="edit-dns-actions">
                  <button class="btn-sm btn-save" on:click={() => saveEditDns(pub)}>Save</button>
                  <button class="btn-sm btn-cancel" on:click={cancelEditDns}>✕</button>
                </div>
              </div>
            {:else}
              <div class="dns-cell" on:click={() => startEditDns(pub)} role="button" tabindex="0" on:keydown={e => e.key === 'Enter' && startEditDns(pub)} title="Click to edit DNS">
                {#if pub.dns_zones && pub.dns_zones.length > 0}
                  <div class="dns-info">
                    <div class="dns-zones">
                      {#each pub.dns_zones as zone}
                        <span class="dns-chip">{zone}</span>
                      {/each}
                    </div>
                    {#if pub.dns_server}
                      <small class="dns-server">via {pub.dns_server}</small>
                    {/if}
                  </div>
                {:else}
                  <span class="hint">—</span>
                {/if}
              </div>
            {/if}
          </td>
          <td><span class="version-tag">{pub.agent_version || 'N/A'}</span></td>
          <td>
            {#if handshakeMap[pub.id] !== undefined && handshakeMap[pub.id] !== null}
              {handshakeMap[pub.id]}s ago
            {:else if handshakeMap[pub.id] === null}
              <span class="text-muted">no handshake</span>
            {:else}
              {timeSince(pub.last_heartbeat)}
            {/if}
          </td>
          <td class="actions">
            {#if pub.status === 'disabled'}
              <button class="btn-action btn-enable" on:click={() => enablePublisher(pub.id)}>Enable</button>
            {:else}
              <button class="btn-action btn-disable" on:click={() => disablePublisher(pub.id)}>Disable</button>
            {/if}
            <button class="btn-action btn-diagnose" on:click={() => diagnosePublisher(pub)} title="Run live diagnostic">🔍</button>
            <button class="btn-action btn-reset" on:click={() => resetPublisher(pub.id)} title="Force reconnection">⟳ Reset</button>
            <button class="btn-danger" on:click={() => deletePublisher(pub.id)}>Delete</button>
          </td>
        </tr>
        {#if diagnosingId === pub.id}
          <tr class="diag-row">
            <td colspan="10">
              <div class="diag-panel">
                {#if diagLoading}
                  <p class="diag-loading">Running diagnostics...</p>
                {:else if diagResult}
                  <div class="diag-header">
                    <h4>Diagnostic — {pub.name}</h4>
                    <span class="diag-badge diag-{diagResult.overall_status}">{diagResult.overall_status}</span>
                  </div>
                  <div class="diag-checks">
                    {#each diagResult.checks as check}
                      <div class="diag-check diag-check-{check.status}">
                        <span class="diag-icon">{check.status === 'pass' ? '✅' : check.status === 'warn' ? '⚠️' : '❌'}</span>
                        <span class="diag-name">{check.name}</span>
                        <span class="diag-detail">{check.detail}</span>
                        {#if check.duration_ms}
                          <span class="diag-duration">{check.duration_ms}ms</span>
                        {/if}
                      </div>
                    {/each}
                  </div>
                  {#if diagResult.recommendation}
                    <div class="diag-recommendation">
                      <strong>Recommendation:</strong> {diagResult.recommendation}
                    </div>
                  {/if}
                {/if}
              </div>
            </td>
          </tr>
        {/if}
      {:else}
        <tr><td colspan="10" class="empty-table-cell">
          <div class="empty-state">
            <Plug size={36} strokeWidth={1.2} style="color: var(--text-muted); opacity: 0.6;" />
            <h3>No publishers enrolled</h3>
            <p>Click "+ Add Publisher" to generate an enrollment token and deploy a publisher agent on your target network.</p>
          </div>
        </td></tr>
      {/each}
    </tbody>
  </table>
{/if}

<style>
  .table-toolbar {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-bottom: 0.75rem;
  }

  .search-input {
    flex: 1;
    max-width: 320px;
    padding: 0.5rem 0.75rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-size: 0.8125rem;
    font-family: inherit;
    transition: border-color 0.15s ease;
  }

  .search-input:focus {
    outline: none;
    border-color: var(--accent-primary);
    box-shadow: 0 0 0 3px var(--accent-primary-dim);
  }

  .result-count {
    font-size: 0.75rem;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .page-header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: var(--space-xl); padding-bottom: var(--space-lg); border-bottom: 1px solid var(--border-subtle); }

  .actions { display: flex; gap: 0.3rem; flex-wrap: wrap; }
  .btn-reset { background: var(--accent-orange-dim); color: var(--accent-orange); border: 1px solid rgba(251, 191, 36, 0.3); }
  .btn-reset:hover { background: rgba(251, 191, 36, 0.2); }
  .btn-disable { background: var(--bg-elevated); color: var(--text-secondary); border: 1px solid var(--border-subtle); }
  .btn-disable:hover { background: var(--bg-hover); color: var(--text-primary); }
  .btn-enable { background: var(--accent-green-dim); color: var(--accent-green); border: 1px solid rgba(52, 211, 153, 0.3); }
  .btn-enable:hover { background: rgba(52, 211, 153, 0.2); }
  .btn-sm { padding: 0.2rem 0.5rem; border-radius: var(--radius-sm); border: none; cursor: pointer; font-size: 0.8rem; }
  .btn-save { background: var(--accent-green-dim); color: var(--accent-green); }
  .btn-save:hover { background: rgba(52, 211, 153, 0.25); }
  .btn-cancel { background: var(--accent-red-dim); color: var(--accent-red); }
  .btn-cancel:hover { background: rgba(248, 113, 113, 0.25); }

  .cidr-tags { display: flex; flex-wrap: wrap; gap: 0.3rem; cursor: pointer; min-height: 1.5rem; align-items: center; padding: 0.25rem; border-radius: var(--radius-sm); transition: background var(--transition); }
  .cidr-tags:hover { background: var(--bg-hover); }
  .cidr-chip { background: var(--accent-blue-dim); color: var(--accent-blue); padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.75rem; font-family: 'JetBrains Mono', monospace; }
  .edit-cidrs { display: flex; align-items: center; gap: 0.3rem; }
  .cidr-input { padding: 0.3rem 0.5rem; border: 1px solid var(--accent-primary); border-radius: var(--radius-sm); font-size: 0.85rem; width: 200px; background: var(--bg-elevated); color: var(--text-primary); }
  .hint { color: var(--text-muted); font-size: 0.8rem; }

  .endpoint-ip { font-family: 'JetBrains Mono', monospace; font-size: 0.8rem; color: var(--text-secondary); }

  .name-cell { cursor: pointer; padding: 0.25rem; border-radius: var(--radius-sm); transition: background var(--transition); display: flex; flex-direction: column; gap: 0.15rem; }
  .name-cell:hover { background: var(--bg-hover); }
  .pub-location { color: var(--text-secondary); font-size: 0.78rem; }
  .pub-description { color: var(--text-muted); font-size: 0.75rem; font-style: italic; }
  .edit-name { display: flex; flex-direction: column; gap: 0.3rem; min-width: 180px; }
  .name-input { padding: 0.3rem 0.5rem; border: 1px solid var(--accent-purple); border-radius: var(--radius-sm); font-size: 0.85rem; width: 100%; background: var(--bg-elevated); color: var(--text-primary); }
  .edit-name-actions { display: flex; gap: 0.3rem; }

  .dns-cell { cursor: pointer; min-height: 1.5rem; padding: 0.25rem; border-radius: var(--radius-sm); transition: background var(--transition); }
  .dns-cell:hover { background: var(--bg-hover); }
  .dns-info { display: flex; flex-direction: column; gap: 0.2rem; }
  .dns-zones { display: flex; flex-wrap: wrap; gap: 0.2rem; }
  .dns-chip { background: var(--accent-green-dim); color: var(--accent-green); padding: 0.15rem 0.4rem; border-radius: 4px; font-size: 0.72rem; font-family: 'JetBrains Mono', monospace; }
  .dns-server { color: var(--text-muted); font-size: 0.75rem; }
  .edit-dns { display: flex; flex-direction: column; gap: 0.3rem; }
  .dns-input { padding: 0.3rem 0.5rem; border: 1px solid var(--accent-green); border-radius: var(--radius-sm); font-size: 0.85rem; width: 180px; background: var(--bg-elevated); color: var(--text-primary); }
  .edit-dns-actions { display: flex; gap: 0.3rem; }

  .token-display { display: flex; flex-direction: column; gap: 0.5rem; }
  .token-display strong { color: var(--text-primary); font-size: 0.9rem; }
  .token-hint { color: var(--text-secondary); font-size: 0.82rem; margin: 0; }
  .install-command { padding: 0.75rem; background: var(--bg-elevated); border-radius: var(--radius-sm); border: 1px solid var(--accent-green); overflow-x: auto; }
  .install-command code { color: var(--accent-green); font-size: 0.8rem; font-family: 'JetBrains Mono', monospace; white-space: nowrap; }
  .token-details { margin-top: 0.3rem; }
  .token-details summary { font-size: 0.78rem; color: var(--text-muted); cursor: pointer; }
  .token-value { display: block; padding: 0.5rem; background: var(--bg-elevated); color: var(--text-secondary); border-radius: var(--radius-sm); border: 1px solid var(--border-subtle); word-break: break-all; font-size: 0.78rem; font-family: 'JetBrains Mono', monospace; margin-top: 0.3rem; }

  .btn-diagnose { background: rgba(167, 139, 250, 0.15); color: var(--accent-purple); border: 1px solid rgba(167, 139, 250, 0.3); }
  .btn-diagnose:hover { background: rgba(167, 139, 250, 0.25); }
  .diag-row td { padding: 0; background: var(--bg-elevated); }
  .diag-panel { padding: 1rem 1.5rem; border-top: 2px solid var(--accent-purple); }
  .diag-loading { color: var(--text-secondary); font-size: 0.9rem; }
  .diag-header { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.75rem; }
  .diag-header h4 { margin: 0; font-size: 0.9rem; color: var(--text-primary); }
  .diag-badge { padding: 0.2rem 0.6rem; border-radius: 9999px; font-size: 0.72rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.03em; }
  .diag-healthy { background: var(--accent-green-dim); color: var(--accent-green); }
  .diag-degraded { background: var(--accent-orange-dim); color: var(--accent-orange); }
  .diag-critical { background: var(--accent-red-dim); color: var(--accent-red); }
  .diag-checks { display: flex; flex-direction: column; gap: 0.3rem; }
  .diag-check { display: grid; grid-template-columns: 24px 160px 1fr auto; align-items: center; gap: 0.5rem; padding: 0.4rem 0.6rem; border-radius: var(--radius-sm); font-size: 0.82rem; }
  .diag-check-pass { background: rgba(52, 211, 153, 0.08); }
  .diag-check-warn { background: rgba(251, 191, 36, 0.08); }
  .diag-check-fail { background: rgba(248, 113, 113, 0.08); }
  .diag-icon { font-size: 0.9rem; }
  .diag-name { font-weight: 500; color: var(--text-primary); }
  .diag-detail { color: var(--text-secondary); font-size: 0.8rem; }
  .diag-duration { color: var(--text-muted); font-size: 0.75rem; font-family: 'JetBrains Mono', monospace; }
  .diag-recommendation { margin-top: 0.75rem; padding: 0.75rem; background: var(--accent-blue-dim); border: 1px solid rgba(96, 165, 250, 0.2); border-radius: var(--radius-sm); font-size: 0.82rem; color: var(--accent-blue); }

  /* Published Apps */
  .apps-cell { cursor: pointer; display: flex; flex-wrap: wrap; gap: 0.2rem; min-height: 1.5rem; align-items: center; padding: 0.25rem; border-radius: var(--radius-sm); transition: background var(--transition); }
  .apps-cell:hover { background: var(--bg-hover); }
  .app-chip { background: var(--accent-orange-dim); color: var(--accent-orange); padding: 0.15rem 0.4rem; border-radius: 4px; font-size: 0.72rem; font-family: 'JetBrains Mono', monospace; white-space: nowrap; }
  .edit-apps { display: flex; flex-direction: column; gap: 0.4rem; min-width: 280px; }
  .app-row { display: flex; align-items: center; gap: 0.3rem; }
  .app-summary { font-size: 0.8rem; background: var(--accent-orange-dim); color: var(--accent-orange); padding: 0.1rem 0.4rem; border-radius: 3px; }
  .app-name { color: var(--text-muted); font-size: 0.72rem; }
  .app-add-row { display: flex; gap: 0.2rem; align-items: center; flex-wrap: wrap; }
  .app-input { padding: 0.25rem 0.4rem; border: 1px solid var(--accent-orange); border-radius: 3px; font-size: 0.8rem; background: var(--bg-elevated); color: var(--text-primary); }
  .app-target { width: 110px; }
  .app-port { width: 55px; }
  .app-proto { width: 55px; background: var(--bg-elevated); color: var(--text-primary); border: 1px solid var(--accent-orange); }
  .app-name-input { width: 70px; }
  .edit-apps-actions { display: flex; gap: 0.3rem; margin-top: 0.2rem; }

  .version-tag { font-family: 'JetBrains Mono', monospace; font-size: 0.78rem; color: var(--text-secondary); }

  /* Responsive */
  @media (max-width: 1400px) {
    .page-header { flex-direction: column; gap: 1rem; }
  }
  @media (max-width: 1100px) {
    :global(.data-table) { display: block; overflow-x: auto; }
  }
  .toggle-switch {
    position: relative;
    display: inline-block;
    width: 36px;
    height: 20px;
    cursor: pointer;
  }
  .toggle-switch input {
    opacity: 0;
    width: 0;
    height: 0;
  }
  .toggle-slider {
    position: absolute;
    inset: 0;
    background: var(--bg-tertiary, #374151);
    border-radius: 20px;
    transition: background 0.2s;
  }
  .toggle-slider::before {
    content: '';
    position: absolute;
    height: 14px;
    width: 14px;
    left: 3px;
    bottom: 3px;
    background: white;
    border-radius: 50%;
    transition: transform 0.2s;
  }
  .toggle-switch input:checked + .toggle-slider {
    background: var(--accent-primary);
  }
  .toggle-switch input:checked + .toggle-slider::before {
    transform: translateX(16px);
  }

  /* Next steps hint after token generation */
  .next-steps-hint {
    margin-top: 0.75rem;
    padding: 0.65rem 0.85rem;
    background: var(--accent-green-dim);
    border: 1px solid var(--accent-green);
    border-radius: var(--radius-sm, 6px);
    font-size: 0.8rem;
  }

  .next-steps-hint strong {
    color: var(--accent-green);
    font-size: 0.8rem;
    display: block;
    margin-bottom: 0.3rem;
  }

  .next-steps-hint ol {
    margin: 0;
    padding-left: 1.2rem;
    color: var(--text-secondary);
    line-height: 1.6;
  }

  .next-steps-hint li {
    margin: 0.15rem 0;
  }
</style>
