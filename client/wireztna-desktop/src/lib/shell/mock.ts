import { createMockIpc, type MockIpcOptions } from '../ipc/contract';
import {
  DesktopAuthError,
  DesktopEnrollmentError,
  type DesktopAuthClient,
  type DesktopAuthContext,
  type DesktopBootstrapClients,
  type DesktopEnrollmentClient,
  type DesktopEnrollmentContext,
  type ShellCapabilities,
  type ShellClient,
  type ShellEvent,
  type ShellEventSource,
  type TrayVisualState,
} from './contract';

const OFFLINE_CAPABILITIES: Readonly<ShellCapabilities> = Object.freeze({
  tray: 'emulated',
  closeToHide: 'emulated',
  singleInstance: 'not_implemented',
  deepLinks: 'not_implemented',
  notifications: 'not_implemented',
});

/** Deterministic, in-memory shell for tests and local browser development only. */
export class MockShellClient implements ShellClient {
  readonly backend = 'offline-mock' as const;
  readonly capabilities = OFFLINE_CAPABILITIES;
  private readonly listeners = new Set<(event: ShellEvent) => void>();
  private readonly commandLog: Array<'show' | 'hide' | 'quit'> = [];
  private readonly trayStateLog: TrayVisualState[] = [];

  async subscribe(listener: (event: ShellEvent) => void): Promise<() => void> {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  async show(): Promise<void> {
    this.commandLog.push('show');
    this.emit({ type: 'visibility_changed', visible: true, source: 'api' });
  }

  async hide(): Promise<void> {
    this.commandLog.push('hide');
    this.emit({ type: 'visibility_changed', visible: false, source: 'api' });
  }

  async quit(): Promise<void> {
    this.commandLog.push('quit');
    this.emit({ type: 'quit_requested', source: 'api' });
  }

  async setTrayVisualState(state: TrayVisualState): Promise<void> {
    this.trayStateLog.push(state);
  }

  closeWindow(): void {
    this.emit({ type: 'visibility_changed', visible: false, source: 'window-close' });
  }

  activateTray(action: 'show' | 'hide' | 'quit'): void {
    this.commandLog.push(action);
    if (action === 'quit') {
      this.emit({ type: 'quit_requested', source: 'tray' });
      return;
    }
    this.emit({ type: 'visibility_changed', visible: action === 'show', source: 'tray' });
  }

  commands(): readonly ('show' | 'hide' | 'quit')[] {
    return [...this.commandLog];
  }

  trayStates(): readonly TrayVisualState[] {
    return [...this.trayStateLog];
  }

  listenerCount(): number {
    return this.listeners.size;
  }

  private emit(event: ShellEvent): void {
    for (const listener of this.listeners) listener(event);
  }
}

/** Deterministic, in-memory authentication client for tests and browser development. */
export class MockDesktopAuthClient implements DesktopAuthClient {
  private requestedEmail = '';

  constructor(private readonly scenarios?: { setScenario(scenario: 'disconnected'): Promise<unknown> }) {}

  async context(): Promise<DesktopAuthContext> {
    return { email: 'developer@example.com' };
  }

  async requestOtp(email: string): Promise<void> {
    const normalized = email.trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(normalized)) {
      throw new DesktopAuthError('AUTH_INVALID_EMAIL');
    }
    this.requestedEmail = normalized;
  }

  async verifyOtp(email: string, code: string): Promise<void> {
    if (email.trim() !== this.requestedEmail || !/^\d{6}$/.test(code)) {
      throw new DesktopAuthError('AUTH_INVALID_OTP');
    }
    await this.scenarios?.setScenario('disconnected');
  }
}

export class MockDesktopEnrollmentClient implements DesktopEnrollmentClient {
  private enrolled: boolean;

  constructor(enrolled = false) {
    this.enrolled = enrolled;
  }

  async context(): Promise<DesktopEnrollmentContext> {
    return { enrolled: this.enrolled, email: this.enrolled ? 'developer@example.com' : '' };
  }

  async enroll(url: string): Promise<DesktopEnrollmentContext> {
    if (!/^https:\/\/[^/]+\/api\/v1\/clients\/enroll\?token=[A-Za-z0-9_-]{32,256}$/.test(url)) {
      throw new DesktopEnrollmentError('ENROLLMENT_INVALID_URL');
    }
    this.enrolled = true;
    return { enrolled: true, email: 'developer@example.com' };
  }
}

export interface OfflineMockOptions extends MockIpcOptions {
  shell?: MockShellClient;
}

export function createOfflineMockBootstrap(options: OfflineMockOptions = {}): DesktopBootstrapClients {
  const desktopIpcClient = createMockIpc(options);
  return {
    desktopIpcClient,
    desktopAuthClient: new MockDesktopAuthClient(desktopIpcClient),
    desktopEnrollmentClient: new MockDesktopEnrollmentClient(options.initialStatus !== 'setup_required'),
    scenarioClient: desktopIpcClient,
    shellClient: options.shell ?? new MockShellClient(),
  };
}

export function visibilityEvent(visible: boolean, source: ShellEventSource): ShellEvent {
  return { type: 'visibility_changed', visible, source };
}
