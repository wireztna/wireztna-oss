<script lang="ts">
  import { onMount } from 'svelte';
  import {
    Activity, AppWindow, ArrowRight, Check, CheckCircle2, ChevronRight, CircleAlert,
    CircleGauge, Cloud, Database, Download, Globe2, Languages, Laptop, LayoutGrid,
    LockKeyhole, Network, Power, RefreshCw, Route, Search, Server, Settings, ShieldCheck,
    SlidersHorizontal, TerminalSquare, Wifi, WifiOff, X,
  } from '@lucide/svelte';
  import { isValidEnrollmentUrl, isValidOtp, type Command, type ConnectionStep, type DesktopIpcClient, type DesktopScenarioClient, type DesktopStatus, type DesiredState, type Scenario } from './lib/ipc/contract';
  import { DesktopAuthError, DesktopEnrollmentError, type DesktopAuthClient, type DesktopAuthErrorCode, type DesktopEnrollmentClient, type DesktopEnrollmentErrorCode, type ShellClient, type ShellEvent, type TrayVisualState } from './lib/shell/contract';
  import { deriveTrayVisualState } from './lib/tray-state';
  import { exitNodeForProject, exitNodesForProject, firstExitNodeForProject } from './lib/selection';
  import { createDesktopStore } from './lib/stores/desktop';
  import { deriveDegradedHeadline } from './lib/degraded-headline';
  import { ipcErrorPresentation, translate, type Locale, type MessageKey } from './lib/i18n';

  export let desktopIpcClient: DesktopIpcClient;
  export let desktopAuthClient: DesktopAuthClient;
  export let desktopEnrollmentClient: DesktopEnrollmentClient;
  export let shellClient: ShellClient;
  export let scenarioClient: DesktopScenarioClient | undefined = undefined;

  type View = 'connection' | 'resources' | 'activity' | 'diagnostics' | 'settings';
  type OnboardingStep = 'enroll' | 'email' | 'otp' | 'complete';

  const desktop = createDesktopStore(desktopIpcClient, scenarioClient ?? null);

  let activeView: View = 'connection';
  let locale: Locale = 'en';
  let enrollmentUrl = '';
  let enrollmentRequired = false;
  let authEmail = '';
  let otpSentTo = '';
  let otp = '';
  let formError = '';
  let authBusy = false;
  let onboardingStep: OnboardingStep = 'enroll';
  let onboardingComplete = false;
  let launchAtLogin = true;
  let autoConnect = false;
  let notifications = false;
  let reducedMotion = false;
  let checksRunning = false;
  let diagnosticsNotice = '';
  let enrollmentInput: HTMLInputElement;
  let authEmailInput: HTMLInputElement;
  let otpInput: HTMLInputElement;
  let confirmationHeading: HTMLHeadingElement;
  let selectedGroupId = '';
  let selectedExitNodeId: string | null = null;
  let selectionBaseline = '';
  let lastTrayVisualState: TrayVisualState | null = null;
  let queuedTrayVisualState: TrayVisualState | null = null;
  let trayFallbackToAttention = false;
  let trayRetryBlocked = false;
  let trayUpdateUnavailable = false;
  let trayRetryTimer: ReturnType<typeof setTimeout> | null = null;
  let trayStateUpdates: Promise<void> = Promise.resolve();

  const nav: { id: View; key: MessageKey; icon: typeof ShieldCheck }[] = [
    { id: 'connection', key: 'connection', icon: ShieldCheck },
    { id: 'resources', key: 'resources', icon: LayoutGrid },
    { id: 'activity', key: 'activity', icon: Activity },
    { id: 'diagnostics', key: 'diagnostics', icon: CircleGauge },
    { id: 'settings', key: 'settings', icon: Settings },
  ];

  const statusKeys: Record<DesktopStatus, MessageKey> = {
    setup_required: 'setup_required', auth_required: 'auth_required', update_required: 'update_required',
    disconnected: 'disconnected', connecting: 'connecting', connected: 'connected', reconnecting: 'reconnecting',
    degraded: 'degraded', service_unavailable: 'service_unavailable',
  };

  const steps: ConnectionStep[] = [
    'authenticating', 'requesting_access', 'configuring_wireguard', 'configuring_routes',
    'configuring_dns', 'verifying_connection', 'connected',
  ];

  const stepNames = {
    en: {
      authenticating: 'Authenticating', requesting_access: 'Requesting access', configuring_wireguard: 'Configuring WireGuard',
      configuring_routes: 'Configuring routes', configuring_dns: 'Configuring private DNS', verifying_connection: 'Verifying connection', connected: 'Connected',
    },
    es: {
      authenticating: 'Autenticando', requesting_access: 'Solicitando acceso', configuring_wireguard: 'Configurando WireGuard',
      configuring_routes: 'Configurando rutas', configuring_dns: 'Configurando DNS privado', verifying_connection: 'Verificando conexión', connected: 'Conectado',
    },
  } as const;

  const scenarioOptions: { value: Scenario; en: string; es: string }[] = [
    { value: 'setup_required', en: 'Setup required', es: 'Configuración requerida' },
    { value: 'auth_required', en: 'Authentication required', es: 'Autenticación requerida' },
    { value: 'update_required', en: 'Update required', es: 'Actualización requerida' },
    { value: 'disconnected', en: 'Disconnected', es: 'Desconectado' },
    { value: 'connecting', en: 'Connecting with progress', es: 'Conectando con progreso' },
    { value: 'connected_split', en: 'Connected · split', es: 'Conectado · dividido' },
    { value: 'connected_vpn', en: 'Connected · VPN', es: 'Conectado · VPN' },
    { value: 'reconnecting', en: 'Reconnecting', es: 'Reconectando' },
    { value: 'degraded', en: 'Degraded · DNS', es: 'Degradado · DNS' },
    { value: 'service_unavailable', en: 'Service unavailable', es: 'Servicio no disponible' },
  ];

  const tx = (key: MessageKey) => translate(locale, key);
  const appVersion = __APP_VERSION__;
  const appVersionMarker = `wireztna-about-version:${appVersion}`;

  $: snapshot = $desktop.snapshot;
  $: status = enrollmentRequired
    ? 'setup_required'
    : $desktop.lastError === 'REAUTH_REQUIRED'
      ? 'auth_required'
      : snapshot?.status ?? 'service_unavailable';
  $: trayVisualState = deriveTrayVisualState(
    snapshot?.status ?? null,
    snapshot?.health.healthy === true,
    Boolean($desktop.lastError),
  );
  $: requestedTrayVisualState = trayFallbackToAttention ? 'attention' : trayVisualState;
  $: if (!trayRetryBlocked
    && requestedTrayVisualState !== lastTrayVisualState
    && requestedTrayVisualState !== queuedTrayVisualState) {
    queueTrayVisualState(requestedTrayVisualState);
  }
  $: nextSelectionBaseline = snapshot
    ? `${snapshot.catalog?.configurationId ?? ''}:${snapshot.sequence}:${snapshot.desired.groupId}:${snapshot.desired.exitNodeId ?? ''}`
    : '';
  $: if (snapshot && selectionBaseline !== nextSelectionBaseline) {
    selectionBaseline = nextSelectionBaseline;
    selectedGroupId = snapshot.desired.groupId || snapshot.projectId;
    selectedExitNodeId = snapshot.desired.exitNodeId ?? snapshot.exitNodeId;
  }
  $: availableExitNodes = snapshot
    ? exitNodesForProject(snapshot.projects, snapshot.exitNodes, selectedGroupId)
    : [];
  $: if (selectedExitNodeId && !availableExitNodes.some((node) => node.id === selectedExitNodeId)) {
    selectedExitNodeId = null;
  }
  $: currentProject = snapshot?.projects.find((project) => project.id === selectedGroupId);
  $: currentExitNode = availableExitNodes.find((node) => node.id === selectedExitNodeId);
  $: selectedMode = selectedExitNodeId ? 'vpn' : 'split';
  $: connectBlocked = Boolean(snapshot && status !== 'connected' && status !== 'degraded'
    && (snapshot.projects.length === 0 || !selectedGroupId));
  $: connectGuidance = snapshot?.projects.length === 0
    ? (locale === 'en' ? 'No authorized projects are available. Ask your administrator to grant access.' : 'No hay proyectos autorizados disponibles. Solicita acceso a tu administrador.')
    : (locale === 'en' ? 'Select a project before connecting.' : 'Selecciona un proyecto antes de conectarte.');
  $: desktopError = $desktop.lastError
    ? (() => {
      const presentation = ipcErrorPresentation(locale, $desktop.lastError);
      const terminalDetail = $desktop.lastControllerError?.detail?.trim();
      return terminalDetail ? { ...presentation, message: terminalDetail } : presentation;
    })()
    : null;
  $: degradedHeadline = snapshot
    ? deriveDegradedHeadline(locale, snapshot.health, $desktop.lastControllerError)
    : tx('degradedIssue');
  $: showOnboarding = status === 'setup_required' || status === 'auth_required' || onboardingComplete;
  $: if (status === 'auth_required' && onboardingStep === 'enroll') onboardingStep = 'email';
  $: document.documentElement.lang = locale;
  $: document.documentElement.dataset.motion = reducedMotion ? 'reduced' : 'full';

  function queueTrayVisualState(target: TrayVisualState): void {
    queuedTrayVisualState = target;
    trayStateUpdates = trayStateUpdates
      .then(async () => {
        if (target !== requestedTrayVisualState) return;
        await shellClient.setTrayVisualState(target);
        lastTrayVisualState = target;
        trayUpdateUnavailable = false;
        if (trayFallbackToAttention) trayFallbackToAttention = false;
      })
      .catch(() => {
        lastTrayVisualState = null;
        trayUpdateUnavailable = true;
        trayFallbackToAttention = true;
        trayRetryBlocked = true;
        if (trayRetryTimer !== null) clearTimeout(trayRetryTimer);
        trayRetryTimer = setTimeout(() => {
          trayRetryTimer = null;
          trayRetryBlocked = false;
        }, 1_000);
      })
      .finally(() => {
        if (queuedTrayVisualState === target) queuedTrayVisualState = null;
      });
  }

  onMount(() => {
    let mounted = true;
    let unsubscribeShell: () => void = () => undefined;
    const handleShellEvent = (event: ShellEvent) => {
      if (event.type === 'visibility_changed') {
        document.documentElement.dataset.shellVisibility = event.visible ? 'visible' : 'hidden';
      } else if (event.type === 'deep_link_received' && isValidEnrollmentUrl(event.url)) {
        enrollmentUrl = event.url;
      }
    };
    void shellClient.subscribe(handleShellEvent).then((unsubscribe) => {
      if (mounted) unsubscribeShell = unsubscribe;
      else unsubscribe();
    }).catch(() => undefined);
    void (async () => {
      try {
        const enrollmentContext = await desktopEnrollmentClient.context();
        if (!mounted) return;
        enrollmentRequired = !enrollmentContext.enrolled;
        if (enrollmentContext.email) authEmail = enrollmentContext.email;
      } catch (error) {
        if (!mounted) return;
        enrollmentRequired = true;
        formError = enrollmentError(error);
      }
      if (enrollmentRequired) {
        requestAnimationFrame(() => enrollmentInput?.focus());
        return;
      }
      try {
        const authContext = await desktopAuthClient.context();
        if (mounted && authContext.email) authEmail = authContext.email;
      } catch {
        // Authentication context is optional until the service requests a new session.
      }
      await desktop.initialize();
      if (!mounted) return;
      requestAnimationFrame(() => {
        if ($desktop.lastError === 'REAUTH_REQUIRED') authEmailInput?.focus();
        else enrollmentInput?.focus();
      });
    })();
    return () => {
      mounted = false;
      unsubscribeShell();
      if (trayRetryTimer !== null) clearTimeout(trayRetryTimer);
      desktop.dispose();
    };
  });

  function statusLabel(value: DesktopStatus): string {
    return tx(statusKeys[value]);
  }

  function statusTone(value: DesktopStatus): string {
    if (value === 'connected') return 'positive';
    if (value === 'connecting' || value === 'reconnecting') return 'progress';
    if (value === 'degraded' || value === 'auth_required' || value === 'setup_required') return 'warning';
    if (value === 'service_unavailable') return 'danger';
    return 'neutral';
  }

  function healthLabel(value: string): string {
    if (value === 'healthy') return tx('healthy');
    if (value === 'degraded') return tx('degradedValue');
    if (value === 'unavailable') return tx('unavailableValue');
    if (value === 'stale') return tx('staleValue');
    return value;
  }

  function activityTitle(code: string): string {
    const spanish: Record<string, string> = {
      'scenario.changed': 'escenario cambiado', 'device.enrolled': 'dispositivo inscrito',
      'auth.completed': 'autenticación completada', 'connection.connecting': 'conexión iniciada',
      'connection.reconnecting': 'reconexión iniciada', 'connection.connected': 'conexión verificada',
      'connection.disconnected': 'conexión cerrada', 'project.changed': 'proyecto cambiado',
      'project.preference_saved': 'preferencia de proyecto guardada', 'mode.changed': 'modo cambiado',
      'mode.preference_saved': 'preferencia de modo guardada', 'service.restored': 'servicio restaurado',
      'diagnostics.completed': 'diagnóstico completado',
    };
    return locale === 'es' ? spanish[code] ?? code.replaceAll('.', ' ') : code.replaceAll('.', ' ');
  }

  function activityDetail(code: string, fallback: string): string {
    if (locale === 'en') return fallback;
    if (code === 'scenario.changed') return fallback.replace('Mock scenario:', 'Escenario mock:');
    if (code.startsWith('project.')) return fallback.replace('Project:', 'Proyecto:');
    if (code.startsWith('mode.')) return fallback === 'vpn' ? 'VPN privada' : 'Túnel dividido';
    const spanish: Record<string, string> = {
      'device.enrolled': 'Identidad del dispositivo creada por el servicio local',
      'auth.completed': 'Sesión autorizada', 'connection.connecting': 'Conexión solicitada',
      'connection.reconnecting': 'Aplicando cambios de conexión', 'connection.connected': 'Handshake verificado',
      'connection.disconnected': 'Desconectado por el usuario', 'service.restored': 'El servicio local está disponible',
      'diagnostics.completed': 'Cuatro comprobaciones locales completadas',
    };
    return spanish[code] ?? fallback;
  }

  function sendSafely(command: Command): void {
    void desktop.send(command).catch(() => undefined);
  }

  function closeOnboarding(): void {
    onboardingComplete = false;
    requestAnimationFrame(() => document.getElementById('primary-connection-action')?.focus());
  }

  function trapModalFocus(event: KeyboardEvent): void {
    if (event.key !== 'Tab') return;
    const dialog = event.currentTarget as HTMLElement;
    const focusable = [...dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex="0"]')];
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const active = document.activeElement as HTMLElement | null;
    if (event.shiftKey && (active === first || !active || !focusable.includes(active))) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (active === last || !active || !focusable.includes(active))) {
      event.preventDefault();
      first.focus();
    }
  }

  function desiredForConnect(): DesiredState | null {
    if (!snapshot || !selectedGroupId || !snapshot.projects.some((project) => project.id === selectedGroupId)) return null;
    const exitNodeId = exitNodeForProject(snapshot.projects, snapshot.exitNodes, selectedGroupId, selectedExitNodeId);
    return {
      ...snapshot.desired,
      groupId: selectedGroupId,
      exitNodeId,
      connected: true,
    };
  }

  function enrollmentError(error: unknown): string {
    const code: DesktopEnrollmentErrorCode = error instanceof DesktopEnrollmentError
      ? error.code
      : 'ENROLLMENT_HELPER_FAILED';
    if (code === 'ENROLLMENT_INVALID_URL') return tx('urlError');
    if (code === 'ENROLLMENT_REQUEST_FAILED') {
      return locale === 'en'
        ? 'Enrollment could not reach the control plane or the token was rejected. You can safely retry the same URL.'
        : 'El enrollment no pudo contactar con el control plane o el token fue rechazado. Puedes reintentar de forma segura con la misma URL.';
    }
    if (code === 'ENROLLMENT_RESPONSE_INVALID') {
      return locale === 'en'
        ? 'The control plane returned an invalid device configuration. Nothing unsafe was saved.'
        : 'El control plane devolvió una configuración de dispositivo no válida. No se guardó ningún dato inseguro.';
    }
    if (code === 'ENROLLMENT_SAVE_FAILED' || code === 'ENROLLMENT_KEY_FAILED' || code === 'ENROLLMENT_CONFIG_UNAVAILABLE') {
      return locale === 'en'
        ? 'WireZTNA could not persist the device configuration securely. Check your account permissions and retry the same URL.'
        : 'WireZTNA no pudo guardar de forma segura la configuración del dispositivo. Revisa los permisos de tu cuenta y reintenta la misma URL.';
    }
    return locale === 'en'
      ? `WireZTNA could not start enrollment. Diagnostic code: ${code}`
      : `WireZTNA no pudo iniciar el enrollment. Código de diagnóstico: ${code}`;
  }

  async function handleEnroll(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    formError = '';
    if (!isValidEnrollmentUrl(enrollmentUrl)) {
      formError = tx('urlError');
      return;
    }
    authBusy = true;
    try {
      const context = await desktopEnrollmentClient.enroll(enrollmentUrl);
      if (!context.enrolled || !context.email) throw new DesktopEnrollmentError('ENROLLMENT_HELPER_FAILED');
      enrollmentRequired = false;
      authEmail = context.email;
      onboardingStep = 'email';
      requestAnimationFrame(() => authEmailInput?.focus());
    } catch (error) {
      formError = enrollmentError(error);
    } finally {
      authBusy = false;
    }
  }

  function authenticationError(error: unknown): string {
    const code: DesktopAuthErrorCode = error instanceof DesktopAuthError
      ? error.code
      : 'AUTH_HELPER_FAILED';
    if (code === 'AUTH_INVALID_EMAIL') return tx('emailError');
    if (code === 'AUTH_INVALID_OTP') return tx('authError');
    if (code === 'AUTH_REQUEST_FAILED') {
      return locale === 'en'
        ? 'The code could not be sent. Check your email address and try again.'
        : 'No se pudo enviar el código. Comprueba tu dirección de email y vuelve a intentarlo.';
    }
    if (code === 'AUTH_VERIFY_FAILED' || code === 'AUTH_TOKEN_INVALID') {
      return locale === 'en'
        ? 'The code is invalid or expired. Request a new code and try again.'
        : 'El código no es válido o ha caducado. Solicita uno nuevo y vuelve a intentarlo.';
    }
    if (code === 'AUTH_MFA_REQUIRED') {
      return locale === 'en'
        ? 'This account requires an authenticator code, which this desktop build does not support yet. No partial session was saved.'
        : 'Esta cuenta requiere un código de autenticador, todavía no compatible con esta versión de escritorio. No se guardó ninguna sesión parcial.';
    }
    if (code === 'AUTH_TOKEN_SAVE_FAILED') {
      return locale === 'en'
        ? 'WireZTNA could not save the session securely. Check your account permissions.'
        : 'WireZTNA no pudo guardar la sesión de forma segura. Revisa los permisos de tu cuenta.';
    }
    if (code === 'AUTH_CONFIG_UNAVAILABLE') {
      return locale === 'en'
        ? 'WireZTNA account configuration is unavailable. Reinstall the client or contact your administrator.'
        : 'La configuración de la cuenta de WireZTNA no está disponible. Reinstala el cliente o contacta con tu administrador.';
    }
    return locale === 'en'
      ? `WireZTNA could not start authentication. Diagnostic code: ${code}`
      : `WireZTNA no pudo iniciar la autenticación. Código de diagnóstico: ${code}`;
  }

  async function handleOtpRequest(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    formError = '';
    const email = authEmail.trim();
    if (!email || !authEmailInput?.checkValidity()) {
      formError = tx('emailError');
      return;
    }
    authBusy = true;
    try {
      await desktopAuthClient.requestOtp(email);
      authEmail = email;
      otpSentTo = email;
      otp = '';
      onboardingStep = 'otp';
      requestAnimationFrame(() => otpInput?.focus());
    } catch (error) {
      formError = authenticationError(error);
    } finally {
      authBusy = false;
    }
  }

  async function initializeEnrolledService(): Promise<boolean> {
    for (let attempt = 0; attempt < 20; attempt += 1) {
      await desktop.initialize(true);
      if ($desktop.snapshot && !$desktop.lastError) return true;
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    return false;
  }

  async function handleOtp(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    formError = '';
    if (!otpSentTo || !isValidOtp(otp)) {
      formError = tx('authError');
      return;
    }
    authBusy = true;
    try {
      await desktopAuthClient.verifyOtp(otpSentTo, otp);
      if (!await initializeEnrolledService()) {
        formError = tx('connectionError');
        return;
      }
      onboardingComplete = true;
      onboardingStep = 'complete';
      requestAnimationFrame(() => confirmationHeading?.focus());
    } catch (error) {
      formError = authenticationError(error);
    } finally {
      authBusy = false;
    }
  }

  function finishOnboarding(): void {
    closeOnboarding();
    activeView = 'connection';
  }

  function toggleConnection(): void {
    if ($desktop.busy || status === 'connecting' || status === 'reconnecting') return;
    if (status === 'connected' || status === 'degraded') {
      sendSafely({ type: 'disconnect' });
      return;
    }
    if (status === 'service_unavailable') {
      void desktop.initialize();
      return;
    }
    const desired = desiredForConnect();
    if (desired) sendSafely({ type: 'connect', desired });
  }

  function selectProject(event: Event): void {
    if (!snapshot?.uxCapabilities.projectCatalog) return;
    const groupId = (event.currentTarget as HTMLSelectElement).value;
    if (!snapshot.projects.some((project) => project.id === groupId)) return;
    const exitNodeId = exitNodeForProject(snapshot.projects, snapshot.exitNodes, groupId, selectedExitNodeId);
    selectedGroupId = groupId;
    selectedExitNodeId = exitNodeId;
    if (snapshot.applied.connected) {
      sendSafely({ type: 'switch', desired: { ...snapshot.desired, groupId, exitNodeId, connected: true } });
    }
  }

  function selectMode(event: Event): void {
    if (!snapshot?.uxCapabilities.exitNodeCatalog) return;
    const mode = (event.currentTarget as HTMLSelectElement).value as 'split' | 'vpn';
    const exitNodeId = mode === 'vpn'
      ? exitNodeForProject(snapshot.projects, snapshot.exitNodes, selectedGroupId, selectedExitNodeId)
        ?? firstExitNodeForProject(snapshot.projects, snapshot.exitNodes, selectedGroupId)
      : null;
    if (mode === 'vpn' && !exitNodeId) return;
    selectedExitNodeId = exitNodeId;
    if (snapshot.applied.connected && selectedGroupId) {
      sendSafely({ type: 'switch', desired: { ...snapshot.desired, groupId: selectedGroupId, exitNodeId, connected: true } });
    }
  }

  function selectExitNode(event: Event): void {
    if (!snapshot?.uxCapabilities.exitNodeCatalog) return;
    const requestedExitNodeId = (event.currentTarget as HTMLSelectElement).value || null;
    const exitNodeId = exitNodeForProject(snapshot.projects, snapshot.exitNodes, selectedGroupId, requestedExitNodeId);
    if (requestedExitNodeId && !exitNodeId) return;
    selectedExitNodeId = exitNodeId;
    if (snapshot.applied.connected && selectedGroupId) {
      sendSafely({ type: 'switch', desired: { ...snapshot.desired, groupId: selectedGroupId, exitNodeId, connected: true } });
    }
  }

  function previewScenario(event: Event): void {
    const scenario = (event.currentTarget as HTMLSelectElement).value as Scenario;
    onboardingComplete = false;
    onboardingStep = 'enroll';
    formError = '';
    void desktop.setScenario(scenario).then(() => {
      if (scenario === 'setup_required') requestAnimationFrame(() => enrollmentInput?.focus());
      if (scenario === 'auth_required') requestAnimationFrame(() => authEmailInput?.focus());
    });
  }

  function runChecks(): void {
    checksRunning = false;
    diagnosticsNotice = snapshot?.uxCapabilities.diagnostics
      ? tx('allChecks')
      : 'DIAGNOSTICS_CAPABILITY_NOT_AVAILABLE';
  }

  function showExportNotice(): void {
    diagnosticsNotice = locale === 'en' ? 'Redacted bundle prepared — no file was written.' : 'Informe redactado preparado; no se escribió ningún archivo.';
  }

  function formatTime(value: string): string {
    return new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' }).format(new Date(value));
  }
</script>

<a class="skip-link" href="#main-content">{tx('skip')}</a>
<div class="app-shell" inert={showOnboarding}>
  <aside class="sidebar">
    <div class="brand">
      <span class="brand-mark" aria-hidden="true"><ShieldCheck size={21} strokeWidth={1.9} /></span>
      <span class="brand-name">Wire<span>ZTNA</span></span>
    </div>

    <nav aria-label={tx('navLabel')}>
      <p class="eyebrow">{tx('workspace')}</p>
      {#each nav as item}
        {@const Icon = item.icon}
        <button class:active={activeView === item.id} type="button" onclick={() => activeView = item.id} aria-current={activeView === item.id ? 'page' : undefined}>
          <Icon size={18} strokeWidth={1.6} />
          <span>{tx(item.key)}</span>
          {#if item.id === 'activity' && $desktop.activities.length > 0}<span class="nav-count">{$desktop.activities.length}</span>{/if}
        </button>
      {/each}
    </nav>

    <div class="sidebar-foot">
      <div class="device-row"><span class="avatar">AL</span><span><strong>Alex Lee</strong><small>{tx('account')}</small></span></div>
      <p>{tx('version')}</p>
    </div>
  </aside>

  <main id="main-content" tabindex="-1">
    <header class="topbar">
      <div>
        <p class="eyebrow">{tx('device')} · macOS</p>
        <h1>{tx(nav.find((item) => item.id === activeView)?.key ?? 'connection')}</h1>
      </div>
      <div class="topbar-actions">
        {#if desktop.supportsScenarios}<span class="mock-badge"><TerminalSquare size={14} />{tx('noNetwork')}</span>{/if}
        <label class="language-select">
          <Languages size={16} aria-hidden="true" />
          <span class="sr-only">{tx('language')}</span>
          <select bind:value={locale} aria-label={tx('language')}>
            <option value="en">EN</option><option value="es">ES</option>
          </select>
        </label>
      </div>
    </header>

    {#if !$desktop.ready}
      <section class="loading" aria-live="polite"><RefreshCw class="spin" size={22} />{tx('checking')}</section>
    {:else if !snapshot}
      <section class="view startup-error" role="alert">
        <span class="hero-icon"><WifiOff size={34} /></span>
        <div><p class="eyebrow">{tx('service_unavailable')}</p><h2>{tx('startupError')}</h2><p>{desktopError?.message ?? tx('serviceIssue')}</p>{#if desktopError}<small>{locale === 'en' ? 'Diagnostic code' : 'Código de diagnóstico'}: {desktopError.diagnosticCode}</small>{/if}</div>
        <button class="button primary" type="button" onclick={() => void desktop.initialize()}>{tx('retry')}</button>
      </section>
    {:else if activeView === 'connection'}
      <section class="view connection-view" aria-labelledby="connection-title">
        <div class="connection-hero tone-{statusTone(status)}">
          <div class="hero-orbit" aria-hidden="true">
            <div class="orbit-ring"></div>
            <span class="hero-icon">
              {#if status === 'connected'}<ShieldCheck size={42} strokeWidth={1.45} />
              {:else if status === 'connecting' || status === 'reconnecting'}<RefreshCw class="spin" size={38} strokeWidth={1.45} />
              {:else if status === 'service_unavailable'}<WifiOff size={38} strokeWidth={1.45} />
              {:else if status === 'degraded'}<CircleAlert size={38} strokeWidth={1.45} />
              {:else}<Power size={38} strokeWidth={1.45} />{/if}
            </span>
          </div>
          <div class="hero-copy">
            <div class="status-line"><span class="status-dot"></span><span>{statusLabel(status)}</span></div>
            <h2 id="connection-title">
              {#if status === 'connected'}{snapshot.mode === 'vpn' ? tx('vpn') : tx('split')}
              {:else if status === 'degraded'}{degradedHeadline}
              {:else if status === 'service_unavailable'}{tx('serviceIssue')}
              {:else if status === 'connecting' || status === 'reconnecting'}{$desktop.activeStep ? stepNames[locale][$desktop.activeStep] : tx('checking')}
              {:else}{tx('protected')}{/if}
            </h2>
            <p>{(currentProject?.name ?? snapshot.projectId) || snapshot.desired.groupId}{currentExitNode ? ` · ${currentExitNode.name}` : ''}</p>
          </div>
          <button id="primary-connection-action" class:secondary={status === 'connected' || status === 'degraded'} class="primary-action" type="button" onclick={toggleConnection} disabled={$desktop.busy || status === 'connecting' || status === 'reconnecting' || connectBlocked}>
            {#if status === 'connected' || status === 'degraded'}{tx('disconnect')}
            {:else if status === 'service_unavailable'}{tx('retry')}
            {:else}{tx('connect')}{/if}
          </button>
        </div>

        {#if status === 'connecting' || status === 'reconnecting'}
          <div class="progress-card" aria-live="polite">
            <div class="progress-heading"><span>{tx('progress')}</span><strong>{$desktop.progress}%</strong></div>
            <div class="progress-track" role="progressbar" aria-valuenow={$desktop.progress} aria-valuemin="0" aria-valuemax="100"><span style={`width: ${$desktop.progress}%`}></span></div>
            <ol class="steps">
              {#each steps as step, index}
                {@const activeIndex = $desktop.activeStep ? steps.indexOf($desktop.activeStep) : -1}
                <li class:done={index < activeIndex} class:current={index === activeIndex}>
                  <span>{index < activeIndex ? '✓' : index + 1}</span>{stepNames[locale][step]}
                </li>
              {/each}
            </ol>
          </div>
        {/if}

        {#if connectBlocked}<div class="inline-alert" role="status"><CircleAlert size={18} /><span>{connectGuidance}</span></div>{/if}
        {#if trayUpdateUnavailable}<div class="inline-alert" role="status"><CircleAlert size={18} /><span>{locale === 'en' ? 'Tray status unavailable; retrying with the warning state.' : 'Estado del tray no disponible; reintentando con el estado de advertencia.'}</span></div>{/if}
        {#if desktopError}<div class="inline-alert danger" role="alert"><CircleAlert size={18} /><span>{desktopError.message} <small>{locale === 'en' ? 'Diagnostic code' : 'Código de diagnóstico'}: {desktopError.diagnosticCode}</small></span></div>{/if}

        <div class="control-grid">
          <article class="panel controls-panel">
            <div class="panel-heading"><div><p class="eyebrow">{tx('accessPolicy')}</p><h3>{locale === 'en' ? 'Connection preferences' : 'Preferencias de conexión'}</h3></div><SlidersHorizontal size={20} /></div>
            <label>{tx('project')}<select value={selectedGroupId} onchange={selectProject} disabled={$desktop.busy || !snapshot.uxCapabilities.projectCatalog}><option value="" disabled>{locale === 'en' ? 'Select a project' : 'Selecciona un proyecto'}</option>{#each snapshot.projects as project}<option value={project.id}>{project.name}</option>{/each}</select></label>
            <div class="field-row">
              <label>{tx('mode')}<select value={selectedMode} onchange={selectMode} disabled={$desktop.busy || !snapshot.uxCapabilities.exitNodeCatalog}><option value="split">{tx('split')}</option><option value="vpn" disabled={availableExitNodes.length === 0}>{tx('vpn')}</option></select></label>
              <label>{tx('exitNode')}<select value={selectedExitNodeId ?? ''} onchange={selectExitNode} disabled={$desktop.busy || selectedMode !== 'vpn' || !snapshot.uxCapabilities.exitNodeCatalog}><option value="">—</option>{#each availableExitNodes as node}<option value={node.id}>{node.name}{node.location ? ` · ${node.location}` : ''} · {tx('online')}</option>{/each}</select></label>
            </div>
          </article>

          <article class="panel health-panel">
            <div class="panel-heading"><div><p class="eyebrow">{tx('liveSignal')}</p><h3>{tx('health')}</h3></div><Wifi size={20} /></div>
            <dl>
              <div><dt>{tx('lastHandshake')}</dt><dd><span class="health-dot"></span>—</dd></div>
              <div><dt>{tx('dns')}</dt><dd class:warning-text={snapshot.health.dns !== 'healthy'}>{healthLabel(snapshot.health.dns)}</dd></div>
              <div><dt>{tx('routes')}</dt><dd>{healthLabel(snapshot.health.routes)}</dd></div>
              <div><dt>{tx('session')}</dt><dd>{healthLabel(snapshot.health.endToEnd)}</dd></div>
            </dl>
          </article>
        </div>
        <p class="intent-note"><CircleAlert size={15} />{tx('disconnectIntent')}</p>
      </section>
    {:else if activeView === 'resources'}
      <section class="view">
        <div class="view-intro"><div><p class="eyebrow">{currentProject?.name}</p><h2>{tx('availableResources')}</h2><p>{tx('resourceBody')}</p></div><div class="search-box"><Search size={17} /><input aria-label={tx('searchResources')} placeholder={tx('searchResources')} /></div></div>
        <div class="resource-grid">
          {#each currentProject?.resources ?? snapshot.resources as resource}
            <article class="resource-card">
              <span class="resource-icon">{#if resource.kind === 'app'}<AppWindow size={20} />{:else if resource.kind === 'cidr'}<Network size={20} />{:else}<Server size={20} />{/if}</span>
              <div><div class="resource-title"><h3>{resource.name}</h3><span class:offline={!resource.online}>{resource.online ? tx('online') : tx('offline')}</span></div><p>{resource.detail}</p><small>{tx(resource.kind)}</small></div>
              <button type="button" aria-label={`${resource.name}: ${locale === 'en' ? 'details' : 'detalles'}`}><ChevronRight size={18} /></button>
            </article>
          {/each}
        </div>
      </section>
    {:else if activeView === 'activity'}
      <section class="view narrow-view">
        <div class="view-intro"><div><p class="eyebrow">{tx('eventStream')}</p><h2>{tx('recentActivity')}</h2><p>{desktop.supportsScenarios ? (locale === 'en' ? 'Events emitted by the versioned mock service.' : 'Eventos emitidos por el servicio mock versionado.') : (locale === 'en' ? 'Events emitted by the local IPC v2 service.' : 'Eventos emitidos por el servicio IPC v2 local.')}</p></div></div>
        <div class="timeline" aria-live="polite">
          {#if $desktop.activities.length === 0}<div class="empty-state"><Activity size={28} /><p>{desktop.supportsScenarios ? (locale === 'en' ? 'No activity yet. Connect or preview a scenario.' : 'Aún no hay actividad. Conecta o previsualiza un escenario.') : (locale === 'en' ? 'No activity yet. Connect to begin.' : 'Aún no hay actividad. Conecta para empezar.')}</p></div>{/if}
          {#each $desktop.activities as event}
            <article><span class="timeline-mark"><Check size={14} /></span><div><div><strong>{activityTitle(event.code)}</strong><time datetime={event.at}>{formatTime(event.at)}</time></div><p>{activityDetail(event.code, event.detail)}</p></div></article>
          {/each}
        </div>
      </section>
    {:else if activeView === 'diagnostics'}
      <section class="view narrow-view">
        <div class="view-intro"><div><p class="eyebrow">{tx('localOnly')}</p><h2>{tx('diagnostics')}</h2><p>{locale === 'en' ? 'Inspect service readiness without exposing credentials or tunnel keys.' : 'Comprueba el servicio sin exponer credenciales ni claves del túnel.'}</p></div><button class="button secondary" type="button" onclick={runChecks} disabled={checksRunning || !snapshot.uxCapabilities.diagnostics}>{#if checksRunning}<RefreshCw class="spin" size={17} />{:else}<CircleGauge size={17} />{/if}{tx('runChecks')}</button></div>
        <div class="diagnostic-list">
          {#each [
            { icon: ShieldCheck, label: tx('deviceSecurity'), value: tx('passed') },
            { icon: Server, label: tx('service'), value: snapshot.health.endToEnd === 'healthy' ? tx('passed') : tx('warning') },
            { icon: Globe2, label: tx('dns'), value: snapshot.health.dns === 'healthy' ? tx('passed') : tx('warning') },
            { icon: Route, label: tx('routes'), value: snapshot.health.routes === 'healthy' ? tx('passed') : tx('warning') },
          ] as check}
            {@const CheckIcon = check.icon}
            <div><span class="diagnostic-icon"><CheckIcon size={19} /></span><strong>{check.label}</strong><span class:warning-text={check.value === tx('warning')}><CheckCircle2 size={16} />{check.value}</span></div>
          {/each}
        </div>
        <div class="export-panel"><div><Download size={20} /><span><strong>{locale === 'en' ? 'Support bundle' : 'Informe de soporte'}</strong><small>{locale === 'en' ? 'Logs and metadata are redacted before export.' : 'Los logs y metadatos se redactan antes de exportar.'}</small></span></div><button class="button secondary" type="button" onclick={showExportNotice}>{tx('exportBundle')}</button></div>
        {#if diagnosticsNotice}<div class="inline-alert" role="status"><CheckCircle2 size={18} />{diagnosticsNotice}</div>{/if}
      </section>
    {:else}
      <section class="view narrow-view">
        <div class="view-intro"><div><p class="eyebrow">Desktop</p><h2>{tx('appearance')}</h2><p>{locale === 'en' ? 'Preferences for this build remain in UI memory.' : 'En esta versión las preferencias sólo viven en la memoria de la UI.'}</p></div></div>
        <div class="settings-list">
          <label class="setting-row"><span><Laptop size={19} /><span><strong>{tx('launchLogin')}</strong><small>{locale === 'en' ? 'Open the UI after signing in' : 'Abre la UI al iniciar sesión'}</small></span></span><input type="checkbox" bind:checked={launchAtLogin} /></label>
          <label class="setting-row"><span><Power size={19} /><span><strong>{tx('autoConnect')}</strong><small>{locale === 'en' ? 'Respect explicit disconnect intent' : 'Respeta la intención de desconexión'}</small></span></span><input type="checkbox" bind:checked={autoConnect} /></label>
          <label class="setting-row"><span><Cloud size={19} /><span><strong>{tx('notifications')}</strong><small>{locale === 'en' ? 'Not implemented' : 'No implementadas'}</small></span></span><input type="checkbox" bind:checked={notifications} disabled /></label>
          <label class="setting-row"><span><Activity size={19} /><span><strong>{tx('reducedMotion')}</strong><small>{locale === 'en' ? 'Also follows the system preference' : 'También respeta la preferencia del sistema'}</small></span></span><input type="checkbox" bind:checked={reducedMotion} /></label>
        </div>
        {#if desktop.supportsScenarios}
          <div class="scenario-panel">
            <div><p class="eyebrow">{tx('developerPreview')}</p><h3>{tx('mockScenarios')}</h3><p>{tx('mockHint')}</p></div>
            <label>{tx('status')}<select onchange={previewScenario} disabled={$desktop.busy}>{#each scenarioOptions as option}<option value={option.value}>{option[locale]}</option>{/each}</select></label>
          </div>
        {/if}
        <div class="about-panel" data-version-marker={appVersionMarker}><span class="brand-mark"><ShieldCheck size={21} /></span><div><strong>WireZTNA Desktop</strong><small>{appVersion} · Tauri 2 · IPC {$desktop.handshake?.negotiatedVersion ?? '—'}</small></div><span>{tx('update')}</span></div>
      </section>
    {/if}
  </main>
</div>

<div class="sr-only" aria-live="polite" aria-atomic="true">{snapshot ? statusLabel(status) : tx('checking')}</div>

{#if showOnboarding && (snapshot || status === 'auth_required' || status === 'setup_required')}
  <div class="modal-backdrop" role="presentation">
    <div class="onboarding" role="dialog" aria-modal="true" aria-labelledby="onboarding-title" tabindex="-1" onkeydown={trapModalFocus}>
      <div class="onboarding-visual" aria-hidden="true">
        <div class="mini-brand"><span class="brand-mark"><ShieldCheck size={20} /></span><strong>WireZTNA</strong></div>
        <div class="trust-graphic"><span class="trust-ring ring-one"></span><span class="trust-ring ring-two"></span><span class="trust-center"><LockKeyhole size={31} /></span><span class="node node-a"></span><span class="node node-b"></span><span class="node node-c"></span></div>
        <div><p>{tx('zeroTrustAccess')}</p><small>{locale === 'en' ? 'Private by design. Simple by default.' : 'Privado por diseño. Simple por defecto.'}</small></div>
      </div>
      <div class="onboarding-form">
        <div class="step-dots" aria-label={tx('progress')}><span class:active={onboardingStep === 'enroll' || onboardingStep === 'email'}></span><span class:active={onboardingStep === 'otp'}></span><span class:active={onboardingStep === 'complete'}></span></div>
        {#if onboardingStep === 'enroll'}
          <p class="eyebrow">1 / 3 · {tx('enrollment')}</p><h2 id="onboarding-title">{tx('setupTitle')}</h2><p>{tx('setupBody')}</p>
          <form onsubmit={handleEnroll} novalidate>
            <label for="enrollment-url">{tx('enrollmentUrl')}</label><input id="enrollment-url" bind:this={enrollmentInput} type="url" bind:value={enrollmentUrl} spellcheck="false" autocomplete="off" aria-describedby={formError ? 'form-error' : 'url-hint'} />
            <small id="url-hint">https://your-wireztna-host/api/v1/clients/enroll?token=…</small>
            {#if formError}<p id="form-error" class="form-error" role="alert">{formError}</p>{/if}
            <button class="button primary" type="submit" disabled={authBusy}>{tx('continue')}<ArrowRight size={17} /></button>
          </form>
        {:else if onboardingStep === 'email'}
          <p class="eyebrow">1 / 3 · {tx('verification')}</p><h2 id="onboarding-title">{tx('emailTitle')}</h2><p>{tx('emailBody')}</p>
          <form onsubmit={handleOtpRequest} novalidate>
            <label for="auth-email">{tx('email')}</label><input id="auth-email" bind:this={authEmailInput} type="email" bind:value={authEmail} spellcheck="false" autocomplete="email" required aria-describedby={formError ? 'form-error' : undefined} />
            {#if formError}<p id="form-error" class="form-error" role="alert">{formError}</p>{/if}
            <button class="button primary" type="submit" disabled={authBusy}>{tx('sendCode')}<ArrowRight size={17} /></button>
          </form>
        {:else if onboardingStep === 'otp'}
          <p class="eyebrow">2 / 3 · {tx('verification')}</p><h2 id="onboarding-title">{tx('otpTitle')}</h2><p>{tx('otpBody')} <strong>{otpSentTo}</strong></p>
          <form onsubmit={handleOtp} novalidate>
            <label for="otp">{tx('otp')}</label><input id="otp" bind:this={otpInput} class="otp-input" inputmode="numeric" maxlength="6" pattern="[0-9]{6}" placeholder="000000" bind:value={otp} autocomplete="one-time-code" />
            {#if formError}<p class="form-error" role="alert">{formError}</p>{/if}
            <button class="button primary" type="submit" disabled={authBusy}>{tx('verify')}<ArrowRight size={17} /></button>
            <button class="text-button" type="button" disabled={authBusy} onclick={() => { formError = ''; onboardingStep = 'email'; requestAnimationFrame(() => authEmailInput?.focus()); }}>{locale === 'en' ? 'Use another email' : 'Usar otro email'}</button>
          </form>
        {:else}
          <div class="success-mark"><Check size={32} /></div><p class="eyebrow">3 / 3 · {tx('ready')}</p><h2 id="onboarding-title" bind:this={confirmationHeading} tabindex="-1">{tx('readyTitle')}</h2><p>{tx('readyBody')}</p>
          <div class="confirm-summary"><div><Laptop size={18} /><span><small>{tx('device')}</small><strong>Alex’s MacBook Pro</strong></span></div><div><ShieldCheck size={18} /><span><small>{tx('project')}</small><strong>Engineering</strong></span></div></div>
          <button class="button primary" type="button" onclick={finishOnboarding}>{tx('continue')}<ArrowRight size={17} /></button>
          <button class="text-button" type="button" onclick={closeOnboarding}>{tx('later')}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}
