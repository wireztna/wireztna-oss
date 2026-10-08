<script lang="ts">
  import { onMount } from 'svelte';
  import { authStore } from '$lib/stores/auth';
  import { portal, announcements, accessPasses } from '$lib/api/client';
  import { toasts } from '$lib/stores/toast';
  import { Shield, Mail, Key, Monitor, Network, Bell, Share2 } from 'lucide-svelte';
  import { page } from '$app/stores';
  import QRCode from 'qrcode';

  let profile: any = null;
  let status: any = null;
  let access: any = null;
  let tokens: any[] = [];
  let activeAnnouncements: any[] = [];
  let loading = true;

  let activeTab: 'account' | 'security' | 'devices' | 'access' | 'status' | 'delegation' = 'account';

  // WebSocket base for access-pass commands — derived from the current origin
  // (ws:// over http, wss:// over https). Guarded for SSR.
  let brokerWsBase = 'wss://your-broker';
  $: if (typeof window !== 'undefined') {
    brokerWsBase = `${window.location.protocol === 'http:' ? 'ws' : 'wss'}://${window.location.host}`;
  }

  // Forced password change
  let forcePasswordChange = false;

  // Account
  let editEmail = '';
  let emailPassword = '';
  let emailSaving = false;

  // Security
  let currentPassword = '';
  let newPassword = '';
  let confirmPassword = '';
  let passwordMsg = '';
  let passwordSaving = false;

  // OTP Verification (removed — OTP is passwordless, not MFA)
  // MFA (TOTP authenticator app)
  let mfaEnabled = false;
  let mfaSetupStep: 'idle' | 'show_qr' = 'idle';
  let mfaSecret = '';
  let mfaOtpauthUri = '';
  let mfaSetupCode = '';
  let mfaDisableCode = '';
  let mfaSettingUp = false;
  let mfaConfirming = false;
  let mfaDisabling = false;
  let mfaError = '';

  // Devices
  let tokenGenerating = false;
  let newTokenUrl: string | null = null;
  let copied = false;
  let tokenQrDataUrl: string | null = null;

  // Delegation (Access Passes)
  let passes: any[] = [];
  let passesLoaded = false;
  let passCreating = false;
  let showCreatePass = false;
  let newPassLabel = '';
  let newPassPublisher = '';
  let newPassCidr = '';
  let newPassPort = '';
  let newPassTtl = 1800;
  let createdPassId: string | null = null;
  let createdPassUrl: string | null = null;
  let passCopied = false;
  let accessiblePublishers: { id: string; name: string; cidrs: string[] }[] = [];

  onMount(async () => {
    // Check if forced password change
    if ($page.url.searchParams.get('change_password') === '1') {
      forcePasswordChange = true;
      activeTab = 'security';
    }
    await loadAll();
  });

  async function loadAll() {
    loading = true;
    const token = $authStore.token!;
    try {
      const [p, s, ann] = await Promise.all([
        portal.profile(token),
        portal.status(token).catch(() => null),
        announcements.active(token).catch(() => []),
      ]);
      profile = p;
      status = s;
      activeAnnouncements = ann;
      editEmail = profile?.email || '';
      // Force password change if flag is set (from DB or URL param)
      if (profile?.must_change_password) {
        forcePasswordChange = true;
        activeTab = 'security';
      }
      // Load MFA status
      await loadMFAStatus();
    } catch { }
    loading = false;
  }

  async function loadTokens() {
    try {
      tokens = await portal.listTokens($authStore.token!);
    } catch { tokens = []; }
  }

  async function loadAccess() {
    try {
      access = await portal.access($authStore.token!);
    } catch { access = null; }
  }

  async function switchTab(tab: typeof activeTab) {
    if (forcePasswordChange && tab !== 'security') {
      toasts.warning('Please change your password first');
      return;
    }
    activeTab = tab;
    if (tab === 'devices' && tokens.length === 0) await loadTokens();
    if (tab === 'access' && !access) await loadAccess();
    if (tab === 'delegation' && !passesLoaded) await loadPasses();
  }

  async function saveEmail() {
    if (!emailPassword) { toasts.error('Enter your current password to confirm'); return; }
    emailSaving = true;
    try {
      await portal.updateEmail(editEmail, emailPassword, $authStore.token!);
      profile.email = editEmail;
      emailPassword = '';
      toasts.success('Email updated');
    } catch (e: any) { toasts.error(e.message || 'Failed to update email'); }
    emailSaving = false;
  }

  async function changePassword() {
    passwordMsg = '';
    if (newPassword.length < 6) { passwordMsg = 'Minimum 6 characters'; return; }
    if (newPassword !== confirmPassword) { passwordMsg = 'Passwords do not match'; return; }
    passwordSaving = true;
    try {
      // When force-changing (onboarding), don't require current password
      const current = forcePasswordChange ? '_force_change_' : currentPassword;
      await portal.changePassword(current, newPassword, $authStore.token!);
      toasts.success('Password changed');
      currentPassword = ''; newPassword = ''; confirmPassword = '';
      forcePasswordChange = false;
    } catch (e: any) { passwordMsg = e.message || 'Failed to change password'; }
    passwordSaving = false;
  }

  async function generateToken() {
    tokenGenerating = true;
    newTokenUrl = null;
    tokenQrDataUrl = null;
    try {
      const t = await portal.generateToken($authStore.token!);
      newTokenUrl = t.token;
      // Generate QR code for the enrollment URL
      if (newTokenUrl) {
        tokenQrDataUrl = await QRCode.toDataURL(newTokenUrl, {
          width: 200,
          margin: 2,
          color: { dark: '#0f172a', light: '#ffffff' },
        });
      }
      await loadTokens();
      toasts.success('Enrollment token generated');
    } catch (e: any) { toasts.error(e.message || 'Failed to generate token'); }
    tokenGenerating = false;
  }

  async function sendOTPTest() {
    // Removed — OTP is passwordless login, not MFA setup
  }

  async function verifyOTPTest() {
    // Removed
  }

  function resetOTP() {
    // Removed
  }

  // MFA (TOTP) functions
  async function loadMFAStatus() {
    try {
      const res = await portal.mfaStatus($authStore.token!);
      mfaEnabled = res.enabled;
    } catch { mfaEnabled = false; }
  }

  async function startMFASetup() {
    mfaError = '';
    mfaSettingUp = true;
    try {
      const res = await portal.mfaSetup($authStore.token!);
      mfaSecret = res.secret;
      mfaOtpauthUri = res.otpauth_uri;
      mfaSetupStep = 'show_qr';
    } catch (e: any) { mfaError = e.message || 'Failed to start setup'; }
    mfaSettingUp = false;
  }

  async function confirmMFASetup() {
    mfaError = '';
    mfaConfirming = true;
    try {
      await portal.mfaVerify(mfaSetupCode, $authStore.token!);
      mfaEnabled = true;
      mfaSetupStep = 'idle';
      mfaSetupCode = '';
      toasts.success('MFA enabled — authenticator app is now active');
    } catch (e: any) { mfaError = e.message || 'Invalid code'; }
    mfaConfirming = false;
  }

  async function disableMFA() {
    mfaError = '';
    mfaDisabling = true;
    try {
      await portal.mfaDisable(mfaDisableCode, $authStore.token!);
      mfaEnabled = false;
      mfaDisableCode = '';
      toasts.success('MFA disabled');
    } catch (e: any) { mfaError = e.message || 'Invalid code'; }
    mfaDisabling = false;
  }

  function cancelMFASetup() {
    mfaSetupStep = 'idle';
    mfaSetupCode = '';
    mfaSecret = '';
    mfaOtpauthUri = '';
    mfaError = '';
  }

  function copyToken() {
    if (newTokenUrl) {
      navigator.clipboard.writeText(`wireztna enroll "${newTokenUrl}"`);
      copied = true;
      setTimeout(() => { copied = false; }, 2000);
    }
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

  function tokenStatus(t: any): string {
    if (t.status === 'used') return 'used';
    if (t.status === 'revoked') return 'revoked';
    if (t.status === 'expired') return 'expired';
    return 'pending';
  }

  // ─── Delegation (Access Passes) ───
  async function loadPasses() {
    try {
      passes = await accessPasses.list($authStore.token!);
      passesLoaded = true;
    } catch { passes = []; passesLoaded = true; }
    // Load accessible publishers for the dropdown
    if (accessiblePublishers.length === 0) {
      try {
        const acc = await portal.access($authStore.token!);
        if (acc?.groups) {
          const seen = new Set<string>();
          for (const g of acc.groups) {
            for (const p of (g.publishers || [])) {
              if (p.status === 'online' && p.id && !seen.has(p.id)) {
                seen.add(p.id);
                accessiblePublishers.push({ id: p.id, name: p.name, cidrs: p.exposed_cidrs || [] });
              }
            }
          }
          accessiblePublishers = accessiblePublishers;
        }
      } catch { }
    }
  }

  async function createPass() {
    if (!newPassLabel || !newPassPublisher || !newPassCidr || !newPassPort) {
      toasts.error('All fields are required');
      return;
    }
    passCreating = true;
    createdPassId = null;
    createdPassUrl = null;
    try {
      const result = await accessPasses.create({
        label: newPassLabel,
        scope: {
          publishers: [newPassPublisher],
          cidrs: [newPassCidr.includes('/') ? newPassCidr : `${newPassCidr}/32`],
          ports: [parseInt(newPassPort)],
        },
        ttl_seconds: newPassTtl,
      }, $authStore.token!);
      createdPassId = result.pass_id;
      createdPassUrl = result.connection_url;
      showCreatePass = false;
      newPassLabel = '';
      newPassCidr = '';
      newPassPort = '';
      await loadPasses();
      toasts.success(`Access pass created: ${result.pass_id}`);
    } catch (e: any) {
      toasts.error(e.message || 'Failed to create pass');
    }
    passCreating = false;
  }

  async function revokePass(passId: string) {
    try {
      await accessPasses.revoke(passId, $authStore.token!);
      await loadPasses();
      toasts.success(`Pass ${passId} revoked`);
    } catch (e: any) { toasts.error(e.message || 'Failed to revoke'); }
  }

  function copyPassCommand(passId: string) {
    const cmd = `wireztna-proxy --pass "${passId}" --broker "${brokerWsBase}" --local-port <PORT>`;
    navigator.clipboard.writeText(cmd);
    passCopied = true;
    setTimeout(() => { passCopied = false; }, 2000);
  }

  function formatTtl(seconds: number): string {
    if (seconds >= 3600) return `${Math.floor(seconds / 3600)}h`;
    return `${Math.floor(seconds / 60)}m`;
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>My Portal</h1>
    <p class="page-subtitle">Manage your account, devices, and view your access policies.</p>
  </div>
  {#if status}
    <div class="connection-badge" class:online={status.is_connected} class:offline={!status.is_connected}>
      <span class="dot"></span>
      {status.is_connected ? 'Connected' : 'Disconnected'}
    </div>
  {/if}
</div>

{#if loading}
  <p class="loading">Loading...</p>
{:else}

  <!-- System Announcements Banner -->
  {#if activeAnnouncements.length > 0}
    <div class="announcements-banner">
      <div class="ann-header"><Bell size={14} /> System Announcements</div>
      {#each activeAnnouncements as ann}
        <div class="ann-item type-{ann.type}">
          <strong>{ann.title}</strong>
          <p>{ann.message}</p>
        </div>
      {/each}
    </div>
  {/if}

  <!-- Tabs -->
  <div class="tabs">
    <button class:active={activeTab === 'account'} on:click={() => switchTab('account')}>
      <Mail size={14} /> Account
    </button>
    <button class:active={activeTab === 'security'} on:click={() => switchTab('security')}>
      <Key size={14} /> Security
    </button>
    <button class:active={activeTab === 'devices'} on:click={() => switchTab('devices')}>
      <Monitor size={14} /> Devices
    </button>
    <button class:active={activeTab === 'access'} on:click={() => switchTab('access')}>
      <Network size={14} /> Access
    </button>
    <button class:active={activeTab === 'status'} on:click={() => switchTab('status')}>
      <Shield size={14} /> Status
    </button>
    <button class:active={activeTab === 'delegation'} on:click={() => switchTab('delegation')}>
      <Share2 size={14} /> Delegation
    </button>
  </div>

  <!-- ACCOUNT TAB -->
  {#if activeTab === 'account'}
    <div class="card">
      <h3>My Profile</h3>
      <div class="info-grid">
        <div class="info-item">
          <span class="label">Username</span>
          <span class="value">{profile?.username}</span>
        </div>
        <div class="info-item">
          <span class="label">Overlay IP</span>
          <span class="value"><code>{profile?.overlay_ip || 'Not assigned'}</code></span>
        </div>
        <div class="info-item">
          <span class="label">Status</span>
          <span class="value"><span class="badge {profile?.status}">{profile?.status}</span></span>
        </div>
        <div class="info-item">
          <span class="label">VPN Mode</span>
          <span class="value">{profile?.vpn_mode ? 'Enabled' : 'Disabled'}</span>
        </div>
      </div>
    </div>

    <div class="card">
      <h3>Email Address</h3>
      <p class="card-desc">This email is used for OTP login codes and notifications. You must confirm your current password to change it.</p>
      <div class="email-form">
        <input type="email" bind:value={editEmail} placeholder="your@email.com" />
        <input type="password" bind:value={emailPassword} placeholder="Current password" />
        <button class="btn-primary" on:click={saveEmail} disabled={emailSaving || editEmail === (profile?.email || '') || !emailPassword}>
          {emailSaving ? 'Saving...' : 'Save'}
        </button>
      </div>
    </div>

  <!-- SECURITY TAB -->
  {:else if activeTab === 'security'}
    {#if forcePasswordChange}
      <div class="force-change-banner">
        <strong>Password change required</strong>
        <p>You're using a temporary password. Please set a new one before continuing.</p>
      </div>
    {/if}
    <div class="card">
      <h3>{forcePasswordChange ? 'Set Your Password' : 'Change Password'}</h3>
      <p class="card-desc">{forcePasswordChange ? 'You need to set a password to complete your account activation.' : 'Your password is used as a fallback login method. OTP email login does not require a password.'}</p>
      <div class="form-stack">
        {#if !forcePasswordChange}
        <div class="field">
          <label>Current password</label>
          <input type="password" bind:value={currentPassword} placeholder="Current password" />
        </div>
        {/if}
        <div class="field">
          <label>New password</label>
          <input type="password" bind:value={newPassword} placeholder="Min 6 characters" />
        </div>
        <div class="field">
          <label>Confirm new password</label>
          <input type="password" bind:value={confirmPassword} placeholder="Repeat new password" />
        </div>
        {#if passwordMsg}
          <div class="form-error">{passwordMsg}</div>
        {/if}
        <button class="btn-primary" on:click={changePassword} disabled={passwordSaving || (!forcePasswordChange && !currentPassword) || !newPassword}>
          {passwordSaving ? 'Saving...' : forcePasswordChange ? 'Set Password' : 'Change Password'}
        </button>
      </div>
    </div>

    <div class="card">
      <h3>Passwordless Login (OTP Email)</h3>
      <p class="card-desc">
        {#if profile?.email}
          Login codes are sent to <strong>{profile.email}</strong>. This is your primary login method — no password needed.
        {:else}
          <span class="text-warning">No email configured. Set your email in the Account tab to enable passwordless login.</span>
        {/if}
      </p>
    </div>

    <div class="card">
      <h3>MFA — Authenticator App</h3>
      <p class="card-desc">Add a second factor using an authenticator app (Google Authenticator, Authy, 1Password, etc.)</p>

      {#if mfaEnabled}
        <div class="mfa-verified">
          <span class="verified-badge">Active</span>
          <p>MFA is enabled. You'll be asked for a code from your authenticator app on every login.</p>
        </div>
        <div class="mfa-disable-section">
          <p class="card-desc">To disable MFA, enter your current authenticator code:</p>
          <div class="otp-verify-form">
            <input type="text" bind:value={mfaDisableCode} placeholder="6-digit code" maxlength="6" inputmode="numeric" class="otp-input" />
            <button class="btn-danger" on:click={disableMFA} disabled={mfaDisabling || mfaDisableCode.length < 6}>
              {mfaDisabling ? 'Disabling...' : 'Disable MFA'}
            </button>
          </div>
          {#if mfaError}<div class="form-error">{mfaError}</div>{/if}
        </div>
      {:else if mfaSetupStep === 'idle'}
        <button class="btn-primary" on:click={startMFASetup} disabled={mfaSettingUp}>
          {mfaSettingUp ? 'Generating...' : 'Set up authenticator'}
        </button>
        {#if mfaError}<div class="form-error">{mfaError}</div>{/if}
      {:else if mfaSetupStep === 'show_qr'}
        <div class="mfa-setup-box">
          <p>Scan this QR code with your authenticator app:</p>
          <div class="qr-container">
            <img src="https://api.qrserver.com/v1/create-qr-code/?size=200x200&data={encodeURIComponent(mfaOtpauthUri)}" alt="MFA QR Code" />
          </div>
          <details class="manual-entry">
            <summary>Can't scan? Enter manually</summary>
            <code class="secret-display">{mfaSecret}</code>
          </details>
          <p class="card-desc">Then enter the 6-digit code shown in your app to confirm:</p>
          <div class="otp-verify-form">
            <input type="text" bind:value={mfaSetupCode} placeholder="6-digit code" maxlength="6" inputmode="numeric" class="otp-input" />
            <button class="btn-primary" on:click={confirmMFASetup} disabled={mfaConfirming || mfaSetupCode.length < 6}>
              {mfaConfirming ? 'Verifying...' : 'Activate MFA'}
            </button>
          </div>
          {#if mfaError}<div class="form-error">{mfaError}</div>{/if}
          <button class="btn-link" on:click={cancelMFASetup}>Cancel</button>
        </div>
      {/if}
    </div>

  <!-- DEVICES TAB -->
  {:else if activeTab === 'devices'}
    <div class="card">
      <h3>Enrollment Tokens</h3>
      <p class="card-desc">
        Use enrollment tokens to connect new devices. You can generate up to <strong>{profile?.max_enrollment_tokens || 3}</strong> tokens.
      </p>

      {#if newTokenUrl}
        <div class="new-token-box">
          <p class="token-label">Scan this QR code with the WireZTNA app, or copy the command below:</p>
          {#if tokenQrDataUrl}
            <div class="token-qr">
              <img src={tokenQrDataUrl} alt="Enrollment QR Code" width="200" height="200" />
            </div>
          {/if}
          <div class="token-value">
            <code>wireztna enroll "{newTokenUrl}"</code>
            <button class="btn-copy" on:click={copyToken}>{copied ? 'Copied!' : 'Copy'}</button>
          </div>
          <p class="token-warning">This URL is shown only once. Copy it now or scan the QR.</p>
        </div>
      {/if}

      <button class="btn-primary" on:click={generateToken} disabled={tokenGenerating} style="margin-bottom: 1rem;">
        {tokenGenerating ? 'Generating...' : '+ New Device Token'}
      </button>

      {#if tokens.length > 0}
        <table class="data-table compact">
          <thead><tr><th>Status</th><th>Note</th><th>Created</th><th>Expires</th></tr></thead>
          <tbody>
            {#each tokens as t}
              <tr>
                <td><span class="badge {tokenStatus(t)}">{tokenStatus(t)}</span></td>
                <td>{t.note || '—'}</td>
                <td>{new Date(t.created_at).toLocaleDateString()}</td>
                <td>{t.expires_at ? new Date(t.expires_at).toLocaleDateString() : '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="text-muted">No enrollment tokens yet.</p>
      {/if}
    </div>

  <!-- ACCESS TAB -->
  {:else if activeTab === 'access'}
    {#if access}
      <div class="card">
        <h3>My Groups</h3>
        {#if access.groups.length > 0}
          {#each access.groups as group}
            <div class="access-group">
              <div class="group-header">
                <span class="group-name">{group.name}</span>
                {#if group.description}<span class="group-desc">{group.description}</span>{/if}
              </div>
              {#if group.publishers.length > 0}
                <div class="publishers-list">
                  {#each group.publishers as pub}
                    <div class="pub-row">
                      <span class="pub-dot" class:online={pub.status === 'online'} class:offline={pub.status !== 'online'}></span>
                      <span class="pub-name">{pub.name}</span>
                      <span class="badge {pub.status}">{pub.status}</span>
                      {#if pub.access_policy === 'restricted'}
                        <span class="policy-badge restricted">Restricted</span>
                      {:else}
                        <span class="policy-badge unrestricted">Full access</span>
                      {/if}
                    </div>
                    {#if pub.exposed_cidrs.length > 0}
                      <div class="pub-cidrs">
                        {#each pub.exposed_cidrs as cidr}
                          <code class="cidr-tag">{cidr}</code>
                        {/each}
                      </div>
                    {/if}
                    {#if pub.access_policy === 'restricted' && pub.allowed_apps?.length}
                      <div class="allowed-apps">
                        {#each pub.allowed_apps as app}
                          <code class="app-tag">{app.target}:{app.port}/{app.protocol}{#if app.name} ({app.name}){/if}</code>
                        {/each}
                      </div>
                    {/if}
                  {/each}
                </div>
              {:else}
                <p class="text-muted">No publishers in this group.</p>
              {/if}
            </div>
          {/each}
        {:else}
          <p class="text-muted">Not assigned to any groups. Contact your administrator.</p>
        {/if}
      </div>

      {#if access.dns_zones.length > 0}
        <div class="card">
          <h3>DNS Zones</h3>
          <div class="cidr-tags">
            {#each access.dns_zones as zone}
              <code class="cidr-tag">{zone}</code>
            {/each}
          </div>
        </div>
      {/if}

      <div class="card">
        <h3>All Accessible CIDRs</h3>
        <div class="cidr-tags">
          {#each access.total_cidrs as cidr}
            <code class="cidr-tag">{cidr}</code>
          {/each}
        </div>
      </div>
    {:else}
      <p class="loading">Loading access data...</p>
    {/if}

  <!-- STATUS TAB -->
  {:else if activeTab === 'status'}
    {#if status}
      <div class="card">
        <h3>Tunnel</h3>
        <div class="info-grid">
          <div class="info-item">
            <span class="label">Status</span>
            <span class="value">{#if status.is_connected}<span class="badge online">Connected</span>{:else}<span class="badge offline">Disconnected</span>{/if}</span>
          </div>
          <div class="info-item">
            <span class="label">Overlay IP</span>
            <span class="value"><code>{status.overlay_ip || '—'}</code></span>
          </div>
          <div class="info-item">
            <span class="label">Handshake</span>
            <span class="value">{formatAge(status.handshake_age_seconds)}</span>
          </div>
          <div class="info-item">
            <span class="label">Traffic</span>
            <span class="value">↓ {formatBytes(status.rx_bytes)}  ↑ {formatBytes(status.tx_bytes)}</span>
          </div>
        </div>
      </div>

      {#if status.active_session}
        <div class="card">
          <h3>Session</h3>
          <div class="info-grid">
            <div class="info-item">
              <span class="label">Expires in</span>
              <span class="value" class:warning={status.active_session.ttl_remaining_seconds < 300}>{formatAge(status.active_session.ttl_remaining_seconds)}</span>
            </div>
            <div class="info-item">
              <span class="label">Scope</span>
              <span class="value">{status.active_session.selected_group_name || 'All groups'}</span>
            </div>
          </div>
        </div>
      {/if}

      {#if status.issues.length > 0}
        <div class="issues-banner">
          {#each status.issues as issue}<p>{issue}</p>{/each}
        </div>
      {/if}
    {:else}
      <p class="text-muted">Unable to load connection status.</p>
    {/if}
  {:else if activeTab === 'delegation'}
    <!-- DELEGATION TAB -->
    <div class="card">
      <div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 1rem;">
        <h3 style="margin: 0;">Access Passes</h3>
        <button class="btn-primary btn-sm" on:click={() => { showCreatePass = true; createdPassId = null; createdPassUrl = null; }}>
          + New Pass
        </button>
      </div>
      <p class="text-muted" style="margin-bottom: 1rem; font-size: 0.8rem;">
        Create temporary, scoped access passes for AI agents, consultants, or CI pipelines. Recipients use <code>wireztna-proxy</code> to connect.
      </p>

      {#if createdPassId}
        <div class="success-banner" style="margin-bottom: 1rem;">
          <strong>Pass created: {createdPassId}</strong>
          <div style="margin-top: 0.5rem; font-size: 0.78rem; color: var(--text-secondary);">
            Share this command with the recipient:
          </div>
          <code class="pass-command" style="display: block; margin-top: 0.5rem; padding: 0.5rem; background: var(--bg-elevated); border-radius: 4px; font-size: 0.75rem; word-break: break-all;">
            wireztna-proxy --pass "{createdPassId}" --broker "{brokerWsBase}" --local-port &lt;PORT&gt;
          </code>
          <button class="btn-sm" style="margin-top: 0.5rem;" on:click={() => copyPassCommand(createdPassId || '')}>
            {passCopied ? '✓ Copied' : 'Copy command'}
          </button>
        </div>
      {/if}

      {#if showCreatePass}
        <div class="create-pass-form" style="border: 1px solid var(--border-default); border-radius: var(--radius-md); padding: 1rem; margin-bottom: 1rem; background: var(--bg-elevated);">
          <h4 style="margin: 0 0 0.75rem; font-size: 0.85rem;">Create Access Pass</h4>
          <div class="form-grid" style="display: grid; grid-template-columns: 1fr 1fr; gap: 0.75rem;">
            <div style="grid-column: 1 / -1;">
              <label class="form-label">Label (what is this for?)</label>
              <input type="text" bind:value={newPassLabel} placeholder="e.g., SSH access for DBA audit" class="input" />
            </div>
            <div>
              <label class="form-label">Target IP/CIDR</label>
              <input type="text" bind:value={newPassCidr} placeholder="10.0.0.68" class="input" />
            </div>
            <div>
              <label class="form-label">Port</label>
              <input type="number" bind:value={newPassPort} placeholder="22" class="input" />
            </div>
            <div>
              <label class="form-label">Publisher</label>
              <select bind:value={newPassPublisher} class="input">
                <option value="">Select a publisher...</option>
                {#each accessiblePublishers as pub}
                  <option value={pub.id}>{pub.name} ({pub.cidrs.join(', ')})</option>
                {/each}
              </select>
            </div>
            <div>
              <label class="form-label">TTL</label>
              <select bind:value={newPassTtl} class="input">
                <option value={300}>5 minutes</option>
                <option value={900}>15 minutes</option>
                <option value={1800}>30 minutes</option>
                <option value={3600}>1 hour</option>
                <option value={7200}>2 hours</option>
              </select>
            </div>
          </div>
          <div style="margin-top: 0.75rem; display: flex; gap: 0.5rem;">
            <button class="btn-primary btn-sm" on:click={createPass} disabled={passCreating}>
              {passCreating ? 'Creating...' : 'Create Pass'}
            </button>
            <button class="btn-sm" on:click={() => { showCreatePass = false; }}>Cancel</button>
          </div>
        </div>
      {/if}

      {#if passes.length > 0}
        <div class="passes-list">
          {#each passes as pass}
            <div class="pass-item" class:expired={pass.status !== 'active'}>
              <div style="display: flex; justify-content: space-between; align-items: flex-start;">
                <div>
                  <strong style="font-size: 0.85rem;">{pass.label}</strong>
                  <div style="font-size: 0.75rem; color: var(--text-secondary); margin-top: 0.2rem;">
                    <code>{pass.pass_id}</code> · {pass.scope_summary}
                  </div>
                </div>
                <span class="badge {pass.status}">{pass.status}</span>
              </div>
              <div style="display: flex; gap: 1rem; margin-top: 0.5rem; font-size: 0.72rem; color: var(--text-secondary);">
                <span>TTL: {formatTtl(pass.ttl_seconds || ((new Date(pass.expires_at).getTime() - new Date(pass.created_at).getTime()) / 1000))}</span>
                <span>↑ {formatBytes(pass.bytes_uploaded)} ↓ {formatBytes(pass.bytes_downloaded)}</span>
                <span>{pass.connections_count} conn</span>
              </div>
              {#if pass.status === 'active'}
                <div style="margin-top: 0.5rem; display: flex; gap: 0.5rem;">
                  <button class="btn-sm" on:click={() => copyPassCommand(pass.pass_id)}>Copy command</button>
                  <button class="btn-sm btn-danger" on:click={() => revokePass(pass.pass_id)}>Revoke</button>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {:else if passesLoaded}
        <p class="text-muted" style="text-align: center; padding: 2rem 0; font-size: 0.85rem;">No access passes yet. Create one to delegate access.</p>
      {/if}
    </div>
  {/if}
{/if}

<style>
  /* Announcements Banner */
  .announcements-banner { background: var(--bg-surface); border: 1px solid var(--accent-primary-dim); border-radius: var(--radius-md); padding: 1rem 1.25rem; margin-bottom: 1.25rem; }
  .ann-header { display: flex; align-items: center; gap: 0.5rem; font-size: 0.78rem; font-weight: 600; color: var(--accent-blue); text-transform: uppercase; letter-spacing: 0.03em; margin-bottom: 0.5rem; }
  .ann-item { padding: 0.5rem 0; border-top: 1px solid var(--border-subtle); }
  .ann-item:first-of-type { border-top: none; }
  .ann-item strong { font-size: 0.85rem; color: var(--text-primary); }
  .ann-item p { margin: 0.2rem 0 0; font-size: 0.8rem; color: var(--text-secondary); }
  .ann-item.type-warning strong { color: var(--accent-orange); }
  .ann-item.type-success strong { color: var(--accent-green); }
  .ann-item.type-update strong { color: var(--accent-purple); }

  /* Connection Badge */
  .connection-badge { display: flex; align-items: center; gap: 0.5rem; padding: 0.5rem 1rem; border-radius: var(--radius-sm); font-size: 0.85rem; font-weight: 600; }
  .connection-badge.online { background: var(--accent-green-dim); color: var(--accent-green); }
  .connection-badge.offline { background: var(--accent-red-dim); color: var(--accent-red); }
  .connection-badge .dot { width: 8px; height: 8px; border-radius: 50%; }
  .connection-badge.online .dot { background: var(--accent-green); box-shadow: 0 0 6px var(--accent-green); }
  .connection-badge.offline .dot { background: var(--accent-red); }

  /* Tabs */
  .tabs { display: flex; gap: 0; margin-bottom: 1.25rem; border-bottom: 1px solid var(--border-subtle); overflow-x: auto; }
  .tabs button { display: flex; align-items: center; gap: 0.4rem; padding: 0.7rem 1rem; background: none; border: none; color: var(--text-secondary); font-size: 0.82rem; cursor: pointer; border-bottom: 2px solid transparent; transition: color 0.15s, border-color 0.15s; white-space: nowrap; }
  .tabs button.active { color: var(--accent-blue); border-bottom-color: var(--accent-blue); }
  .tabs button:hover { color: var(--text-primary); }

  /* Cards */
  .card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 1.25rem; margin-bottom: 1rem; }
  .card h3 { margin: 0 0 0.5rem; font-size: 0.9rem; color: var(--text-primary); font-weight: 600; }
  .card-desc { font-size: 0.8rem; color: var(--text-secondary); margin: 0 0 1rem; }

  /* Info grid */
  .info-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 0.75rem; }
  .info-item { display: flex; flex-direction: column; gap: 0.2rem; }
  .info-item .label { font-size: 0.7rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.03em; }
  .info-item .value { font-size: 0.85rem; color: var(--text-primary); }
  .info-item .value.warning { color: var(--accent-orange); }

  /* Email form */
  .email-form { display: flex; gap: 0.5rem; align-items: center; }
  .email-form input { flex: 1; padding: 0.5rem 0.75rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.85rem; font-family: inherit; }
  .email-form input:focus { outline: none; border-color: var(--accent-blue); }

  /* Form stack */
  .form-stack { display: flex; flex-direction: column; gap: 0.75rem; max-width: 360px; }
  .field { display: flex; flex-direction: column; gap: 0.25rem; }
  .field label { font-size: 0.75rem; color: var(--text-secondary); font-weight: 500; }
  .field input { padding: 0.5rem 0.75rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.85rem; font-family: inherit; }
  .field input:focus { outline: none; border-color: var(--accent-blue); }
  .form-error { font-size: 0.8rem; color: var(--accent-red); }

  /* Buttons */
  .btn-primary { padding: 0.5rem 1rem; background: var(--accent-primary); color: white; border: none; border-radius: var(--radius-sm); font-size: 0.82rem; font-weight: 600; font-family: inherit; cursor: pointer; transition: opacity 0.15s; }
  .btn-primary:hover:not(:disabled) { opacity: 0.9; }
  .btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Token display */
  .new-token-box { background: var(--bg-elevated); border: 1px solid rgba(52, 211, 153, 0.3); border-radius: var(--radius-sm); padding: 1rem; margin-bottom: 1rem; }
  .token-label { font-size: 0.78rem; color: var(--text-secondary); margin: 0 0 0.5rem; }
  .token-qr { display: flex; justify-content: center; margin: 0.75rem 0; }
  .token-qr img { border-radius: var(--radius-sm); border: 1px solid var(--border-default); }
  .token-value { display: flex; align-items: center; gap: 0.5rem; }
  .token-value code { flex: 1; font-size: 0.75rem; padding: 0.4rem 0.6rem; background: var(--bg-primary); border-radius: 3px; overflow-x: auto; white-space: nowrap; }
  .btn-copy { padding: 0.35rem 0.75rem; background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); font-size: 0.75rem; cursor: pointer; font-family: inherit; color: var(--text-primary); }
  .btn-copy:hover { background: var(--bg-elevated); }
  .token-warning { font-size: 0.72rem; color: var(--accent-orange); margin: 0.4rem 0 0; }

  /* Access groups */
  .access-group { margin-bottom: 1rem; padding-bottom: 1rem; border-bottom: 1px solid var(--border-subtle); }
  .access-group:last-child { border-bottom: none; margin-bottom: 0; padding-bottom: 0; }
  .group-header { margin-bottom: 0.5rem; }
  .group-name { font-weight: 600; font-size: 0.88rem; color: var(--text-primary); }
  .group-desc { margin-left: 0.5rem; font-size: 0.78rem; color: var(--text-muted); }

  /* Publishers */
  .publishers-list { display: flex; flex-direction: column; gap: 0.4rem; }
  .pub-row { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.6rem; background: var(--bg-elevated); border-radius: var(--radius-sm); font-size: 0.8rem; flex-wrap: wrap; }
  .pub-dot { width: 7px; height: 7px; border-radius: 50%; }
  .pub-dot.online { background: var(--accent-green); }
  .pub-dot.offline { background: var(--accent-red); }
  .pub-name { font-weight: 500; color: var(--text-primary); }
  .pub-cidrs { padding: 0.2rem 0 0.3rem 1.5rem; display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .allowed-apps { padding: 0.2rem 0 0.3rem 1.5rem; display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .app-tag { font-size: 0.7rem; padding: 0.12rem 0.35rem; background: var(--bg-primary); border: 1px solid var(--border-subtle); border-radius: 3px; color: var(--text-secondary); }

  .policy-badge { font-size: 0.65rem; padding: 0.1rem 0.35rem; border-radius: 3px; font-weight: 500; }
  .policy-badge.restricted { background: rgba(248, 113, 113, 0.12); color: var(--accent-red); }
  .policy-badge.unrestricted { background: rgba(52, 211, 153, 0.1); color: var(--accent-green); }

  .cidr-tags { display: flex; flex-wrap: wrap; gap: 0.3rem; }
  .cidr-tag { padding: 0.2rem 0.5rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: 3px; font-size: 0.78rem; }

  /* Issues */
  .issues-banner { background: rgba(248, 113, 113, 0.08); border: 1px solid rgba(248, 113, 113, 0.2); border-radius: var(--radius-sm); padding: 0.75rem 1rem; margin-bottom: 1rem; }
  .issues-banner p { margin: 0.2rem 0; font-size: 0.8rem; color: var(--accent-red); }

  /* Force password change */
  .force-change-banner { background: rgba(251, 191, 36, 0.1); border: 1px solid rgba(245, 158, 11, 0.4); border-radius: var(--radius-sm); padding: 1rem 1.25rem; margin-bottom: 1rem; }
  .force-change-banner strong { color: #92400e; font-size: 0.9rem; }
  .force-change-banner p { margin: 0.3rem 0 0; font-size: 0.82rem; color: #a16207; }

  /* Table */
  .data-table.compact { width: 100%; border-collapse: collapse; font-size: 0.78rem; }
  .data-table.compact th { text-align: left; padding: 0.4rem 0.6rem; color: var(--text-muted); font-weight: 500; border-bottom: 1px solid var(--border-subtle); }
  .data-table.compact td { padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--border-subtle); color: var(--text-primary); }

  .loading { color: var(--text-secondary); font-size: 0.85rem; }
  .text-muted { color: var(--text-muted); font-size: 0.82rem; }
  .text-warning { color: var(--accent-orange); }

  .badge { font-size: 0.7rem; padding: 0.12rem 0.4rem; border-radius: 3px; font-weight: 500; }
  .badge.online, .badge.active { background: var(--accent-green-dim); color: var(--accent-green); }
  .badge.offline, .badge.disabled { background: var(--accent-red-dim); color: var(--accent-red); }
  .badge.pending { background: rgba(251, 191, 36, 0.12); color: var(--accent-orange); }
  .badge.used { background: var(--accent-primary-dim); color: var(--accent-primary); }
  .badge.expired, .badge.revoked { background: var(--bg-elevated); color: var(--text-muted); }

  /* OTP Verification */
  .otp-verify-form { display: flex; gap: 0.5rem; align-items: center; margin-bottom: 0.5rem; }
  .otp-verify-form .otp-input { width: 160px; text-align: center; font-size: 1.2rem; letter-spacing: 0.3rem; font-weight: 600; padding: 0.5rem 0.75rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); font-family: inherit; }
  .otp-verify-form .otp-input:focus { outline: none; border-color: var(--accent-primary); box-shadow: 0 0 0 2px var(--accent-primary-dim); }
  .btn-link { background: none; border: none; color: var(--accent-blue); font-size: 0.78rem; cursor: pointer; padding: 0.25rem 0; font-family: inherit; }
  .btn-link:hover:not(:disabled) { text-decoration: underline; }
  .btn-link:disabled { opacity: 0.5; }
  .mfa-verified { padding: 0.75rem 1rem; background: rgba(52, 211, 153, 0.08); border: 1px solid rgba(52, 211, 153, 0.25); border-radius: var(--radius-sm); }
  .mfa-verified p { margin: 0.3rem 0; font-size: 0.82rem; color: var(--text-secondary); }
  .verified-badge { display: inline-block; font-size: 0.72rem; font-weight: 600; padding: 0.15rem 0.5rem; border-radius: 3px; background: rgba(52, 211, 153, 0.15); color: var(--accent-green); text-transform: uppercase; letter-spacing: 0.03em; }

  /* MFA Setup */
  .mfa-setup-box { margin-top: 0.5rem; }
  .mfa-setup-box p { font-size: 0.82rem; color: var(--text-secondary); margin: 0.5rem 0; }
  .qr-container { display: flex; justify-content: center; padding: 1rem; background: white; border-radius: var(--radius-sm); width: fit-content; margin: 0.75rem 0; }
  .qr-container img { width: 200px; height: 200px; }
  .manual-entry { margin: 0.5rem 0; font-size: 0.78rem; color: var(--text-muted); }
  .manual-entry summary { cursor: pointer; }
  .secret-display { display: block; margin-top: 0.4rem; padding: 0.5rem 0.75rem; background: var(--bg-elevated); border-radius: 3px; font-size: 0.85rem; letter-spacing: 0.1rem; word-break: break-all; user-select: all; }
  .mfa-disable-section { margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px solid var(--border-subtle); }
  .btn-danger { padding: 0.5rem 1rem; background: linear-gradient(135deg, #ef4444, #dc2626); color: white; border: none; border-radius: var(--radius-sm); font-size: 0.82rem; font-weight: 600; font-family: inherit; cursor: pointer; }
  .btn-danger:hover:not(:disabled) { opacity: 0.9; }
  .btn-danger:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Delegation (Access Passes) */
  .passes-list { display: flex; flex-direction: column; gap: 0.75rem; }
  .pass-item { padding: 0.75rem; border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); background: var(--bg-surface); }
  .pass-item.expired { opacity: 0.6; }
  .badge.active { background: var(--accent-green-dim); color: var(--accent-green); padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.7rem; font-weight: 600; }
  .badge.expired { background: var(--accent-orange-dim, rgba(245, 158, 11, 0.1)); color: var(--accent-orange, #f59e0b); padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.7rem; font-weight: 600; }
  .badge.revoked { background: var(--accent-red-dim, rgba(239, 68, 68, 0.1)); color: var(--accent-red, #ef4444); padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.7rem; font-weight: 600; }
  .success-banner { padding: 0.75rem 1rem; background: rgba(16, 185, 129, 0.08); border: 1px solid rgba(16, 185, 129, 0.25); border-radius: var(--radius-sm); }
  .success-banner strong { color: var(--accent-green); }
  .form-label { display: block; font-size: 0.75rem; font-weight: 500; color: var(--text-secondary); margin-bottom: 0.25rem; }
  .btn-sm.btn-danger { padding: 0.3rem 0.6rem; font-size: 0.72rem; background: rgba(239, 68, 68, 0.1); color: var(--accent-red, #ef4444); border: 1px solid rgba(239, 68, 68, 0.3); }
  .btn-sm.btn-danger:hover { background: rgba(239, 68, 68, 0.2); }
</style>
