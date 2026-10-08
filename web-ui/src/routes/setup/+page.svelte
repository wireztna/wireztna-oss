<script lang="ts">
  import { auth } from '$lib/api/client';
  import { authStore } from '$lib/stores/auth';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  let email = '';
  let password = '';
  let confirm = '';
  let error = '';
  let loading = false;
  let checking = true;

  // Only show the first-run screen when no users exist yet. If setup is already
  // done, send the operator to the normal login page.
  onMount(async () => {
    try {
      const config = await auth.getOIDCConfig();
      if (!config.setup_required) {
        goto('/login');
        return;
      }
    } catch {
      // If the config can't be read, fall through and let POST /auth/setup
      // return its own 400 "Setup already completed" if that's the case.
    } finally {
      checking = false;
    }
  });

  async function handleSetup() {
    error = '';
    if (password !== confirm) {
      error = 'Passwords do not match';
      return;
    }
    loading = true;
    try {
      const response = await auth.setup(email, password);
      authStore.login(response.access_token);
      goto('/dashboard');
    } catch (e: any) {
      // Fallback: setup already completed between page load and submit.
      if (e.message && e.message.toLowerCase().includes('setup already completed')) {
        goto('/login');
        return;
      }
      error = e.message || 'Setup failed';
    } finally {
      loading = false;
    }
  }
</script>

<div class="login-page">
  <div class="login-card">
    <div class="brand">
      <div class="brand-icon">
        <img src="/img/logo-wireztna-lg.png" alt="WireZTNA" style="width: 48px; height: auto;" />
      </div>
      <h1>WireZTNA</h1>
      <p>Create the first administrator</p>
    </div>

    {#if checking}
      <p class="checking">Checking setup status…</p>
    {:else}
      <form on:submit|preventDefault={handleSetup}>
        <div class="field">
          <label for="email">Admin email</label>
          <input id="email" type="email" bind:value={email} placeholder="admin@example.com" required autofocus />
        </div>
        <div class="field">
          <label for="password">Password</label>
          <input id="password" type="password" bind:value={password} placeholder="Choose a strong password" required />
        </div>
        <div class="field">
          <label for="confirm">Confirm password</label>
          <input id="confirm" type="password" bind:value={confirm} placeholder="Re-enter password" required />
        </div>

        {#if error}
          <div class="error">{error}</div>
        {/if}

        <button type="submit" class="login-btn" disabled={loading || !email || !password || !confirm}>
          {loading ? 'Creating…' : 'Create admin & continue'}
        </button>
      </form>
    {/if}
  </div>

  <div class="login-footer">
    <span>Secured by WireGuard</span>
  </div>
</div>

<style>
  .login-page {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-direction: column;
    background: var(--bg-primary);
    position: relative;
    overflow: hidden;
  }

  .login-page::before {
    content: '';
    position: absolute;
    top: -50%;
    left: -50%;
    width: 200%;
    height: 200%;
    background: radial-gradient(ellipse at 30% 20%, var(--accent-primary-dim) 0%, transparent 50%),
                radial-gradient(ellipse at 70% 80%, rgba(52, 211, 153, 0.05) 0%, transparent 50%);
    pointer-events: none;
  }

  .login-card {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    padding: 2.5rem;
    border-radius: var(--radius-lg);
    width: 100%;
    max-width: 380px;
    position: relative;
    z-index: 1;
    box-shadow: var(--shadow-lg);
  }

  .brand {
    text-align: center;
    margin-bottom: 2rem;
  }

  .brand-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 56px;
    height: 56px;
    border-radius: 14px;
    margin-bottom: 1rem;
  }

  .brand h1 {
    margin: 0;
    font-size: 1.5rem;
    font-weight: 700;
    color: var(--text-primary);
  }

  .brand p {
    color: var(--text-secondary);
    margin: 0.25rem 0 0;
    font-size: 0.875rem;
  }

  .checking {
    text-align: center;
    color: var(--text-secondary);
    font-size: 0.875rem;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }

  .field label {
    font-size: 0.8rem;
    font-weight: 500;
    color: var(--text-secondary);
  }

  .field input {
    padding: 0.7rem 0.875rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-size: 0.9rem;
    font-family: inherit;
    transition: border-color var(--transition), box-shadow var(--transition);
  }

  .field input:focus {
    outline: none;
    border-color: var(--accent-primary);
    box-shadow: 0 0 0 3px var(--accent-primary-dim);
  }

  .field input::placeholder {
    color: var(--text-muted);
  }

  .login-btn {
    padding: 0.75rem;
    background: linear-gradient(135deg, var(--accent-primary) 0%, var(--accent-primary-strong) 100%);
    color: white;
    border: none;
    border-radius: var(--radius-sm);
    font-size: 0.9rem;
    font-weight: 600;
    font-family: inherit;
    cursor: pointer;
    box-shadow: var(--shadow-sm);
    transition: all 0.2s ease;
    margin-top: 0.5rem;
  }

  .login-btn:hover:not(:disabled) {
    box-shadow: var(--shadow-md);
    transform: translateY(-1px);
  }

  .login-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .error {
    background: var(--accent-red-dim);
    color: var(--accent-red);
    padding: 0.6rem 0.875rem;
    border-radius: var(--radius-sm);
    font-size: 0.85rem;
    border: 1px solid rgba(248, 113, 113, 0.2);
  }

  .login-footer {
    margin-top: 2rem;
    color: var(--text-muted);
    font-size: 0.75rem;
    position: relative;
    z-index: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.4rem;
  }
</style>
