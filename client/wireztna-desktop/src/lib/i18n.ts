export type Locale = 'en' | 'es';

const messages = {
  en: {
    appName: 'WireZTNA', connection: 'Connection', resources: 'Resources', activity: 'Activity',
    diagnostics: 'Diagnostics', settings: 'Settings', account: 'Northstar Labs', device: 'This Mac',
    connected: 'Connected', disconnected: 'Disconnected', connecting: 'Connecting', reconnecting: 'Reconnecting',
    degraded: 'Needs attention', setup_required: 'Setup required', auth_required: 'Sign in required', update_required: 'Update required',
    service_unavailable: 'Service unavailable', split: 'Split tunnel', vpn: 'Private VPN', connect: 'Connect',
    disconnect: 'Disconnect', project: 'Project', mode: 'Mode', exitNode: 'Exit node', health: 'Connection health',
    healthy: 'Healthy', lastHandshake: 'Last handshake', protected: 'Your private resources are protected.',
    setupTitle: 'Connect this device', setupBody: 'Paste the enrollment link from your WireZTNA administrator.',
    enrollmentUrl: 'Enrollment URL', continue: 'Continue', emailTitle: 'Sign in to WireZTNA',
    emailBody: 'Enter your account email to receive a one-time code.', email: 'Email address', sendCode: 'Send code',
    otpTitle: 'Verify your identity', otpBody: 'Enter the 6-digit code sent to your email address.', otp: 'One-time code', verify: 'Verify code',
    readyTitle: 'Sign-in complete', readyBody: 'Your WireZTNA session is active and your authorized resources are ready.',
    connectNow: 'Connect now', later: 'Not now', emailError: 'Enter a valid email address.', authError: 'Enter a valid 6-digit code.', connectionError: 'Authentication succeeded, but the local service could not load your session.', urlError: 'Enter a valid wireztna:// enrollment URL.',
    availableResources: 'Available to you', resourceBody: 'Private apps and networks granted by your active project.',
    recentActivity: 'Recent activity', runChecks: 'Run checks', exportBundle: 'Export redacted bundle', allChecks: 'All local checks passed',
    language: 'Language', launchLogin: 'Launch at login', autoConnect: 'Connect automatically', notifications: 'Notifications',
    reducedMotion: 'Reduce motion', appearance: 'Appearance & behavior', mockScenarios: 'Spike scenarios',
    mockHint: 'Preview every IPC state without touching the network.', status: 'Status', retry: 'Retry',
    dnsIssue: 'Private DNS is not responding.', routesIssue: 'Private route configuration needs attention.',
    wireGuardIssue: 'The WireGuard tunnel needs attention.', endToEndIssue: 'The private connection could not be verified.',
    degradedIssue: 'Connection health needs attention.', serviceIssue: 'The local WireZTNA service cannot be reached.',
    openSettings: 'Open settings', progress: 'Connection progress', checking: 'Checking local service…',
    version: 'Desktop · IPC v2', skip: 'Skip to content', navLabel: 'Main navigation', close: 'Close',
    noNetwork: 'Mock mode · no network changes', selected: 'Selected', online: 'Online', offline: 'Offline',
    resourcesCount: 'resources', cidr: 'Private network', app: 'Published app', publisher: 'Publisher',
    deviceSecurity: 'Device security', service: 'Background service', dns: 'Private DNS', routes: 'Private routes',
    passed: 'Passed', warning: 'Warning', update: 'Up to date', session: 'Session', expires: 'Expires in 7h 42m',
    disconnectIntent: 'Disconnecting is explicit. Quitting the UI leaves tunnel state unchanged.',
    workspace: 'Workspace', accessPolicy: 'Access policy', liveSignal: 'Live signal', eventStream: 'IPC event stream',
    localOnly: 'Local only', developerPreview: 'Developer preview', enrollment: 'Enrollment', verification: 'Verification',
    ready: 'Ready', zeroTrustAccess: 'Zero Trust access', degradedValue: 'Degraded', unavailableValue: 'Unavailable',
    staleValue: 'Stale', searchResources: 'Search resources', startupError: 'The local service did not complete the IPC handshake.',
  },
  es: {
    appName: 'WireZTNA', connection: 'Conexión', resources: 'Recursos', activity: 'Actividad',
    diagnostics: 'Diagnóstico', settings: 'Ajustes', account: 'Northstar Labs', device: 'Este Mac',
    connected: 'Conectado', disconnected: 'Desconectado', connecting: 'Conectando', reconnecting: 'Reconectando',
    degraded: 'Requiere atención', setup_required: 'Configuración requerida', auth_required: 'Inicio de sesión requerido', update_required: 'Actualización requerida',
    service_unavailable: 'Servicio no disponible', split: 'Túnel dividido', vpn: 'VPN privada', connect: 'Conectar',
    disconnect: 'Desconectar', project: 'Proyecto', mode: 'Modo', exitNode: 'Nodo de salida', health: 'Salud de la conexión',
    healthy: 'Saludable', lastHandshake: 'Último handshake', protected: 'Tus recursos privados están protegidos.',
    setupTitle: 'Conecta este dispositivo', setupBody: 'Pega el enlace de inscripción de tu administrador de WireZTNA.',
    enrollmentUrl: 'URL de inscripción', continue: 'Continuar', emailTitle: 'Inicia sesión en WireZTNA',
    emailBody: 'Introduce el email de tu cuenta para recibir un código de un solo uso.', email: 'Dirección de email', sendCode: 'Enviar código',
    otpTitle: 'Verifica tu identidad', otpBody: 'Introduce el código de 6 dígitos enviado a tu dirección de email.', otp: 'Código de un solo uso', verify: 'Verificar código',
    readyTitle: 'Inicio de sesión completado', readyBody: 'Tu sesión de WireZTNA está activa y tus recursos autorizados están preparados.',
    connectNow: 'Conectar ahora', later: 'Ahora no', emailError: 'Introduce una dirección de email válida.', authError: 'Introduce un código válido de 6 dígitos.', connectionError: 'La autenticación se completó, pero el servicio local no pudo cargar tu sesión.', urlError: 'Introduce una URL de inscripción wireztna:// válida.',
    availableResources: 'Disponibles para ti', resourceBody: 'Aplicaciones y redes privadas concedidas por el proyecto activo.',
    recentActivity: 'Actividad reciente', runChecks: 'Ejecutar comprobaciones', exportBundle: 'Exportar informe redactado', allChecks: 'Todas las comprobaciones locales pasaron',
    language: 'Idioma', launchLogin: 'Abrir al iniciar sesión', autoConnect: 'Conectar automáticamente', notifications: 'Notificaciones',
    reducedMotion: 'Reducir movimiento', appearance: 'Apariencia y comportamiento', mockScenarios: 'Escenarios del spike',
    mockHint: 'Previsualiza cada estado IPC sin tocar la red.', status: 'Estado', retry: 'Reintentar',
    dnsIssue: 'El DNS privado no responde.', routesIssue: 'La configuración de rutas privadas requiere atención.',
    wireGuardIssue: 'El túnel WireGuard requiere atención.', endToEndIssue: 'No se pudo verificar la conexión privada.',
    degradedIssue: 'La salud de la conexión requiere atención.', serviceIssue: 'No se puede contactar con el servicio local de WireZTNA.',
    openSettings: 'Abrir ajustes', progress: 'Progreso de conexión', checking: 'Comprobando el servicio local…',
    version: 'Escritorio · IPC v2', skip: 'Saltar al contenido', navLabel: 'Navegación principal', close: 'Cerrar',
    noNetwork: 'Modo mock · sin cambios de red', selected: 'Seleccionado', online: 'En línea', offline: 'Sin conexión',
    resourcesCount: 'recursos', cidr: 'Red privada', app: 'Aplicación publicada', publisher: 'Publisher',
    deviceSecurity: 'Seguridad del dispositivo', service: 'Servicio en segundo plano', dns: 'DNS privado', routes: 'Rutas privadas',
    passed: 'Correcto', warning: 'Aviso', update: 'Actualizado', session: 'Sesión', expires: 'Caduca en 7h 42m',
    disconnectIntent: 'Desconectar es una acción explícita. Salir de la UI no cambia el estado del túnel.',
    workspace: 'Espacio de trabajo', accessPolicy: 'Política de acceso', liveSignal: 'Señal en directo', eventStream: 'Flujo de eventos IPC',
    localOnly: 'Sólo local', developerPreview: 'Vista de desarrollo', enrollment: 'Inscripción', verification: 'Verificación',
    ready: 'Preparado', zeroTrustAccess: 'Acceso Zero Trust', degradedValue: 'Degradado', unavailableValue: 'No disponible',
    staleValue: 'Desactualizado', searchResources: 'Buscar recursos', startupError: 'El servicio local no completó la negociación IPC.',
  },
} as const;

