<script lang="ts">
  import { auth, mfa } from '$lib/api/client';
  import { authStore } from '$lib/stores/auth';
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';

  let username = '';
  let password = '';
  let otpCode = '';
  let mfaCode = '';
  let mfaToken = '';
  let error = '';
  let loading = false;
  let oidcEnabled = false;
  let oidcLoading = false;
  // Email/OTP availability — false when the broker has no SMTP configured (CE default).
  let emailEnabled = true;

  // OTP flow state
  type LoginStep = 'email' | 'otp_code' | 'password' | 'mfa';
  let step: LoginStep = 'email';
  let emailHint = '';
  let expiresIn = 300;
  let usePassword = false;

  // Check if OIDC/SSO is enabled on mount
  onMount(async () => {
    try {
      const config = await auth.getOIDCConfig();
      oidcEnabled = config.enabled;
      // email_enabled/otp_enabled both reflect SMTP being configured.
      emailEnabled = config.email_enabled ?? config.otp_enabled ?? true;
      // First-run: no users yet — send the admin to the create-first-admin screen.
      if (config.setup_required) {
        goto('/setup');
        return;
      }
      // No email means no OTP: start password-first and hide the email-code path.
      if (!emailEnabled) {
        usePassword = true;
        step = 'password';
      }
    } catch {
      oidcEnabled = false;
    }

    // Handle OIDC callback: if URL has ?token= parameter, it's a redirect back from Microsoft
    const params = $page.url.searchParams;
    const callbackToken = params.get('token');
    if (callbackToken) {
      authStore.login(callbackToken);
      const payload = JSON.parse(atob(callbackToken.split('.')[1]));
      if (payload.is_admin) {
        goto('/dashboard');
      } else {
        goto('/portal');
      }
      return;
    }

    // Handle auto-login from onboarding email: ?auto_email=...&auto_pass=...
    const autoEmail = params.get('auto_email');
    const autoPass = params.get('auto_pass');
    if (autoEmail && autoPass) {
      username = autoEmail;
      password = autoPass;
      loading = true;
      try {
        const response = await auth.login(autoEmail, autoPass);
        if (response.mfa_required) {
          mfaToken = response.access_token;
          step = 'mfa';
        } else {
          completeLogin(response.access_token, response.password_change_required);
        }
      } catch (e: any) {
        // Auto-login failed (password already changed, or expired) — show normal login with email pre-filled
        error = 'Auto-login failed — please sign in manually';
        step = 'email';
      } finally {
        loading = false;
      }
      return;
    }
  });

  async function handleRequestOTP() {
    error = '';
    loading = true;
    try {
      const response = await auth.otpRequest(username);
      emailHint = response.email_hint || '';
      expiresIn = response.expires_in;
      step = 'otp_code';
    } catch (e: any) {
      error = e.message || 'Failed to request code';
    } finally {
      loading = false;
    }
  }

  async function handleVerifyOTP() {
    error = '';
    loading = true;
    try {
      const response = await auth.otpVerify(username, otpCode);
      if (response.mfa_required) {
        mfaToken = response.access_token;
        step = 'mfa';
      } else {
        completeLogin(response.access_token, response.password_change_required);
      }
    } catch (e: any) {
      error = e.message || 'Invalid code';
    } finally {
      loading = false;
    }
  }

  async function handlePasswordLogin() {
    error = '';
    loading = true;
    try {
      const response = await auth.login(username, password);
      if (response.mfa_required) {
        mfaToken = response.access_token;
        step = 'mfa';
      } else {
        completeLogin(response.access_token, response.password_change_required);
      }
    } catch (e: any) {
      error = e.message || 'Login failed';
    } finally {
      loading = false;
    }
  }

  async function handleMFAVerify() {
    error = '';
    loading = true;
    try {
      const response = await mfa.verify(mfaToken, mfaCode);
      completeLogin(response.access_token, response.password_change_required);
    } catch (e: any) {
      error = e.message || 'Invalid authenticator code';
    } finally {
      loading = false;
    }
  }

  function completeLogin(token: string, passwordChangeRequired: boolean = false) {
    authStore.login(token);
    if (passwordChangeRequired) {
      goto('/portal?change_password=1');
      return;
    }
    const payload = JSON.parse(atob(token.split('.')[1]));
    if (payload.is_admin) {
      goto('/dashboard');
    } else {
      goto('/portal');
    }
  }

  function switchToPassword() {
    usePassword = true;
    step = 'password';
    error = '';
  }

  function switchToOTP() {
    usePassword = false;
    step = 'email';
    error = '';
    otpCode = '';
  }

  function goBack() {
    step = 'email';
    error = '';
    otpCode = '';
  }

  async function resendCode() {
    error = '';
    loading = true;
    try {
      const response = await auth.otpRequest(username);
      emailHint = response.email_hint || '';
      expiresIn = response.expires_in;
      error = '';
    } catch (e: any) {
      error = e.message || 'Failed to resend code';
    } finally {
      loading = false;
    }
  }

  function handleSSOLogin() {
    oidcLoading = true;
    window.location.href = '/api/v1/auth/oidc/login';
  }
