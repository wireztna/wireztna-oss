<script lang="ts">
  import { authStore } from '$lib/stores/auth';
  import { Code, Copy, Check, ChevronDown, ChevronRight, Lock, Unlock } from 'lucide-svelte';

  let copiedId = '';

  function copyToClipboard(text: string, id: string) {
    navigator.clipboard.writeText(text);
    copiedId = id;
    setTimeout(() => copiedId = '', 2000);
  }

  interface Endpoint {
    method: string;
    path: string;
    description: string;
    auth: 'admin' | 'user' | 'none';
    body?: string;
    response?: string;
  }

  interface Section {
    title: string;
    description: string;
    endpoints: Endpoint[];
    expanded: boolean;
  }

  let sections: Section[] = [
    {
      title: 'Authentication',
      description: 'Login, OTP, MFA, and token management',
      expanded: true,
      endpoints: [
        { method: 'POST', path: '/api/v1/auth/otp/request', description: 'Request a one-time login code via email', auth: 'none', body: '{"email": "user@company.com"}' },
        { method: 'POST', path: '/api/v1/auth/otp/verify', description: 'Verify OTP code and receive JWT', auth: 'none', body: '{"email": "user@company.com", "code": "123456"}', response: '{"token": "eyJ...", "mfa_required": false}' },
        { method: 'POST', path: '/api/v1/auth/login', description: 'Login with email and password', auth: 'none', body: '{"email": "user@company.com", "password": "..."}' },
        { method: 'POST', path: '/api/v1/auth/mfa/verify', description: 'Verify TOTP code (second factor)', auth: 'none', body: '{"mfa_token": "...", "code": "123456"}' },
      ]
    },
    {
      title: 'Users',
      description: 'User CRUD, enrollment tokens, onboarding',
      expanded: false,
      endpoints: [
        { method: 'GET', path: '/api/v1/users', description: 'List all users', auth: 'admin' },
        { method: 'POST', path: '/api/v1/users', description: 'Create a new user', auth: 'admin', body: '{"email": "new@company.com", "password": "..."}' },
        { method: 'GET', path: '/api/v1/users/{id}', description: 'Get user details', auth: 'admin' },
        { method: 'PUT', path: '/api/v1/users/{id}', description: 'Update user', auth: 'admin' },
        { method: 'DELETE', path: '/api/v1/users/{id}', description: 'Delete user', auth: 'admin' },
        { method: 'POST', path: '/api/v1/users/{id}/enrollment-token', description: 'Generate enrollment token for a user', auth: 'admin', response: '{"token": "...", "url": "https://..."}' },
        { method: 'GET', path: '/api/v1/users/{id}/enrollment-tokens', description: 'List all enrollment tokens for a user', auth: 'admin' },
        { method: 'DELETE', path: '/api/v1/users/{id}/enrollment-tokens/{token_id}', description: 'Revoke a pending token', auth: 'admin' },
        { method: 'POST', path: '/api/v1/users/{id}/send-onboarding', description: 'Send onboarding email with activation link', auth: 'admin' },
      ]
    },
    {
      title: 'Groups',
      description: 'Access groups with user and publisher assignments',
      expanded: false,
      endpoints: [
        { method: 'GET', path: '/api/v1/groups', description: 'List all groups', auth: 'admin' },
        { method: 'POST', path: '/api/v1/groups', description: 'Create a group', auth: 'admin', body: '{"name": "project-alpha"}' },
        { method: 'PUT', path: '/api/v1/groups/{id}', description: 'Update group', auth: 'admin' },
        { method: 'DELETE', path: '/api/v1/groups/{id}', description: 'Delete group', auth: 'admin' },
        { method: 'POST', path: '/api/v1/groups/{id}/members', description: 'Add user to group', auth: 'admin', body: '{"user_id": "..."}' },
        { method: 'DELETE', path: '/api/v1/groups/{id}/members/{user_id}', description: 'Remove user from group', auth: 'admin' },
        { method: 'POST', path: '/api/v1/groups/{id}/publishers', description: 'Assign publisher to group', auth: 'admin', body: '{"publisher_id": "..."}' },
        { method: 'DELETE', path: '/api/v1/groups/{id}/publishers/{pub_id}', description: 'Remove publisher from group', auth: 'admin' },
      ]
    },
    {
      title: 'Publishers',
      description: 'Publisher management, enrollment, diagnostics',
      expanded: false,
      endpoints: [
        { method: 'GET', path: '/api/v1/publishers', description: 'List all publishers', auth: 'admin' },
        { method: 'PUT', path: '/api/v1/publishers/{id}', description: 'Update publisher properties', auth: 'admin' },
        { method: 'DELETE', path: '/api/v1/publishers/{id}', description: 'Delete publisher', auth: 'admin' },
        { method: 'POST', path: '/api/v1/publishers/{id}/disable', description: 'Disable a publisher', auth: 'admin' },
        { method: 'POST', path: '/api/v1/publishers/{id}/enable', description: 'Re-enable a publisher', auth: 'admin' },
        { method: 'POST', path: '/api/v1/publishers/{id}/reset', description: 'Reset publisher to pending state', auth: 'admin' },
        { method: 'GET', path: '/api/v1/publishers/{id}/diagnostics', description: 'Run diagnostics on a publisher', auth: 'admin' },
        { method: 'POST', path: '/api/v1/publishers/enrollment-token', description: 'Generate publisher enrollment token', auth: 'admin' },
      ]
    },
    {
      title: 'Sessions',
      description: 'Client session management and renewal',
      expanded: false,
      endpoints: [
        { method: 'POST', path: '/api/v1/sessions/renew', description: 'Renew session (get fresh PSK)', auth: 'user', body: '{"group_id": "...", "exit_node_id": null}' },
        { method: 'POST', path: '/api/v1/sessions/revoke', description: 'Revoke active session', auth: 'user' },
        { method: 'GET', path: '/api/v1/sessions/info', description: 'Get current session info', auth: 'user' },
        { method: 'GET', path: '/api/v1/sessions/available-groups', description: 'List groups available to the user', auth: 'user' },
        { method: 'GET', path: '/api/v1/sessions/dns-zones', description: 'Get DNS zones for current session', auth: 'user' },
      ]
    },
    {
      title: 'Client Enrollment',
      description: 'Self-enrollment via one-time tokens',
      expanded: false,
      endpoints: [
        { method: 'POST', path: '/api/v1/clients/enroll', description: 'Client self-enrollment (token-based, no auth required)', auth: 'none', body: '{"token": "...", "public_key": "base64..."}', response: '{"username": "...", "overlay_ip": "10.200.1.5", "broker_public_key": "...", "broker_endpoint": "..."}' },
      ]
    },
    {
      title: 'Diagnostics & Health',
      description: 'System health, debug information',
      expanded: false,
      endpoints: [
        { method: 'GET', path: '/health', description: 'Platform health check (public, no auth)', auth: 'none', response: '{"status": "healthy", "checks": {...}}' },
        { method: 'GET', path: '/api/v1/diagnostics/quick', description: 'Quick system status (public)', auth: 'none' },
        { method: 'GET', path: '/api/v1/diagnostics/full', description: 'Full diagnostic report', auth: 'admin' },
        { method: 'GET', path: '/api/v1/debug/system-metrics', description: 'Broker host metrics (CPU, RAM, disk, conntrack)', auth: 'admin' },
        { method: 'GET', path: '/api/v1/debug/nftables', description: 'Current firewall rules', auth: 'admin' },
        { method: 'GET', path: '/api/v1/debug/tunnels', description: 'WireGuard tunnels and namespace state', auth: 'admin' },
        { method: 'GET', path: '/api/v1/debug/routing', description: 'Policy routing tables', auth: 'admin' },
        { method: 'GET', path: '/api/v1/debug/dns-proxy', description: 'DNS proxy configuration', auth: 'admin' },
      ]
    },
    {
      title: 'Self-Service Portal',
      description: 'Endpoints for authenticated non-admin users',
      expanded: false,
      endpoints: [
        { method: 'GET', path: '/api/v1/me/profile', description: 'Get own profile', auth: 'user' },
        { method: 'PUT', path: '/api/v1/me/email', description: 'Update own email', auth: 'user', body: '{"email": "new@company.com", "current_password": "..."}' },
        { method: 'PUT', path: '/api/v1/me/password', description: 'Change own password', auth: 'user', body: '{"current_password": "...", "new_password": "..."}' },
        { method: 'GET', path: '/api/v1/me/tokens', description: 'List own enrollment tokens', auth: 'user' },
        { method: 'POST', path: '/api/v1/me/tokens', description: 'Generate self-service enrollment token', auth: 'user' },
        { method: 'GET', path: '/api/v1/me/access', description: 'View own access policies (groups, publishers, CIDRs)', auth: 'user' },
        { method: 'POST', path: '/api/v1/me/mfa/setup', description: 'Start MFA enrollment (returns QR URI)', auth: 'user' },
        { method: 'POST', path: '/api/v1/me/mfa/verify', description: 'Confirm MFA enrollment with code', auth: 'user' },
        { method: 'POST', path: '/api/v1/me/mfa/disable', description: 'Disable MFA (requires code)', auth: 'user' },
      ]
    },
    {
      title: 'Announcements',
      description: 'System-wide announcements (admin CRUD)',
      expanded: false,
      endpoints: [
        { method: 'GET', path: '/api/v1/announcements/active', description: 'Get active announcements (all authenticated users)', auth: 'user' },
        { method: 'GET', path: '/api/v1/announcements', description: 'List all announcements', auth: 'admin' },
        { method: 'POST', path: '/api/v1/announcements', description: 'Create announcement', auth: 'admin', body: '{"title": "...", "message": "...", "type": "info"}' },
        { method: 'PUT', path: '/api/v1/announcements/{id}', description: 'Update announcement', auth: 'admin' },
        { method: 'DELETE', path: '/api/v1/announcements/{id}', description: 'Delete announcement', auth: 'admin' },
      ]
    },
  ];

  function toggleSection(index: number) {
    sections[index].expanded = !sections[index].expanded;
    sections = sections; // trigger reactivity
  }

  function methodColor(method: string): string {
    switch (method) {
      case 'GET': return 'bg-blue-500/15 text-blue-400 border-blue-500/30';
      case 'POST': return 'bg-green-500/15 text-green-400 border-green-500/30';
      case 'PUT': return 'bg-amber-500/15 text-amber-400 border-amber-500/30';
      case 'DELETE': return 'bg-red-500/15 text-red-400 border-red-500/30';
      default: return 'bg-zinc-500/15 text-zinc-400 border-zinc-500/30';
    }
  }
