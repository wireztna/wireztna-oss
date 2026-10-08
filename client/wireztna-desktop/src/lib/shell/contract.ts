import type { DesktopIpcClient, DesktopScenarioClient } from '../ipc/contract';

export type ShellBackend = 'tauri' | 'wails' | 'offline-mock';
export type ShellCapabilityState = 'available' | 'emulated' | 'not_implemented';
export type ShellEventSource = 'api' | 'tray' | 'window-close' | 'operating-system';
export type TrayVisualState = 'disconnected' | 'connected' | 'attention';

export interface ShellCapabilities {
  tray: ShellCapabilityState;
  closeToHide: ShellCapabilityState;
  singleInstance: ShellCapabilityState;
  deepLinks: ShellCapabilityState;
  notifications: ShellCapabilityState;
}

export type ShellEvent =
  | { type: 'visibility_changed'; visible: boolean; source: ShellEventSource }
  | { type: 'quit_requested'; source: ShellEventSource }
  | { type: 'deep_link_received'; url: string; source: 'operating-system' }
  | { type: 'notification_activated'; id: string; source: 'operating-system' };

export interface ShellClient {
  readonly backend: ShellBackend;
  readonly capabilities: Readonly<ShellCapabilities>;
  show(): Promise<void>;
  hide(): Promise<void>;
  quit(): Promise<void>;
  setTrayVisualState(state: TrayVisualState): Promise<void>;
  subscribe(listener: (event: ShellEvent) => void): Promise<() => void>;
}

export type DesktopAuthErrorCode =
  | 'AUTH_CONFIG_UNAVAILABLE'
  | 'AUTH_HELPER_UNAVAILABLE'
  | 'AUTH_HELPER_UNTRUSTED'
  | 'AUTH_HELPER_FAILED'
  | 'AUTH_INVALID_REQUEST'
  | 'AUTH_INVALID_EMAIL'
  | 'AUTH_INVALID_OTP'
  | 'AUTH_MFA_REQUIRED'
  | 'AUTH_REQUEST_FAILED'
  | 'AUTH_VERIFY_FAILED'
  | 'AUTH_TOKEN_INVALID'
  | 'AUTH_TOKEN_SAVE_FAILED'
  | 'AUTH_UNSUPPORTED_PLATFORM';

export class DesktopAuthError extends Error {
  constructor(readonly code: DesktopAuthErrorCode) {
    super(code);
    this.name = 'DesktopAuthError';
  }
}

export interface DesktopAuthContext {
  email: string;
}

export interface DesktopAuthClient {
  context(): Promise<DesktopAuthContext>;
  requestOtp(email: string): Promise<void>;
  verifyOtp(email: string, code: string): Promise<void>;
}

export type DesktopEnrollmentErrorCode =
  | 'ENROLLMENT_CONFIG_UNAVAILABLE'
  | 'ENROLLMENT_HELPER_UNAVAILABLE'
  | 'ENROLLMENT_HELPER_UNTRUSTED'
  | 'ENROLLMENT_HELPER_FAILED'
  | 'ENROLLMENT_INVALID_REQUEST'
  | 'ENROLLMENT_INVALID_URL'
  | 'ENROLLMENT_KEY_FAILED'
  | 'ENROLLMENT_REQUEST_FAILED'
  | 'ENROLLMENT_RESPONSE_INVALID'
  | 'ENROLLMENT_SAVE_FAILED'
  | 'ENROLLMENT_UNSUPPORTED_PLATFORM';

export class DesktopEnrollmentError extends Error {
  constructor(readonly code: DesktopEnrollmentErrorCode) {
    super(code);
    this.name = 'DesktopEnrollmentError';
  }
}

export interface DesktopEnrollmentContext {
  enrolled: boolean;
  email: string;
}

export interface DesktopEnrollmentClient {
  context(): Promise<DesktopEnrollmentContext>;
  enroll(url: string): Promise<DesktopEnrollmentContext>;
}

export interface DesktopBootstrapClients {
  desktopIpcClient: DesktopIpcClient;
  desktopAuthClient: DesktopAuthClient;
  desktopEnrollmentClient: DesktopEnrollmentClient;
  shellClient: ShellClient;
  scenarioClient?: DesktopScenarioClient;
}

/** Contract-only parity marker. No Wails runtime or toolkit-gate result is claimed. */
export const WAILS_PARITY_SCAFFOLD: Readonly<ShellCapabilities> = Object.freeze({
  tray: 'not_implemented',
  closeToHide: 'not_implemented',
  singleInstance: 'not_implemented',
  deepLinks: 'not_implemented',
  notifications: 'not_implemented',
});