export type MessageKey = keyof typeof messages.en;

export function translate(locale: Locale, key: MessageKey): string {
  return messages[locale][key];
}

export interface IpcErrorPresentation {
  message: string;
  diagnosticCode: string;
}

const ipcGuidance: Record<Locale, Record<string, string>> = {
  en: {
    IPC_HELPER_NOT_FOUND: 'The WireZTNA helper is not installed. Reinstall the desktop app or contact your administrator.',
    IPC_HELPER_NOT_RUNNING: 'The WireZTNA helper is not running. Start or reinstall the desktop service, then retry.',
    IPC_HELPER_PERMISSION_DENIED: 'WireZTNA cannot start its helper with the required permissions. Check system security settings or contact your administrator.',
    IPC_PERMISSION_DENIED: 'WireZTNA cannot start its helper with the required permissions. Check system security settings or contact your administrator.',
    IPC_UNSUPPORTED_PLATFORM: 'This platform is not supported by the WireZTNA helper.',
    IPC_SHUTDOWN: 'The WireZTNA helper is shutting down. Wait a moment, then retry.',
    IPC_PENDING_LIMIT: 'The helper is handling too many requests. Wait a moment, then retry.',
    IPC_REQUEST_IN_PROGRESS: 'A connection change is already in progress. Wait for it to finish, then retry.',
    IPC_OPERATION_IN_PROGRESS: 'A connection change is already in progress. Wait for it to finish, then retry.',
    IPC_CANCELED: 'The request was canceled. Retry if you still want to make this change.',
    IPC_TIMEOUT: 'The local service did not respond in time. Check that the WireZTNA helper is running, then retry.',
  },
  es: {
    IPC_HELPER_NOT_FOUND: 'El asistente de WireZTNA no está instalado. Reinstala la aplicación o contacta con tu administrador.',
    IPC_HELPER_NOT_RUNNING: 'El asistente de WireZTNA no está en ejecución. Inicia o reinstala el servicio de escritorio y vuelve a intentarlo.',
    IPC_HELPER_PERMISSION_DENIED: 'WireZTNA no puede iniciar el asistente con los permisos necesarios. Revisa la seguridad del sistema o contacta con tu administrador.',
    IPC_PERMISSION_DENIED: 'WireZTNA no puede iniciar el asistente con los permisos necesarios. Revisa la seguridad del sistema o contacta con tu administrador.',
    IPC_UNSUPPORTED_PLATFORM: 'Esta plataforma no es compatible con el asistente de WireZTNA.',
    IPC_SHUTDOWN: 'El asistente de WireZTNA se está cerrando. Espera un momento y vuelve a intentarlo.',
    IPC_PENDING_LIMIT: 'El asistente está atendiendo demasiadas solicitudes. Espera un momento y vuelve a intentarlo.',
    IPC_REQUEST_IN_PROGRESS: 'Ya hay un cambio de conexión en curso. Espera a que termine y vuelve a intentarlo.',
    IPC_OPERATION_IN_PROGRESS: 'Ya hay un cambio de conexión en curso. Espera a que termine y vuelve a intentarlo.',
    IPC_CANCELED: 'La solicitud se canceló. Vuelve a intentarlo si aún quieres aplicar este cambio.',
    IPC_TIMEOUT: 'El servicio local no respondió a tiempo. Comprueba que el asistente de WireZTNA esté activo y vuelve a intentarlo.',
  },
};

export function ipcErrorPresentation(locale: Locale, diagnosticCode: string): IpcErrorPresentation {
  const fallback = locale === 'en'
    ? 'The local WireZTNA service could not complete the request. Retry or contact your administrator.'
    : 'El servicio local de WireZTNA no pudo completar la solicitud. Vuelve a intentarlo o contacta con tu administrador.';
  return { message: ipcGuidance[locale][diagnosticCode] ?? fallback, diagnosticCode };
}
