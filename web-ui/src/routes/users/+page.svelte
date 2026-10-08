<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { authStore } from '$lib/stores/auth';
  import { orgStore } from '$lib/stores/org';
  import { users, clients, accessPasses } from '$lib/api/client';
  import type { ClientDebugResponse, ClientFlowsResponse, EnrollmentTokenResponse } from '$lib/api/client';
  import { confirmDialog } from '$lib/stores/confirm';
  import Modal from '$lib/components/Modal.svelte';
  import ActionMenu from '$lib/components/ActionMenu.svelte';
  import { toasts } from '$lib/stores/toast';
  import { User as UserIcon } from 'lucide-svelte';
  import QRCode from 'qrcode';

  let userList: any[] = [];
  let connectedClients: any[] = [];
  let showForm = false;
  let form = { email: '', password: '' };
  let loading = true;
  let searchQuery = '';

  $: filteredUsers = searchQuery
    ? userList.filter(u =>
        u.email.toLowerCase().includes(searchQuery.toLowerCase()) ||
        u.username.toLowerCase().includes(searchQuery.toLowerCase()) ||
        (u.overlay_ip || '').includes(searchQuery)
      )
    : userList;

  // The connected endpoint is global; keep organization-scoped metrics aligned
  // with the users currently visible in this workspace.
  $: scopedConnectedCount = connectedClients.filter(client =>
    client.is_connected && userList.some(user => user.id === client.user_id)
  ).length;

  // Detail panel state
  let selectedUserId: string | null = null;
  let activeTab: 'connection' | 'flows' | 'activity' | 'enrollment' | 'passes' = 'connection';
  let debugData: ClientDebugResponse | null = null;
  let flowsData: ClientFlowsResponse | null = null;
  let historyData: any[] = [];
  let detailLoading = false;
  let flowsLoading = false;
  let qrModal: { username: string; url: string } | null = null;

  // Enrollment token state
  let enrollmentTokens: EnrollmentTokenResponse[] = [];
  let enrollLoading = false;
  let generatedToken: EnrollmentTokenResponse | null = null;
  let enrollForm = { expires_in_hours: 24, note: '' };
  let copied = false;

  // Access passes state (admin view)
  let userPasses: any[] = [];
  let passesLoading = false;
  let qrDataUrl: string | null = null;

  // Password change state
  let showPasswordChange = false;
  let newPassword = '';
  let passwordChangeMsg = '';

  onMount(async () => {
    await loadAll();
    // Auto-select user if navigated from dashboard with ?selected=userId
    const selectedParam = $page.url.searchParams.get('selected');
    if (selectedParam) {
      await selectUser(selectedParam);
    }
  });

  // Reload when org selection changes
  $: $orgStore.selectedOrgId, (() => { if ($authStore.token) loadAll(); })();

  async function loadAll() {
    loading = true;
    const token = $authStore.token!;
    const [uList, cList] = await Promise.all([
      users.list(token, $orgStore.selectedOrgId || undefined),
      clients.connected(token).catch(() => []),
    ]);
    userList = uList;
    connectedClients = cList;
    loading = false;
  }

  function getClientInfo(userId: string) {
    return connectedClients.find(c => c.user_id === userId) || null;
  }

  async function createUser() {
    await users.create(form, $authStore.token!, $orgStore.selectedOrgId || undefined);
    showForm = false;
    toasts.success(`User "${form.email}" created`);
    form = { email: '', password: '' };
    await loadAll();
  }

  async function deleteUser(id: string) {
    const user = userList.find(u => u.id === id);
    confirmDialog.show({
      title: 'Delete user',
      message: `Delete ${user?.username || 'this user'}? Their VPN access will be permanently revoked. This action cannot be undone.`,
      confirmLabel: 'Delete',
      variant: 'danger',
      requireInput: 'eliminar',
      onConfirm: async () => {
        await users.delete(id, $authStore.token!);
        if (selectedUserId === id) selectedUserId = null;
        toasts.success(`User "${user?.username || id}" deleted`);
        await loadAll();
      },
    });
  }

  async function toggleStatus(user: any) {
    if (user.status === 'active') {
      await users.disable(user.id, $authStore.token!);
      toasts.warning(`${user.username} disabled`);
    } else {
      await users.enable(user.id, $authStore.token!);
      toasts.success(`${user.username} enabled`);
    }
    await loadAll();
  }

  async function unenrollUser(user: any) {
    confirmDialog.show({
      title: 'Unenroll device',
      message: `Unenroll ${user.username}? Their device will be disconnected and they must re-enroll to connect again.`,
      confirmLabel: 'Unenroll',
      variant: 'warning',
      onConfirm: async () => {
        await users.unenroll(user.id, $authStore.token!);
        await loadAll();
        if (selectedUserId === user.id) {
          debugData = null;
          detailLoading = true;
          try { debugData = await clients.debug(user.id, $authStore.token!); } catch {}
          detailLoading = false;
        }
      },
    });
  }

  let showRoleModal = false;
  let roleModalUser: any = null;
  let roleModalValue = 'user';

  function openRoleSelector(user: any) {
    roleModalUser = user;
    roleModalValue = user.role || (user.is_admin ? 'super_admin' : 'user');
    showRoleModal = true;
  }

  async function applyRoleChange() {
    if (!roleModalUser) return;
    const roleLabels: Record<string, string> = { user: 'User', org_admin: 'Org Admin', super_admin: 'Super Admin' };
    const oldRole = roleModalUser.role || 'user';
    if (roleModalValue === oldRole) { showRoleModal = false; return; }
    await users.update(roleModalUser.id, { role: roleModalValue }, $authStore.token!);
    toasts.success(`${roleModalUser.username} is now ${roleLabels[roleModalValue]}`);
    showRoleModal = false;
    roleModalUser = null;
    await loadAll();
  }

  async function toggleVpnMode(user: any) {
    const newMode = !user.vpn_mode;
    confirmDialog.show({
      title: newMode ? 'Enable VPN mode' : 'Disable VPN mode',
      message: newMode
        ? `Enable VPN mode for ${user.username}? They will be able to select exit nodes and route all traffic through a publisher.`
        : `Disable VPN mode for ${user.username}?`,
      confirmLabel: newMode ? 'Enable' : 'Disable',
      variant: 'warning',
      onConfirm: async () => {
        await users.update(user.id, { vpn_mode: newMode }, $authStore.token!);
        toasts.success(newMode ? `VPN mode enabled for ${user.username}` : `VPN mode disabled for ${user.username}`);
        await loadAll();
      },
    });
  }

  async function sendOnboarding(user: any) {
    if (!user.email) {
      toasts.error(`User has no email address — cannot send onboarding`);
      return;
    }
    confirmDialog.show({
      title: 'Send onboarding email',
      message: `Send onboarding email to ${user.email}?`,
      confirmLabel: 'Send',
      variant: 'default',
      onConfirm: async () => {
        try {
          await users.sendOnboarding(user.id, $authStore.token!);
          toasts.success(`Onboarding email sent to ${user.email}`);
        } catch (e: any) {
          toasts.error(e.message || 'Failed to send onboarding email');
        }
      },
    });
  }

  async function selectUser(userId: string) {
    if (selectedUserId === userId) { selectedUserId = null; return; }
    selectedUserId = userId;
    activeTab = 'connection';
    detailLoading = true;
    debugData = null;
    flowsData = null;
    historyData = [];
    enrollmentTokens = [];
    generatedToken = null;
    userPasses = [];
    try {
      debugData = await clients.debug(userId, $authStore.token!);
    } catch { debugData = null; }
    detailLoading = false;
  }

  async function loadFlows() {
    if (!selectedUserId) return;
    flowsLoading = true;
    try {
      flowsData = await clients.flows(selectedUserId, $authStore.token!);
    } catch { flowsData = null; }
    flowsLoading = false;
  }

  async function loadHistory() {
    if (!selectedUserId) return;
    try {
      historyData = await clients.history(selectedUserId, $authStore.token!);
    } catch { historyData = []; }
  }

  async function loadEnrollmentTokens() {
    if (!selectedUserId) return;
    enrollLoading = true;
    try {
      enrollmentTokens = await users.listEnrollmentTokens(selectedUserId, $authStore.token!);
    } catch { enrollmentTokens = []; }
    enrollLoading = false;
  }

  async function loadUserPasses() {
    if (!selectedUserId) return;
    passesLoading = true;
    try {
      userPasses = await accessPasses.listByUser(selectedUserId, $authStore.token!);
    } catch { userPasses = []; }
    passesLoading = false;
  }

  async function adminRevokePass(passId: string) {
    try {
      await accessPasses.adminRevoke(passId, $authStore.token!);
      toasts.success(`Pass ${passId} revoked`);
      await loadUserPasses();
    } catch (e: any) { toasts.error(e.message || 'Failed to revoke'); }
  }

  async function generateEnrollmentToken() {
    if (!selectedUserId) return;
    try {
      generatedToken = await users.createEnrollmentToken(
        selectedUserId,
        { expires_in_hours: enrollForm.expires_in_hours, note: enrollForm.note || undefined },
        $authStore.token!,
      );
      enrollForm = { expires_in_hours: 24, note: '' };
      copied = false;
      // Generate QR code for the enrollment URL
      if (generatedToken?.enroll_url) {
        qrDataUrl = await QRCode.toDataURL(generatedToken.enroll_url, {
          width: 200,
          margin: 2,
          color: { dark: '#0f172a', light: '#ffffff' },
        });
      }
      await loadEnrollmentTokens();
    } catch (e: any) {
      alert(`Error: ${e.message}`);
    }
  }

  async function revokeToken(tokenId: string) {
    if (!selectedUserId) return;
    if (!confirm('Revoke this token? It will no longer be usable.')) return;
    try {
      await users.revokeEnrollmentToken(selectedUserId, tokenId, $authStore.token!);
      await loadEnrollmentTokens();
    } catch (e: any) {
      alert(`Error: ${e.message}`);
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text);
    copied = true;
    setTimeout(() => { copied = false; }, 2000);
  }

  async function changePassword() {
    if (!selectedUserId || !newPassword) return;
    if (newPassword.length < 6) {
      passwordChangeMsg = 'Password must be at least 6 characters';
      return;
    }
    try {
      await users.update(selectedUserId, { password: newPassword }, $authStore.token!);
      passwordChangeMsg = 'Password updated successfully';
      newPassword = '';
      setTimeout(() => { passwordChangeMsg = ''; showPasswordChange = false; }, 2000);
    } catch (e: any) {
      passwordChangeMsg = `Error: ${e.message}`;
    }
  }

  async function switchTab(tab: 'connection' | 'flows' | 'activity' | 'enrollment' | 'passes') {
    activeTab = tab;
    if (tab === 'flows' && !flowsData) await loadFlows();
    if (tab === 'activity' && historyData.length === 0) await loadHistory();
    if (tab === 'enrollment' && enrollmentTokens.length === 0) await loadEnrollmentTokens();
    if (tab === 'passes' && userPasses.length === 0) await loadUserPasses();
  }

  async function downloadConfig(userId: string, username: string) {
    try {
      const blob = await clients.getConfig(userId, $authStore.token!);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url; a.download = `wireztna-${username}.conf`; a.click();
      URL.revokeObjectURL(url);
    } catch (e: any) { alert(`Error: ${e.message}`); }
  }

  async function showQr(userId: string, username: string) {
    try {
      const blob = await clients.getQr(userId, $authStore.token!);
      qrModal = { username, url: URL.createObjectURL(blob) };
    } catch (e: any) { alert(`Error generating QR: ${e.message}`); }
  }

  function closeQr() {
    if (qrModal) URL.revokeObjectURL(qrModal.url);
    qrModal = null;
  }

  function formatBytes(bytes: number): string {
    if (!bytes) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  }

  function formatAge(seconds: number | null): string {
    if (seconds === null) return '—';
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
    return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
  }

  function platformLabel(platform: string | null | undefined): string {
    if (!platform) return '';
    const [os, arch] = platform.split('/');
    const osNames: Record<string, string> = {
      'darwin': 'macOS',
      'linux': 'Linux',
      'windows': 'Windows',
      'android': 'Android',
    };
    const archNames: Record<string, string> = {
      'amd64': 'x64',
      'arm64': 'ARM64',
      'arm': 'ARM',
    };
    return `${osNames[os] || os} ${archNames[arch] || arch || ''}`.trim();
  }

  function relativeTime(ts: string): string {
    const diff = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  function tokenStatus(t: EnrollmentTokenResponse): string {
    if (t.used_at) return 'used';
    if (t.revoked) return 'revoked';
    if (new Date(t.expires_at) < new Date()) return 'expired';
    return 'active';
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>Users</h1>
    <p class="page-subtitle">Manage user accounts, VPN connections, and access sessions.</p>
  </div>
  <div class="page-header-actions">
    <button class="btn-primary" on:click={() => showForm = !showForm}>
      {showForm ? 'Cancel' : '+ New User'}
    </button>
  </div>
</div>

{#if !loading}
  <section class="mesh-domain-summary mesh-only mesh-users-summary" aria-labelledby="mesh-users-title">
    <div class="mesh-domain-copy">
      <span class="mesh-domain-eyebrow"><i></i> Identity trust plane</span>
      <h2 id="mesh-users-title">Every identity,<br /><em>one trust signal.</em></h2>
      <p>See account posture, live connections and session scope as one continuous identity surface.</p>
      <div class="mesh-domain-status">
        <span><i class="status-live"></i><strong>{scopedConnectedCount}</strong> connected</span>
        <span><strong>{userList.filter(u => u.status === 'active').length}</strong> active identities</span>
      </div>
    </div>
    <div class="mesh-domain-map" aria-hidden="true">
      <span class="domain-core identity-core"><i></i><strong>{userList.length}</strong><small>identities</small></span>
      <span class="domain-node node-a"><strong>{scopedConnectedCount}</strong><small>live tunnels</small></span>
      <span class="domain-node node-b"><strong>{userList.filter(u => u.vpn_mode).length}</strong><small>VPN mode</small></span>
      <span class="domain-node node-c"><strong>{userList.filter(u => u.role === 'super_admin' || u.role === 'org_admin' || u.is_admin).length}</strong><small>admins</small></span>
      <span class="domain-path path-a"></span>
      <span class="domain-path path-b"></span>
      <span class="domain-path path-c"></span>
    </div>
  </section>
{/if}

<Modal title="New User" open={showForm} on:close={() => { showForm = false; }}>
  <form on:submit|preventDefault={createUser}>
    <label>Email <input type="email" bind:value={form.email} required placeholder="john@company.com" /></label>
    <label>Password <input type="password" bind:value={form.password} required minlength="6" placeholder="Min 6 characters (temporary)" /></label>
    <p class="form-hint">The display name will be derived from the email. The user can also login via OTP code sent to this email.</p>
    <div class="modal-actions">
      <button type="button" class="btn-secondary" on:click={() => { showForm = false; }}>Cancel</button>
      <button type="submit" class="btn-primary">Create User</button>
    </div>
  </form>
</Modal>

{#if loading}
  <p>Loading...</p>
{:else if userList.length === 0}
  <div class="empty-state">
    <UserIcon size={44} strokeWidth={1.2} style="color: var(--text-muted); opacity: 0.6;" />
    <h3>No users yet</h3>
    <p>Create a user account to get started. Users connect to the VPN via the WireZTNA client.</p>
    <button class="btn-primary" on:click={() => showForm = true}>+ New User</button>
  </div>
{:else}
  <div class="table-toolbar">
    <input
      type="text"
      class="search-input"
      placeholder="Search users by name, email, or IP..."
      bind:value={searchQuery}
    />
    <span class="result-count">{filteredUsers.length} of {userList.length}</span>
  </div>
  <table class="data-table">
    <thead>
      <tr><th>User</th><th>Overlay IP</th><th>Status</th><th>Connection</th><th>Version</th><th>Session</th><th>Actions</th></tr>
    </thead>
    <tbody>
      {#each filteredUsers as user}
        {@const ci = getClientInfo(user.id)}
        <tr class:selected={selectedUserId === user.id}>
          <td>
            <button class="user-link" on:click={() => selectUser(user.id)}>
              <strong>{user.email}</strong>
            </button>
            {#if user.role === 'super_admin'}<span class="admin-badge">Super Admin</span>
            {:else if user.role === 'org_admin'}<span class="admin-badge org-admin">Org Admin</span>
            {:else if user.is_admin}<span class="admin-badge">Admin</span>{/if}
            {#if user.username && user.username !== user.email.split('@')[0]}<span class="user-email">{user.username}</span>{/if}
          </td>
          <td><code>{user.overlay_ip || 'N/A'}</code></td>
          <td>
            <button class="badge {user.status}" on:click={() => toggleStatus(user)} title="Click to toggle">
              {user.status}
            </button>
          </td>
          <td>
            {#if ci?.is_connected}
              <span class="badge online">Connected</span>
            {:else if ci}
              <span class="badge offline">Offline</span>
            {:else}
              <span class="text-muted">—</span>
            {/if}
          </td>
          <td><span class="version-tag">{ci?.client_version || '—'}{#if ci?.platform}<span class="platform-hint"> · {platformLabel(ci.platform)}</span>{/if}</span></td>
          <td>
            {#if ci?.session_expires_at}
              <span class="session-info">
                {#if ci.session_group_name}
                  <span class="group-badge" title="Session scope: {ci.session_group_name}">{ci.session_group_name}</span>
                {:else}
                  <span class="group-badge all" title="Session scope: All groups">All</span>
                {/if}
              </span>
            {:else}
              <span class="badge expired">No session</span>
            {/if}
          </td>
          <td class="actions">
            <button class="btn-action" on:click={() => selectUser(user.id)} title="View details">Details</button>
            <button class="btn-action enroll-btn" on:click={() => { selectUser(user.id).then(() => switchTab('enrollment')); }} title="Generate enrollment token">Enroll</button>
            <ActionMenu
              items={[
                { label: 'Send Onboarding Email', icon: '📧', action: 'onboarding' },
                { label: 'Change Password', icon: '🔑', action: 'password' },
                { label: `Role: ${user.role === 'super_admin' ? 'Super Admin' : user.role === 'org_admin' ? 'Org Admin' : 'User'}`, icon: '👑', action: 'toggle-admin' },
                { label: user.vpn_mode ? 'Disable VPN Mode' : 'Enable VPN Mode', icon: '🌐', action: 'toggle-vpn' },
                { label: 'Unenroll Device', icon: '🔓', action: 'unenroll' },
                { label: 'Delete User', icon: '🗑', action: 'delete', variant: 'danger' },
              ]}
              on:action={(e) => {
                const act = e.detail.action;
                if (act === 'onboarding') sendOnboarding(user);
                else if (act === 'password') { selectedUserId = user.id; showPasswordChange = true; }
                else if (act === 'toggle-admin') openRoleSelector(user);
                else if (act === 'toggle-vpn') toggleVpnMode(user);
                else if (act === 'unenroll') unenrollUser(user);
                else if (act === 'delete') deleteUser(user.id);
              }}
            />
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}

<!-- Detail Panel -->
{#if showPasswordChange && selectedUserId}
  {@const targetUser = userList.find(u => u.id === selectedUserId)}
  <div class="password-change-card">
    <h4>Change Password {#if targetUser}— {targetUser.username}{/if}</h4>
    <form on:submit|preventDefault={changePassword}>
      <input type="password" bind:value={newPassword} placeholder="New password (min 6 characters)" minlength="6" required />
      <button type="submit" class="btn-primary">Update Password</button>
      <button type="button" class="btn-action" on:click={() => { showPasswordChange = false; newPassword = ''; passwordChangeMsg = ''; }}>Cancel</button>
    </form>
    {#if passwordChangeMsg}
      <p class="pw-msg" class:success={passwordChangeMsg.includes('success')}>{passwordChangeMsg}</p>
    {/if}
  </div>
{/if}

{#if selectedUserId}
  <div class="detail-panel">
    <div class="detail-tabs">
      <button class:active={activeTab === 'connection'} on:click={() => switchTab('connection')}>Connection</button>
      <button class:active={activeTab === 'flows'} on:click={() => switchTab('flows')}>Flows</button>
      <button class:active={activeTab === 'activity'} on:click={() => switchTab('activity')}>Activity</button>
      <button class:active={activeTab === 'enrollment'} on:click={() => switchTab('enrollment')}>Enrollment</button>
      <button class:active={activeTab === 'passes'} on:click={() => switchTab('passes')}>Passes</button>
      <button class="btn-close-panel" on:click={() => selectedUserId = null}>✕</button>
    </div>

    {#if detailLoading}
      <div class="detail-content"><p>Loading...</p></div>
    {:else if activeTab === 'connection'}
      <div class="detail-content">
        <!-- User Info (editable — always shown) -->
        {#each userList.filter(u => u.id === selectedUserId) as selectedUser (selectedUser.id)}
          <div class="user-info-edit">
            <div class="info-field">
              <span class="label">Email</span>
              <div class="inline-edit">
                <input
                  type="email"
                  value={selectedUser.email || ''}
                  placeholder="user@company.com"
                  on:blur={async (e) => {
                    const newEmail = e.currentTarget.value.trim();
                    if (newEmail !== (selectedUser.email || '')) {
                      try {
                        await users.update(selectedUser.id, { email: newEmail || null }, $authStore.token);
                        toasts.success('Email updated');
                        await loadAll();
                      } catch (err) { toasts.error('Failed to update email'); }
                    }
                  }}
                  on:keydown={(e) => { if (e.key === 'Enter') e.currentTarget.blur(); }}
                />
              </div>
            </div>
            <div class="info-field">
              <span class="label">Tokens</span>
              <button class="btn-reset-tokens" on:click={async () => {
                if (!confirm(`Reset enrollment tokens for ${selectedUser.username}? All previous tokens will be deleted and they can generate ${selectedUser.max_enrollment_tokens ?? 3} new ones.`)) return;
                try {
                  await users.resetTokens(selectedUser.id, $authStore.token);
                  toasts.success(`Tokens reset for ${selectedUser.username}`);
                } catch (err) { toasts.error('Failed to reset tokens'); }
              }}>Reset Tokens</button>
            </div>
          </div>
        {/each}

        {#if debugData}

        <!-- Session Scope -->
        <div class="scope-banner">
          <span class="scope-label">Session scope:</span>
          {#if debugData.active_session?.selected_group_name}
            <span class="scope-value group">{debugData.active_session.selected_group_name}</span>
          {:else if debugData.active_session}
            <span class="scope-value all">All groups (unrestricted)</span>
          {:else}
            <span class="scope-value none">No active session</span>
          {/if}
          {#if debugData.active_session}
            <span class="tunnel-mode-badge {debugData.active_session.tunnel_mode}">
              {#if debugData.active_session.tunnel_mode === 'vpn'}
                🌐 VPN: {debugData.active_session.exit_node_publisher_name || 'Exit Node'}
              {:else}
                🔀 Split Tunnel
              {/if}
            </span>
          {/if}
        </div>

        <!-- Issues -->
        {#if debugData.issues.length > 0}
          <div class="issues-section">
            <h4>Issues Detected</h4>
            <ul>{#each debugData.issues as issue}<li>{issue}</li>{/each}</ul>
          </div>
        {/if}

        <!-- Tunnel State -->
        <div class="debug-section">
          <h4>Tunnel State</h4>
          <div class="debug-grid">
            <div class="field"><span class="label">Status</span><span class="value">{#if debugData.is_connected}<span class="badge online">Connected</span>{:else}<span class="badge offline">Offline</span>{/if}</span></div>
            <div class="field"><span class="label">Overlay IP</span><span class="value"><code>{debugData.overlay_ip || '—'}</code></span></div>
            <div class="field"><span class="label">Endpoint</span><span class="value">{debugData.endpoint_ip ? `${debugData.endpoint_ip}:${debugData.endpoint_port}` : '—'}</span></div>
            <div class="field"><span class="label">Handshake Age</span><span class="value" class:warning={debugData.handshake_age_seconds !== null && debugData.handshake_age_seconds > 180}>{formatAge(debugData.handshake_age_seconds)}</span></div>
            <div class="field"><span class="label">Traffic</span><span class="value">↓{formatBytes(debugData.rx_bytes)} ↑{formatBytes(debugData.tx_bytes)}</span></div>
            <div class="field"><span class="label">Last Report</span><span class="value">{debugData.last_reported_at ? relativeTime(debugData.last_reported_at) : '—'}</span></div>
          </div>
        </div>

        <!-- Session -->
        {#if debugData.active_session}
          <div class="debug-section">
            <h4>Session (PSK)</h4>
            <div class="debug-grid">
              <div class="field"><span class="label">Session ID</span><span class="value"><code>{debugData.active_session.session_id.slice(0, 8)}...</code></span></div>
              <div class="field"><span class="label">Expires</span><span class="value">{new Date(debugData.active_session.expires_at).toLocaleString()}</span></div>
              <div class="field"><span class="label">TTL</span><span class="value" class:warning={debugData.active_session.ttl_remaining_seconds < 300}>{formatAge(debugData.active_session.ttl_remaining_seconds)}</span></div>
              <div class="field"><span class="label">Client IP</span><span class="value">{debugData.active_session.client_ip || '—'}</span></div>
            </div>
          </div>
        {/if}

        <!-- Accessible Publishers -->
        <div class="debug-section">
          <h4>Accessible Publishers ({debugData.accessible_publishers.length})</h4>
          {#if debugData.accessible_publishers.length > 0}
            <div class="pub-list">
              {#each debugData.accessible_publishers as pub}
                <div class="pub-item" class:restricted={pub.access_policy === 'restricted'}>
                  <span class="pub-dot {pub.status === 'online' ? 'dot-green' : 'dot-red'}"></span>
                  <span class="pub-name">{pub.publisher_name}</span>
                  <span class="badge {pub.status}">{pub.status}</span>
                  {#if pub.access_policy === 'restricted'}
                    <span class="policy-tag restricted">Restricted</span>
                  {:else}
                    <span class="policy-tag unrestricted">Full access</span>
                  {/if}
                  <span class="pub-groups">via {pub.via_groups.join(', ')}</span>
                </div>
                {#if pub.access_policy === 'restricted'}
                  <div class="pub-apps-detail">
                    {#if pub.allowed_apps?.length}
                      <span class="apps-label">Allowed apps:</span>
                      {#each pub.allowed_apps as app}
                        <code class="app-rule">{app.target}:{app.port}/{app.protocol}{#if app.name} <span class="app-name">({app.name})</span>{/if}</code>
                      {/each}
                      <span class="apps-note">+ DNS (53/udp) always allowed</span>
                    {:else}
                      <span class="apps-warning">No rules defined — all traffic blocked (except DNS)</span>
                    {/if}
                  </div>
                {/if}
              {/each}
            </div>
          {:else}
            <p class="text-muted">No publishers accessible.</p>
          {/if}
        </div>

        <!-- Allowed CIDRs -->
        {#if debugData.total_allowed_cidrs.length > 0}
          <div class="debug-section">
            <h4>Allowed CIDRs</h4>
            <div class="cidr-list">
              {#each debugData.total_allowed_cidrs as cidr}
                <code class="cidr-tag">{cidr}</code>
              {/each}
            </div>
          </div>
        {/if}
        {/if}
      </div>

    {:else if activeTab === 'flows'}
      <div class="detail-content">
        {#if flowsLoading}
          <p>Loading flows...</p>
        {:else if flowsData && flowsData.flows.length > 0}
          <table class="data-table compact">
            <thead><tr><th>Protocol</th><th>Destination</th><th>Port</th><th>State</th><th>Delivery</th><th>Traffic</th><th>Service</th><th>Publisher</th></tr></thead>
            <tbody>
              {#each flowsData.flows as flow}
                <tr>
                  <td>{flow.protocol.toUpperCase()}</td>
                  <td><code>{flow.dst_ip}</code></td>
                  <td>{flow.dst_port}</td>
                  <td><span class="badge {flow.state === 'ESTABLISHED' ? 'online' : flow.state === 'UNREPLIED' ? 'offline' : 'pending'}">{flow.state}</span></td>
                  <td>
                    {#if flow.reachable === true}
                      <span class="delivery-ok" title="Destination is responding">delivered</span>
                    {:else if flow.reachable === false}
                      <span class="delivery-fail" title="No response from destination">no reply</span>
                    {:else}
                      <span class="delivery-unknown" title="Cannot determine yet">—</span>
                    {/if}
                  </td>
                  <td>↓{formatBytes(flow.bytes_in)} ↑{formatBytes(flow.bytes_out)}</td>
                  <td>{flow.service || '—'}</td>
                  <td>{flow.publisher_name || '—'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
          {#if flowsData.stale}<p class="text-muted">Flow data may be stale.</p>{/if}
        {:else}
          <p class="text-muted">No active flows.</p>
        {/if}
        <button class="btn-action refresh-btn" on:click={loadFlows}>Refresh Flows</button>
      </div>

    {:else if activeTab === 'activity'}
      <div class="detail-content">
        {#if historyData.length > 0}
          <table class="data-table compact">
            <thead><tr><th>Time</th><th>Event</th><th>Detail</th><th>Source IP</th></tr></thead>
            <tbody>
              {#each historyData as entry}
                <tr>
                  <td class="time-cell" title={new Date(entry.timestamp).toLocaleString()}>{relativeTime(entry.timestamp)}</td>
                  <td><span class="badge {entry.event.includes('renew') ? 'online' : entry.event.includes('revoke') ? 'offline' : 'pending'}">{entry.event}</span></td>
                  <td>{entry.detail || '—'}</td>
                  <td class="ip-cell">{entry.client_ip || '—'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}
          <p class="text-muted">No activity recorded.</p>
        {/if}
      </div>
    {:else if activeTab === 'connection' && !debugData}
      <div class="detail-content">
        <p class="text-muted">No connection data available. User may not have a public key configured yet.</p>
      </div>

    {:else if activeTab === 'enrollment'}
      <div class="detail-content">
        <!-- Generated token display -->
        {#if generatedToken}
          <div class="token-generated">
            <h4>Enrollment Token Generated</h4>
            <p class="token-instructions">Send this command to the user, or let them scan the QR code:</p>
            {#if qrDataUrl}
              <div class="token-qr">
                <img src={qrDataUrl} alt="Enrollment QR Code" width="200" height="200" />
                <p class="token-qr-hint">Scan with WireZTNA Android app or any camera app</p>
              </div>
            {/if}
            <div class="token-url-box">
              <code>{generatedToken.enroll_url}</code>
              <button class="btn-copy" on:click={() => copyToClipboard(generatedToken?.enroll_url || '')}>
                {copied ? 'Copied!' : 'Copy'}
              </button>
            </div>
            <p class="token-hint">
              The user runs: <code>wireztna enroll "{generatedToken.enroll_url}"</code>
            </p>
            <p class="token-expiry">Expires: {new Date(generatedToken.expires_at).toLocaleString()}</p>
            <button class="btn-action" on:click={() => { generatedToken = null; qrDataUrl = null; }}>Dismiss</button>
          </div>
        {/if}

        <!-- Generate new token form -->
        <div class="enroll-section">
          <h4>Generate Enrollment Token</h4>
          <p class="text-muted">Create a one-time token for this user to self-enroll their device.</p>
          <form class="enroll-form" on:submit|preventDefault={generateEnrollmentToken}>
            <label>
              Expires in (hours)
              <input type="number" bind:value={enrollForm.expires_in_hours} min="1" max="720" />
            </label>
            <label>
              Note (optional)
              <input type="text" bind:value={enrollForm.note} placeholder="e.g. Sergio's laptop" />
            </label>
            <button type="submit" class="btn-primary">Generate Token</button>
          </form>
        </div>

        <!-- Token history -->
        <div class="enroll-section">
          <h4>Token History</h4>
          {#if enrollLoading}
            <p>Loading...</p>
          {:else if enrollmentTokens.length === 0}
            <p class="text-muted">No enrollment tokens generated yet.</p>
          {:else}
            <table class="data-table compact">
              <thead><tr><th>Created</th><th>Status</th><th>Device</th><th>Note</th><th>Used From</th><th>Actions</th></tr></thead>
              <tbody>
                {#each enrollmentTokens as t}
                  {@const status = tokenStatus(t)}
                  <tr>
                    <td class="time-cell">{relativeTime(t.created_at)}</td>
                    <td>
                      <span class="badge {status === 'active' ? 'online' : status === 'used' ? 'pending' : 'offline'}">
                        {status}
                      </span>
                    </td>
                    <td>
                      {#if t.device_name}
                        <span class="device-label">{t.device_name}</span>
                        {#if t.device_platform}<span class="platform-hint"> ({platformLabel(t.device_platform)})</span>{/if}
                      {:else}
                        <span class="text-muted">—</span>
                      {/if}
                    </td>
                    <td>{t.note || '—'}</td>
                    <td class="ip-cell">{t.used_from_ip || '—'}</td>
                    <td>
                      {#if status === 'active'}
                        <button class="btn-action" on:click={() => copyToClipboard(t.enroll_url)} title="Copy URL">Copy</button>
                        <button class="btn-danger-sm" on:click={() => revokeToken(t.id)}>Revoke</button>
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      </div>
    {:else if activeTab === 'passes'}
      <div class="detail-content">
        <h4 style="margin-bottom: 0.75rem;">Delegated Access Passes</h4>
        <p class="text-muted" style="margin-bottom: 1rem; font-size: 0.8rem;">Temporary access tokens created by this user for agents or collaborators.</p>

        {#if passesLoading}
          <p>Loading...</p>
        {:else if userPasses.length === 0}
          <p class="text-muted">No access passes created by this user.</p>
        {:else}
          <table class="data-table compact">
            <thead><tr><th>Pass</th><th>Label</th><th>Scope</th><th>Status</th><th>Traffic</th><th>Conn</th><th>Actions</th></tr></thead>
            <tbody>
              {#each userPasses as p}
                <tr>
                  <td><code style="font-size: 0.72rem;">{p.pass_id}</code></td>
                  <td>{p.label}</td>
                  <td style="font-size: 0.72rem; max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" title={p.scope_summary}>{p.scope_summary}</td>
                  <td>
                    <span class="badge {p.status === 'active' ? 'online' : p.status === 'expired' ? 'pending' : 'offline'}">
                      {p.status}
                    </span>
                  </td>
                  <td style="font-size: 0.72rem;">↑{formatBytes(p.bytes_uploaded)} ↓{formatBytes(p.bytes_downloaded)}</td>
                  <td>{p.connections_count}</td>
                  <td>
                    {#if p.status === 'active'}
                      <button class="btn-danger-sm" on:click={() => adminRevokePass(p.pass_id)}>Revoke</button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    {/if}
  </div>
{/if}

<!-- QR Modal -->
{#if qrModal}
  <div class="modal-overlay" on:click={closeQr} on:keydown={e => e.key === 'Escape' && closeQr()} role="button" tabindex="0">
    <div class="modal" on:click|stopPropagation role="dialog" aria-label="QR Code">
      <h3>WireGuard QR — {qrModal.username}</h3>
      <img src={qrModal.url} alt="WireGuard QR Code" />
      <p class="hint">Scan with the WireGuard mobile app.</p>
      <button class="btn-primary" on:click={closeQr}>Close</button>
    </div>
  </div>
{/if}

<!-- Role selector modal -->
{#if showRoleModal && roleModalUser}
  <div class="modal-backdrop" on:click={() => showRoleModal = false} role="presentation">
    <div class="modal-content role-modal" on:click|stopPropagation role="dialog">
      <h3>Change role for {roleModalUser.username}</h3>
      <div class="role-options">
        <label class="role-option" class:selected={roleModalValue === 'user'}>
          <input type="radio" bind:group={roleModalValue} value="user" />
          <div class="role-info">
            <strong>User</strong>
            <span>Standard user — can only access their portal and connect</span>
          </div>
        </label>
        <label class="role-option" class:selected={roleModalValue === 'org_admin'}>
          <input type="radio" bind:group={roleModalValue} value="org_admin" />
          <div class="role-info">
            <strong>Org Admin</strong>
            <span>Manages users, publishers, and groups within their organization</span>
          </div>
        </label>
        {#if $authStore.isSuperAdmin}
        <label class="role-option" class:selected={roleModalValue === 'super_admin'}>
          <input type="radio" bind:group={roleModalValue} value="super_admin" />
          <div class="role-info">
            <strong>Super Admin</strong>
            <span>Full access — all organizations, dashboard, debug, system config</span>
          </div>
        </label>
        {/if}
      </div>
      <div class="modal-actions">
        <button class="btn-secondary" on:click={() => showRoleModal = false}>Cancel</button>
        <button class="btn-primary" on:click={applyRoleChange}>Apply</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,0.5); backdrop-filter: blur(2px);
    display: flex; align-items: center; justify-content: center; z-index: 1000;
  }
  .modal-content {
    background: var(--bg-surface, #fff); border-radius: var(--radius-lg, 12px);
    padding: 1.5rem; box-shadow: var(--shadow-lg); width: 90%; max-width: 420px;
  }
  .modal-content h3 { margin: 0 0 0.5rem; font-size: 1.1rem; }
  .role-modal { max-width: 420px; }
  .role-options { display: flex; flex-direction: column; gap: 0.5rem; margin: 1rem 0; }
  .role-option {
    display: flex; align-items: flex-start; gap: 0.75rem;
    padding: 0.75rem; border-radius: var(--radius-md, 8px);
    border: 1px solid var(--border-subtle, #e0e0e0); cursor: pointer;
    transition: border-color 0.15s, background 0.15s;
  }
  .role-option:hover { border-color: var(--accent-primary, #7c3aed); }
  .role-option.selected { border-color: var(--accent-primary, #7c3aed); background: color-mix(in srgb, var(--accent-primary, #7c3aed) 5%, transparent); }
  .role-option input[type="radio"] { margin-top: 0.2rem; }
  .role-info { display: flex; flex-direction: column; gap: 0.15rem; }
  .role-info strong { font-size: 0.9rem; }
  .role-info span { font-size: 0.78rem; color: var(--text-muted, #888); }
  .modal-actions { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 1rem; }
  .form-hint {
    font-size: 0.8rem;
    color: var(--text-muted, #888);
    margin: 0.5rem 0 0;
    line-height: 1.4;
  }
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

  .empty-state { background: var(--bg-surface); border: 1px solid var(--border-subtle); padding: 2rem; border-radius: var(--radius-md); text-align: center; color: var(--text-secondary); }

  .user-link { background: none; border: none; cursor: pointer; font-size: inherit; padding: 0; color: var(--accent-blue); font-family: inherit; text-align: left; }
  .user-link:hover { text-decoration: underline; }
  .user-email { display: block; font-size: 0.75rem; color: var(--text-muted); }
  .admin-badge { display: inline-block; font-size: 0.65rem; padding: 0.1rem 0.35rem; border-radius: 3px; background: rgba(167, 139, 250, 0.15); color: var(--accent-purple); font-weight: 600; margin-left: 0.4rem; vertical-align: middle; }
  .admin-active { background: rgba(167, 139, 250, 0.15) !important; color: var(--accent-purple) !important; border-color: rgba(167, 139, 250, 0.3) !important; }
  .vpn-active { background: var(--accent-primary-dim) !important; color: var(--accent-primary) !important; border-color: var(--accent-primary-dim) !important; }
  .tunnel-mode-badge { display: inline-block; font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 4px; margin-left: 0.5rem; font-weight: 600; }
  .tunnel-mode-badge.vpn { background: var(--accent-primary-dim); color: var(--accent-primary); border: 1px solid var(--accent-primary-dim); }
  .tunnel-mode-badge.split { background: rgba(107, 114, 128, 0.1); color: var(--text-muted); border: 1px solid rgba(107, 114, 128, 0.2); }

  .actions { display: flex; gap: 0.3rem; }
  tr.selected { background: var(--bg-elevated); }

  /* Session group badge */
  .session-info { display: flex; align-items: center; gap: 0.4rem; }
  .group-badge { font-size: 0.75rem; padding: 0.15rem 0.5rem; border-radius: 3px; background: var(--accent-primary-dim); color: var(--accent-primary); font-weight: 500; }
  .group-badge.all { background: rgba(52, 211, 153, 0.12); color: var(--accent-green); }

  /* Detail Panel */
  .detail-panel { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); margin-top: 1.5rem; overflow: hidden; }
  .detail-tabs { display: flex; border-bottom: 1px solid var(--border-subtle); background: var(--bg-elevated); }
  .detail-tabs button { padding: 0.7rem 1.2rem; background: none; border: none; color: var(--text-secondary); font-size: 0.85rem; cursor: pointer; border-bottom: 2px solid transparent; transition: color 0.15s, border-color 0.15s; }
  .detail-tabs button.active { color: var(--accent-blue); border-bottom-color: var(--accent-blue); }
  .detail-tabs button:hover { color: var(--text-primary); }
  .btn-close-panel { margin-left: auto !important; color: var(--text-muted) !important; font-size: 1rem !important; }
  .detail-content { padding: 1.25rem; }

  /* Scope banner */
  .scope-banner { padding: 0.6rem 1rem; background: var(--bg-elevated); border-radius: var(--radius-sm); margin-bottom: 1rem; display: flex; align-items: center; gap: 0.5rem; font-size: 0.85rem; }

  /* User info editable */
  .user-info-edit { padding: 0.6rem 1rem; background: var(--bg-elevated); border-radius: var(--radius-sm); margin-bottom: 1rem; }
  .user-info-edit .info-field { display: flex; align-items: center; gap: 0.75rem; }
  .user-info-edit .info-field .label { font-size: 0.8rem; color: var(--text-muted); font-weight: 500; min-width: 50px; }
  .user-info-edit .inline-edit { flex: 1; }
  .user-info-edit .inline-edit input { width: 100%; padding: 0.4rem 0.6rem; background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.85rem; font-family: inherit; transition: border-color 0.15s; }
  .user-info-edit .inline-edit input:focus { outline: none; border-color: var(--accent-primary); box-shadow: 0 0 0 2px var(--accent-primary-dim); }
  .user-info-edit .inline-edit input::placeholder { color: var(--text-muted); }
  .btn-reset-tokens { padding: 0.3rem 0.7rem; background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); font-size: 0.75rem; color: var(--accent-blue); cursor: pointer; font-family: inherit; font-weight: 500; transition: all 0.15s; }
  .btn-reset-tokens:hover { border-color: var(--accent-primary); background: var(--accent-primary-dim); }
  .scope-label { color: var(--text-muted); font-weight: 500; }
  .scope-value.group { color: var(--accent-blue); font-weight: 600; }
  .scope-value.all { color: var(--accent-green); font-weight: 600; }
  .scope-value.none { color: var(--accent-red); font-weight: 500; }

  /* Issues */
  .issues-section { background: rgba(248, 113, 113, 0.08); border: 1px solid rgba(248, 113, 113, 0.2); border-radius: var(--radius-sm); padding: 0.75rem 1rem; margin-bottom: 1rem; }
  .issues-section h4 { margin: 0 0 0.5rem; font-size: 0.85rem; color: var(--accent-red); }
  .issues-section ul { margin: 0; padding-left: 1.2rem; font-size: 0.8rem; color: var(--text-secondary); }
  .issues-section li { margin: 0.25rem 0; }

  /* Debug sections */
  .debug-section { margin-bottom: 1.25rem; }
  .debug-section h4 { margin: 0 0 0.5rem; font-size: 0.85rem; color: var(--text-primary); font-weight: 600; }
  .debug-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 0.5rem; }
  .field { display: flex; flex-direction: column; gap: 0.15rem; padding: 0.4rem 0; }
  .field .label { font-size: 0.72rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.03em; }
  .field .value { font-size: 0.85rem; color: var(--text-primary); }
  .field .value.warning { color: var(--accent-orange); }

  /* Publisher list */
  .pub-list { display: flex; flex-direction: column; gap: 0.4rem; }
  .pub-item { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.6rem; background: var(--bg-elevated); border-radius: var(--radius-sm); font-size: 0.82rem; }
  .pub-dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }
  .dot-green { background: var(--accent-green); }
  .dot-red { background: var(--accent-red); }
  .pub-name { font-weight: 500; color: var(--text-primary); }
  .pub-groups { margin-left: auto; color: var(--text-muted); font-size: 0.75rem; }

  .policy-tag { font-size: 0.68rem; padding: 0.12rem 0.4rem; border-radius: 3px; font-weight: 500; }
  .policy-tag.restricted { background: rgba(248, 113, 113, 0.12); color: var(--accent-red); }
  .policy-tag.unrestricted { background: rgba(52, 211, 153, 0.1); color: var(--accent-green); }
  .pub-apps-detail { padding: 0.4rem 0.6rem 0.5rem 1.8rem; display: flex; flex-wrap: wrap; gap: 0.3rem; align-items: center; background: rgba(248, 113, 113, 0.04); border-left: 2px solid rgba(248, 113, 113, 0.3); margin-left: 0.5rem; margin-bottom: 0.3rem; border-radius: 0 var(--radius-sm) var(--radius-sm) 0; }
  .apps-label { font-size: 0.72rem; color: var(--text-muted); font-weight: 500; margin-right: 0.2rem; }
  .apps-note { font-size: 0.68rem; color: var(--text-muted); font-style: italic; }
  .apps-warning { font-size: 0.72rem; color: var(--accent-red); }
  .app-rule { font-size: 0.72rem; padding: 0.12rem 0.4rem; background: var(--bg-primary); border: 1px solid var(--border-subtle); border-radius: 3px; color: var(--text-secondary); }
  .app-rule .app-name { color: var(--text-muted); }
  .pub-item.restricted { border-left: 2px solid rgba(248, 113, 113, 0.4); padding-left: 0.5rem; }

  /* CIDR tags */
  .cidr-list { display: flex; flex-wrap: wrap; gap: 0.4rem; }
  .cidr-tag { padding: 0.2rem 0.5rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: 3px; font-size: 0.78rem; }

  /* Compact tables */
  .data-table.compact { font-size: 0.8rem; }
  .data-table.compact th, .data-table.compact td { padding: 0.4rem 0.6rem; }
  .refresh-btn { margin-top: 0.75rem; }

  .time-cell { font-size: 0.78rem; color: var(--text-secondary); white-space: nowrap; }
  .ip-cell { font-family: 'JetBrains Mono', monospace; font-size: 0.78rem; color: var(--text-secondary); }
  .text-muted { color: var(--text-muted); font-size: 0.85rem; }
  .hint { color: var(--text-secondary); font-size: 0.85rem; }

  /* Form */
  .form-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); padding: 1.5rem; border-radius: var(--radius-md); margin-bottom: 1.5rem; }
  .form-card form { display: flex; flex-wrap: wrap; gap: 1rem; align-items: flex-end; }
  .form-card label { display: flex; flex-direction: column; gap: 0.3rem; font-size: 0.82rem; color: var(--text-secondary); flex: 1; min-width: 180px; }
  .form-card input { padding: 0.5rem; background: var(--bg-primary); border: 1px solid var(--border-default); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.85rem; }

  /* Modal */
  .modal-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100; }
  .modal { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 2rem; text-align: center; max-width: 360px; }
  .modal h3 { margin: 0 0 1rem; color: var(--text-primary); }
  .modal img { max-width: 250px; border-radius: var(--radius-sm); margin-bottom: 1rem; }

  /* Enrollment */
  .enroll-btn { background: var(--accent-primary-dim); color: var(--accent-primary); font-weight: 500; }
  .enroll-btn:hover { background: var(--accent-primary-dim); filter: brightness(0.95); }

  .enroll-section { margin-bottom: 1.5rem; }
  .enroll-section h4 { margin: 0 0 0.4rem; font-size: 0.85rem; color: var(--text-primary); font-weight: 600; }
  .enroll-form { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: flex-end; margin-top: 0.5rem; }
  .enroll-form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--text-secondary); flex: 1; min-width: 140px; }
  .enroll-form input { padding: 0.45rem 0.6rem; background: var(--bg-primary); border: 1px solid var(--border-default); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.82rem; }

  .token-generated { background: rgba(52, 211, 153, 0.06); border: 1px solid rgba(52, 211, 153, 0.2); border-radius: var(--radius-sm); padding: 1rem 1.25rem; margin-bottom: 1.25rem; }
  .token-generated h4 { margin: 0 0 0.5rem; color: var(--accent-green); font-size: 0.9rem; }
  .token-instructions { margin: 0 0 0.5rem; font-size: 0.82rem; color: var(--text-secondary); }
  .token-qr { display: flex; flex-direction: column; align-items: center; margin: 1rem 0; }
  .token-qr img { border-radius: var(--radius-sm); border: 1px solid var(--border-default); }
  .token-qr-hint { font-size: 0.72rem; color: var(--text-muted); margin-top: 0.5rem; }
  .token-url-box { display: flex; align-items: center; gap: 0.5rem; background: var(--bg-primary); border: 1px solid var(--border-default); border-radius: var(--radius-sm); padding: 0.5rem 0.75rem; margin-bottom: 0.5rem; overflow-x: auto; }
  .token-url-box code { font-size: 0.78rem; color: var(--text-primary); white-space: nowrap; flex: 1; }
  .btn-copy { padding: 0.3rem 0.7rem; background: var(--accent-blue); color: white; border: none; border-radius: 3px; font-size: 0.75rem; cursor: pointer; white-space: nowrap; }
  .btn-copy:hover { opacity: 0.9; }
  .token-hint { font-size: 0.78rem; color: var(--text-muted); margin: 0.4rem 0; }
  .token-hint code { background: var(--bg-elevated); padding: 0.15rem 0.4rem; border-radius: 2px; }
  .token-expiry { font-size: 0.78rem; color: var(--text-muted); margin: 0.25rem 0 0.75rem; }

  .btn-danger-sm { padding: 0.2rem 0.5rem; font-size: 0.72rem; background: rgba(248, 113, 113, 0.1); color: var(--accent-red); border: 1px solid rgba(248, 113, 113, 0.3); border-radius: 3px; cursor: pointer; }
  .btn-danger-sm:hover { background: rgba(248, 113, 113, 0.2); }

  /* Flow delivery indicators */
  .delivery-ok { color: var(--accent-green); font-size: 0.78rem; font-weight: 500; }
  .delivery-fail { color: var(--accent-red); font-size: 0.78rem; font-weight: 500; }
  .delivery-unknown { color: var(--text-muted); font-size: 0.78rem; }

  /* Password change */
  .password-change-card { background: var(--bg-surface); border: 1px solid var(--accent-blue); border-radius: var(--radius-md); padding: 1.25rem; margin-top: 1rem; margin-bottom: 1rem; }
  .password-change-card h4 { margin: 0 0 0.75rem; font-size: 0.9rem; color: var(--text-primary); }
  .password-change-card form { display: flex; gap: 0.5rem; align-items: center; }
  .password-change-card input { flex: 1; max-width: 280px; padding: 0.45rem 0.6rem; font-size: 0.85rem; }
  .pw-msg { font-size: 0.8rem; margin: 0.5rem 0 0; color: var(--accent-red); }
  .pw-msg.success { color: var(--accent-green); }

  .version-tag { font-family: 'JetBrains Mono', monospace; font-size: 0.78rem; color: var(--text-secondary); }
  .platform-hint { font-family: inherit; font-size: 0.72rem; color: var(--text-tertiary, var(--text-secondary)); opacity: 0.8; }
  .device-label { font-size: 0.8rem; font-weight: 500; }
</style>