</script>

<div class="page-container">
  <div class="page-header">
    <div>
      <h1 class="page-title">API Reference</h1>
      <p class="page-subtitle">REST API endpoints for automation and integration</p>
    </div>
  </div>

  <!-- Auth info -->
  <div class="info-banner">
    <div class="info-content">
      <p class="info-title">Authentication</p>
      <p class="info-text">
        Most endpoints require a JWT token in the <code>Authorization: Bearer &lt;token&gt;</code> header.
        Obtain a token via <code>POST /api/v1/auth/otp/verify</code> or <code>POST /api/v1/auth/login</code>.
      </p>
      <div class="auth-legend">
        <span class="auth-badge admin"><Lock size={12} /> Admin</span>
        <span class="auth-badge user"><Lock size={12} /> Authenticated</span>
        <span class="auth-badge public"><Unlock size={12} /> Public</span>
      </div>
    </div>
  </div>

  <!-- Base URL -->
  <div class="base-url">
    <span class="base-url-label">Base URL</span>
    <code class="base-url-value">{$authStore.token ? window.location.origin : 'https://your-broker-url'}</code>
  </div>

  <!-- Sections -->
  <div class="sections">
    {#each sections as section, i}
      <div class="section-card">
        <button class="section-header" on:click={() => toggleSection(i)}>
          <div class="section-title-group">
            {#if section.expanded}
              <ChevronDown size={18} class="section-chevron" />
            {:else}
              <ChevronRight size={18} class="section-chevron" />
            {/if}
            <h2 class="section-title">{section.title}</h2>
            <span class="section-count">{section.endpoints.length}</span>
          </div>
          <p class="section-desc">{section.description}</p>
        </button>

        {#if section.expanded}
          <div class="endpoints">
            {#each section.endpoints as endpoint, j}
              <div class="endpoint">
                <div class="endpoint-header">
                  <span class="method-badge {methodColor(endpoint.method)}">{endpoint.method}</span>
                  <code class="endpoint-path">{endpoint.path}</code>
                  <span class="auth-indicator" class:admin={endpoint.auth === 'admin'} class:user={endpoint.auth === 'user'} class:public={endpoint.auth === 'none'}>
                    {endpoint.auth === 'admin' ? 'Admin' : endpoint.auth === 'user' ? 'Auth' : 'Public'}
                  </span>
                </div>
                <p class="endpoint-desc">{endpoint.description}</p>
                {#if endpoint.body}
                  <div class="code-sample">
                    <div class="code-label">
                      <span>Request body</span>
                      <button class="copy-btn" on:click={() => copyToClipboard(endpoint.body || '', `body-${i}-${j}`)}>
                        {#if copiedId === `body-${i}-${j}`}<Check size={12} />{:else}<Copy size={12} />{/if}
                      </button>
                    </div>
                    <pre><code>{endpoint.body}</code></pre>
                  </div>
                {/if}
                {#if endpoint.response}
                  <div class="code-sample">
                    <div class="code-label">
                      <span>Response</span>
                      <button class="copy-btn" on:click={() => copyToClipboard(endpoint.response || '', `res-${i}-${j}`)}>
                        {#if copiedId === `res-${i}-${j}`}<Check size={12} />{:else}<Copy size={12} />{/if}
                      </button>
                    </div>
                    <pre><code>{endpoint.response}</code></pre>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .page-container { max-width: 900px; margin: 0 auto; padding: 0 1rem; }
  .page-header { margin-bottom: 1.5rem; padding-bottom: 1rem; border-bottom: 1px solid var(--border-subtle); }
  .page-title { font-size: 1.5rem; font-weight: 700; color: var(--text-primary); }
  .page-subtitle { font-size: 0.875rem; color: var(--text-tertiary); margin-top: 0.25rem; }

  .info-banner {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    padding: 1rem 1.25rem;
    margin-bottom: 1rem;
  }
  .info-title { font-size: 0.8125rem; font-weight: 600; color: var(--text-primary); margin-bottom: 0.25rem; }
  .info-text { font-size: 0.8125rem; color: var(--text-secondary); line-height: 1.6; }
  .info-text code { background: var(--bg-elevated); padding: 0.125rem 0.375rem; border-radius: 4px; font-size: 0.75rem; }
  .auth-legend { display: flex; gap: 0.75rem; margin-top: 0.75rem; flex-wrap: wrap; }
  .auth-badge {
    display: inline-flex; align-items: center; gap: 0.25rem;
    font-size: 0.6875rem; font-weight: 500; padding: 0.2rem 0.5rem; border-radius: 4px;
  }
  .auth-badge.admin { background: rgba(239,68,68,0.08); color: #f87171; }
  .auth-badge.user { background: rgba(59,130,246,0.08); color: #60a5fa; }
  .auth-badge.public { background: rgba(16,185,129,0.08); color: #34d399; }

  .base-url {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    padding: 0.75rem 1rem;
    margin-bottom: 1.5rem;
    display: flex; align-items: center; gap: 0.75rem;
  }
  .base-url-label { font-size: 0.75rem; color: var(--text-tertiary); font-weight: 500; }
  .base-url-value { font-size: 0.8125rem; color: var(--accent-primary, #34d399); }

  .sections { display: flex; flex-direction: column; gap: 0.75rem; }
  .section-card {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    overflow: hidden;
  }
  .section-header {
    width: 100%; padding: 1rem 1.25rem; text-align: left; cursor: pointer;
    background: none; border: none; color: inherit;
    display: flex; flex-direction: column; gap: 0.25rem;
  }
  .section-header:hover { background: var(--bg-elevated); }
  .section-title-group { display: flex; align-items: center; gap: 0.5rem; }
  .section-chevron { color: var(--text-tertiary); }
  .section-title { font-size: 0.9375rem; font-weight: 600; color: var(--text-primary); }
  .section-count {
    font-size: 0.6875rem; background: var(--bg-elevated); color: var(--text-tertiary);
    padding: 0.1rem 0.4rem; border-radius: 10px; font-weight: 500;
  }
  .section-desc { font-size: 0.75rem; color: var(--text-tertiary); padding-left: 1.625rem; }

  .endpoints { border-top: 1px solid var(--border-subtle); }
  .endpoint { padding: 0.875rem 1.25rem; border-bottom: 1px solid var(--border-subtle); }
  .endpoint:last-child { border-bottom: none; }
  .endpoint-header { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }
  .method-badge {
    font-size: 0.625rem; font-weight: 700; letter-spacing: 0.05em;
    padding: 0.15rem 0.4rem; border-radius: 4px; border: 1px solid;
    font-family: 'JetBrains Mono', monospace;
  }
  .endpoint-path { font-size: 0.8125rem; color: var(--text-primary); font-weight: 500; }
  .auth-indicator {
    font-size: 0.625rem; font-weight: 500; padding: 0.1rem 0.35rem; border-radius: 3px; margin-left: auto;
  }
  .auth-indicator.admin { background: rgba(239,68,68,0.08); color: #f87171; }
  .auth-indicator.user { background: rgba(59,130,246,0.08); color: #60a5fa; }
  .auth-indicator.public { background: rgba(16,185,129,0.08); color: #34d399; }
  .endpoint-desc { font-size: 0.8125rem; color: var(--text-secondary); margin-top: 0.375rem; }

  .code-sample {
    background: var(--bg-elevated);
    border-radius: 6px; margin-top: 0.5rem; overflow: hidden;
  }
  .code-label {
    display: flex; justify-content: space-between; align-items: center;
    padding: 0.375rem 0.75rem; border-bottom: 1px solid var(--border-subtle);
    font-size: 0.6875rem; color: var(--text-tertiary);
  }
  .copy-btn {
    background: none; border: none; color: var(--text-tertiary); cursor: pointer;
    padding: 0.2rem; border-radius: 3px;
  }
  .copy-btn:hover { color: var(--text-primary); background: var(--bg-surface); }
  .code-sample pre {
    padding: 0.5rem 0.75rem; margin: 0; overflow-x: auto;
    font-size: 0.75rem; color: var(--text-secondary); line-height: 1.5;
  }
</style>