</script>

<div class="login-page">
  <div class="login-card">
    <div class="brand">
      <div class="brand-icon">
        <img src="/img/logo-wireztna-lg.png" alt="WireZTNA" style="width: 48px; height: auto;" />
      </div>
      <h1>WireZTNA</h1>
      <p>Zero Trust Network Access</p>
    </div>

    {#if oidcEnabled}
      <button class="sso-btn" on:click={handleSSOLogin} disabled={oidcLoading}>
        <svg class="ms-icon" viewBox="0 0 21 21" xmlns="http://www.w3.org/2000/svg">
          <rect x="1" y="1" width="9" height="9" fill="#f25022"/>
          <rect x="11" y="1" width="9" height="9" fill="#7fba00"/>
          <rect x="1" y="11" width="9" height="9" fill="#00a4ef"/>
          <rect x="11" y="11" width="9" height="9" fill="#ffb900"/>
        </svg>
        {oidcLoading ? 'Redirecting...' : 'Sign in with Microsoft'}
      </button>

      <div class="divider">
        <span>or</span>
      </div>
    {/if}

    {#if step === 'email'}
      <!-- Step 1: Enter email -->
      <form on:submit|preventDefault={handleRequestOTP}>
        <div class="field">
          <label for="email">Email</label>
          <input id="email" type="email" bind:value={username} placeholder="Enter your email" required autofocus />
        </div>

        {#if error}
          <div class="error">{error}</div>
        {/if}

        <button type="submit" class="login-btn" disabled={loading || !username}>
          {loading ? 'Sending code...' : 'Send access code'}
        </button>
      </form>

      <div class="alt-method">
        <button class="link-btn" on:click={switchToPassword}>Sign in with password</button>
      </div>

    {:else if step === 'otp_code'}
      <!-- Step 2: Enter OTP code -->
      <div class="otp-info">
        {#if emailHint}
          <p class="otp-sent">Code sent to <strong>{emailHint}</strong></p>
        {:else}
          <p class="otp-sent">If the account exists, a code has been sent</p>
        {/if}
        <p class="otp-expiry">Valid for {Math.floor(expiresIn / 60)} minutes</p>
      </div>

      <form on:submit|preventDefault={handleVerifyOTP}>
        <div class="field">
          <label for="otp">Access code</label>
          <input
            id="otp"
            type="text"
            bind:value={otpCode}
            placeholder="Enter 6-digit code"
            required
            autofocus
            autocomplete="one-time-code"
            inputmode="numeric"
            maxlength="6"
            class="otp-input"
          />
        </div>

        {#if error}
          <div class="error">{error}</div>
        {/if}

        <button type="submit" class="login-btn" disabled={loading || otpCode.length < 4}>
          {loading ? 'Verifying...' : 'Verify'}
        </button>
      </form>

      <div class="otp-actions">
        <button class="link-btn" on:click={resendCode} disabled={loading}>Resend code</button>
        <span class="sep">·</span>
        <button class="link-btn" on:click={goBack}>Change email</button>
      </div>

    {:else if step === 'password'}
      <!-- Password login (legacy) -->
      <form on:submit|preventDefault={handlePasswordLogin}>
        <div class="field">
          <label for="email-pw">Email</label>
          <input id="email-pw" type="email" bind:value={username} placeholder="Enter your email" required />
        </div>
        <div class="field">
          <label for="password">Password</label>
          <input id="password" type="password" bind:value={password} placeholder="Enter your password" required />
        </div>

        {#if error}
          <div class="error">{error}</div>
        {/if}

        <button type="submit" class="login-btn" disabled={loading}>
          {loading ? 'Signing in...' : 'Sign In'}
        </button>
      </form>

      {#if emailEnabled}
        <div class="alt-method">
          <button class="link-btn" on:click={switchToOTP}>Sign in with email code</button>
        </div>
      {/if}

    {:else if step === 'mfa'}
      <!-- Step: MFA (authenticator app) -->
      <div class="mfa-info">
        <p class="mfa-prompt">Enter the 6-digit code from your authenticator app</p>
      </div>

      <form on:submit|preventDefault={handleMFAVerify}>
        <div class="field">
          <label for="mfa-code">Authenticator code</label>
          <input
            id="mfa-code"
            type="text"
            bind:value={mfaCode}
            placeholder="000000"
            required
            autofocus
            inputmode="numeric"
            maxlength="6"
            autocomplete="one-time-code"
            class="otp-input"
          />
        </div>

        {#if error}
          <div class="error">{error}</div>
        {/if}

        <button type="submit" class="login-btn" disabled={loading || mfaCode.length < 6}>
          {loading ? 'Verifying...' : 'Verify'}
        </button>
      </form>

      <div class="alt-method">
        <button class="link-btn" on:click={goBack}>Cancel</button>
      </div>
    {/if}
  </div>

  <div class="login-footer">
    <span>Secured by WireGuard</span>
    <span class="footer-sep">·</span>
    <a href="mailto:support@wireztna.com" class="footer-support">Support</a>
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

  /* ─── SSO Button ─── */
  .sso-btn {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.75rem;
    padding: 0.75rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
    font-size: 0.9rem;
    font-weight: 500;
    font-family: inherit;
    cursor: pointer;
    transition: all 0.2s ease;
  }

  .sso-btn:hover:not(:disabled) {
    border-color: var(--border-default);
    background: var(--bg-surface);
    box-shadow: var(--shadow-sm);
  }

  .sso-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .ms-icon {
    width: 20px;
    height: 20px;
    flex-shrink: 0;
  }

  /* ─── Divider ─── */
  .divider {
    display: flex;
    align-items: center;
    margin: 1.5rem 0;
    gap: 0.75rem;
  }

  .divider::before,
  .divider::after {
    content: '';
    flex: 1;
    height: 1px;
    background: var(--border-subtle);
  }

  .divider span {
    font-size: 0.75rem;
    color: var(--text-muted);
    white-space: nowrap;
  }

  /* ─── Form ─── */
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
    background: linear-gradient(135deg, var(--accent-primary) 0%, var(--accent-primary-strong) 100%);
    box-shadow: var(--shadow-md);
    transform: translateY(-1px);
  }

  .login-btn:active:not(:disabled) {
    transform: translateY(0);
    box-shadow: var(--shadow-xs);
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

  .footer-sep {
    color: var(--text-muted);
    opacity: 0.5;
  }

  .footer-support {
    color: var(--text-muted);
    text-decoration: none;
    transition: color 0.15s ease;
  }

  .footer-support:hover {
    color: var(--accent-blue);
  }

  /* ─── OTP specific ─── */
  .otp-info {
    text-align: center;
    margin-bottom: 1.5rem;
  }

  .otp-sent {
    color: var(--text-secondary);
    font-size: 0.875rem;
    margin: 0 0 0.25rem;
  }

  .otp-sent strong {
    color: var(--text-primary);
  }

  .otp-expiry {
    color: var(--text-muted);
    font-size: 0.75rem;
    margin: 0;
  }

  .otp-input {
    text-align: center;
    font-size: 1.5rem !important;
    letter-spacing: 0.5rem;
    font-weight: 600;
  }

  .otp-actions {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    margin-top: 1rem;
  }

  .otp-actions .sep {
    color: var(--text-muted);
    font-size: 0.75rem;
  }

  /* ─── Alt method / link button ─── */
  .alt-method {
    text-align: center;
    margin-top: 1rem;
  }

  .link-btn {
    background: none;
    border: none;
    color: var(--accent-primary);
    font-size: 0.8rem;
    font-family: inherit;
    cursor: pointer;
    padding: 0.25rem 0.5rem;
    border-radius: var(--radius-sm);
    transition: background 0.15s ease;
  }

  .link-btn:hover:not(:disabled) {
    background: var(--accent-primary-dim);
  }

  .link-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .mfa-info {
    text-align: center;
    margin-bottom: 1.5rem;
  }

  .mfa-prompt {
    color: var(--text-secondary);
    font-size: 0.875rem;
    margin: 0;
  }
</style>
