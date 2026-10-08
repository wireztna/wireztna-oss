import { invoke as tauriInvoke, isTauri } from '@tauri-apps/api/core';
import { listen as tauriListen, type Event, type UnlistenFn } from '@tauri-apps/api/event';
import { DesktopIpcError } from '../ipc/contract';
import type {
  CommandEnvelope,
  DesktopIpcClient,
  DesktopSubscription,
  HandshakeResponse,
  OperationAccepted,
  SnapshotBaseline,
  StreamIdentity,
  SubscribeOptions,
} from '../ipc/contract';
import {
  createWireGetSnapshotRequestV2,
  createWireSubscribeRequestV2,
  fromWireEventEnvelopeV2,
  fromWireMutationResponseV2,
  fromWireServerHelloV2,
  fromWireSnapshotResponseV2,
  fromWireSubscribeResponseV2,
  toWireClientHelloV2,
  toWireCommandRequestV2,
} from '../ipc/wire-v2';
import { DesktopAuthError, DesktopEnrollmentError } from './contract';
import type {
  DesktopAuthClient,
  DesktopAuthContext,
  DesktopAuthErrorCode,
  DesktopBootstrapClients,
  DesktopEnrollmentClient,
  DesktopEnrollmentContext,
  DesktopEnrollmentErrorCode,
  ShellCapabilities,
  ShellClient,
  ShellEvent,
  ShellEventSource,
  TrayVisualState,
} from './contract';

const IPC_EVENT_PREFIX = 'wireztna://ipc-event/';
const IPC_ERROR_PREFIX = 'wireztna://ipc-error/';
const SHELL_SHOWN_EVENT = 'wireztna://shell-shown';
const SHELL_HIDDEN_EVENT = 'wireztna://shell-hidden';
const SHELL_QUITTING_EVENT = 'wireztna://shell-quitting';

const NATIVE_FAILURE_CODES = [
  'IPC_HELPER_NOT_FOUND',
  'IPC_HELPER_NOT_RUNNING',
  'IPC_HELPER_PERMISSION_DENIED',
  'IPC_HELPER_UNTRUSTED',
  'IPC_PERMISSION_DENIED',
  'IPC_UNSUPPORTED_PLATFORM',
  'IPC_SHUTDOWN',
  'IPC_PENDING_LIMIT',
  'IPC_REQUEST_IN_PROGRESS',
  'IPC_CANCELED',
  'IPC_CONNECT_FAILED',
  'IPC_TIMEOUT',
  'IPC_FRAME_TOO_LARGE',
  'IPC_TRUNCATED_FRAME',
  'IPC_INVALID_JSON',
  'IPC_INVALID_MESSAGE',
  'IPC_IO_FAILED',
  'IPC_CHANNEL_NOT_NEGOTIATED',
  'IPC_INVALID_ARGUMENT',
  'IPC_EVENT_DELIVERY_FAILED',
  'IPC_INTERNAL',
] as const;

type NativeFailureCode = typeof NATIVE_FAILURE_CODES[number];

export class TauriIpcBridgeError extends Error {
  constructor(readonly code: NativeFailureCode | 'IPC_NATIVE_FAILURE') {
    super(code);
    this.name = 'TauriIpcBridgeError';
  }
}

const TAURI_CAPABILITIES: Readonly<ShellCapabilities> = Object.freeze({
  tray: 'available',
  closeToHide: 'available',
  singleInstance: 'not_implemented',
  deepLinks: 'not_implemented',
  notifications: 'not_implemented',
});

type Invoke = <T>(command: string, args?: Record<string, unknown>) => Promise<T>;
type Listen = <T>(event: string, handler: (event: Event<T>) => void) => Promise<UnlistenFn>;

export interface TauriTransport {
  invoke: Invoke;
  listen: Listen;
  onListenError?: (error: unknown) => void;
}

const defaultTransport: TauriTransport = {
  invoke: tauriInvoke,
  listen: tauriListen,
};

function normalizeNativeFailure(error: unknown): TauriIpcBridgeError {
  if (typeof error !== 'object' || error === null || Array.isArray(error)) {
    return new TauriIpcBridgeError('IPC_NATIVE_FAILURE');
  }
  const object = error as Record<string, unknown>;
  if (Object.keys(object).length !== 1 || typeof object.code !== 'string'
    || !NATIVE_FAILURE_CODES.includes(object.code as NativeFailureCode)) {
    return new TauriIpcBridgeError('IPC_NATIVE_FAILURE');
  }
  return new TauriIpcBridgeError(object.code as NativeFailureCode);
}

async function invokeNative<T>(
  transport: TauriTransport,
  command: string,
  args?: Record<string, unknown>,
): Promise<T> {
  try {
    return await transport.invoke<T>(command, args);
  } catch (error) {
    throw normalizeNativeFailure(error);
  }
}

async function subscribePush<T>(
  transport: TauriTransport,
  eventName: string,
  listener: (payload: T) => void,
): Promise<() => void> {
  let active = true;
  const unlisten = await transport.listen<T>(eventName, (event) => {
    if (active) listener(event.payload);
  });

  return () => {
    if (!active) return;
    active = false;
    unlisten();
  };
}

export function ipcEventName(requestId: string): string {
  return `${IPC_EVENT_PREFIX}${requestId}`;
}

export function ipcErrorName(requestId: string): string {
  return `${IPC_ERROR_PREFIX}${requestId}`;
}

export type TauriDesktopIpcReadiness = 'available' | 'not_implemented';

/** Native commands are registered by src-tauri/src/main.rs. */
export const TAURI_DESKTOP_IPC_READINESS: TauriDesktopIpcReadiness = 'available';
export const TAURI_DESKTOP_IPC_MILESTONE = 'DX-01/IPC v2' as const;

export class TauriDesktopIpcClient implements DesktopIpcClient {
  private commandIdentity: StreamIdentity | null = null;

  constructor(private readonly transport: TauriTransport = defaultTransport) {}

  async handshake(): Promise<HandshakeResponse> {
    this.commandIdentity = null;
    try {
      const wire = await invokeNative<unknown>(this.transport, 'desktop_ipc_handshake', {
        hello: toWireClientHelloV2('command'),
      });
      const hello = fromWireServerHelloV2(wire);
      this.commandIdentity = { streamId: hello.streamId, epoch: hello.epoch };
      return hello;
    } catch (error) {
      this.commandIdentity = null;
      throw error;
    }
  }

  async getSnapshot(): Promise<SnapshotBaseline> {
    if (!this.commandIdentity) throw new TauriIpcBridgeError('IPC_CHANNEL_NOT_NEGOTIATED');
    const request = createWireGetSnapshotRequestV2(
      crypto.randomUUID(),
      new Date(Date.now() + 30_000).toISOString(),
    );
    try {
      const wire = await invokeNative<unknown>(this.transport, 'desktop_ipc_request', { request });
      return fromWireSnapshotResponseV2(wire, request, this.commandIdentity);
    } catch (error) {
      if (!(error instanceof DesktopIpcError)) this.commandIdentity = null;
      throw error;
    }
  }

  async request(envelope: CommandEnvelope): Promise<OperationAccepted> {
    if (!this.commandIdentity) throw new TauriIpcBridgeError('IPC_CHANNEL_NOT_NEGOTIATED');
    const request = toWireCommandRequestV2(envelope);
    const wire = await invokeNative<unknown>(this.transport, 'desktop_ipc_request', { request });
    return fromWireMutationResponseV2(wire, request);
  }

  async subscribe(options: SubscribeOptions): Promise<DesktopSubscription> {
    const request = createWireSubscribeRequestV2(
      crypto.randomUUID(),
      new Date(Date.now() + 24 * 60 * 60 * 1_000).toISOString(),
      options,
    );
    let active = true;
    let nativeChannelOpen = false;
    let ackValidated = false;
    let streamFailure: unknown = null;
    let cancelPromise: Promise<void> | null = null;
    const pendingDeliveries: Array<{ kind: 'event' | 'error'; payload: unknown }> = [];
    const unsubscribers: UnlistenFn[] = [];
    const unlistenAll = () => {
      while (unsubscribers.length > 0) unsubscribers.pop()?.();
    };
    const cancelNative = (): Promise<void> => {
      if (!nativeChannelOpen) return Promise.resolve();
      cancelPromise ??= invokeNative<void>(this.transport, 'desktop_ipc_cancel_subscription', {
        requestId: request.request_id,
      });
      return cancelPromise;
    };
    const fail = (error: unknown) => {
      if (!active) return;
      streamFailure = error;
      active = false;
      unlistenAll();
      options.onError(error);
      void cancelNative().catch(() => undefined);
    };
    const deliver = (delivery: { kind: 'event' | 'error'; payload: unknown }) => {
      if (!active) return;
      if (!ackValidated) {
        pendingDeliveries.push(delivery);
        return;
      }
      if (delivery.kind === 'error') {
        fail(normalizeNativeFailure(delivery.payload));
        return;
      }
      try {
        options.listener(fromWireEventEnvelopeV2(delivery.payload, {
          requestId: request.request_id,
          streamId: options.streamId,
          epoch: options.epoch,
        }));
      } catch (error) {
        fail(error);
      }
    };
    const closeAfterFailure = async (error: unknown): Promise<never> => {
      active = false;
      pendingDeliveries.length = 0;
      unlistenAll();
      try {
        await cancelNative();
      } catch {
        // Preserve the protocol/listener failure that made this channel unusable.
      }
      throw error;
    };

    try {
      const helloWire = await invokeNative<unknown>(this.transport, 'desktop_ipc_handshake', {
        hello: toWireClientHelloV2('event'),
        requestId: request.request_id,
      });
      nativeChannelOpen = true;
      const eventHello = fromWireServerHelloV2(helloWire);
      if (eventHello.streamId !== options.streamId || eventHello.epoch !== options.epoch) {
        throw new DesktopIpcError({ code: 'RESYNC_REQUIRED' });
      }
    } catch (error) {
      return closeAfterFailure(error);
    }

    try {
      unsubscribers.push(await this.transport.listen<unknown>(ipcEventName(request.request_id), (event) => {
        deliver({ kind: 'event', payload: event.payload });
      }));
      unsubscribers.push(await this.transport.listen<unknown>(ipcErrorName(request.request_id), (event) => {
        deliver({ kind: 'error', payload: event.payload });
      }));
    } catch (error) {
      this.transport.onListenError?.(error);
      return closeAfterFailure(normalizeNativeFailure(error));
    }

    try {
      const responseWire = await invokeNative<unknown>(this.transport, 'desktop_ipc_request', { request });
      fromWireSubscribeResponseV2(responseWire, request);
      ackValidated = true;
      while (active && pendingDeliveries.length > 0) {
        deliver(pendingDeliveries.shift()!);
      }
      if (!active) {
        await cancelNative();
        throw streamFailure instanceof Error
          ? streamFailure
          : new TauriIpcBridgeError('IPC_NATIVE_FAILURE');
      }
      return {
        streamId: options.streamId,
        epoch: options.epoch,
        close: async () => {
          if (active) {
            active = false;
            pendingDeliveries.length = 0;
            unlistenAll();
          }
          await cancelNative();
        },
      };
    } catch (error) {
      return closeAfterFailure(error);
    }
  }
}

export class TauriShellClient implements ShellClient {
  readonly backend = 'tauri' as const;
  readonly capabilities = TAURI_CAPABILITIES;

  constructor(private readonly transport: TauriTransport = defaultTransport) {}

  show(): Promise<void> {
    return this.transport.invoke('shell_show');
  }

  hide(): Promise<void> {
    return this.transport.invoke('shell_hide');
  }

  quit(): Promise<void> {
    return this.transport.invoke('shell_quit');
  }

  setTrayVisualState(state: TrayVisualState): Promise<void> {
    return this.transport.invoke('shell_set_tray_visual_state', { state });
  }

  async subscribe(listener: (event: ShellEvent) => void): Promise<() => void> {
    const unsubscribers: Array<() => void> = [];
    try {
      unsubscribers.push(await subscribePush<ShellEventSource>(this.transport, SHELL_SHOWN_EVENT, (source) => {
        listener({ type: 'visibility_changed', visible: true, source });
      }));
      unsubscribers.push(await subscribePush<ShellEventSource>(this.transport, SHELL_HIDDEN_EVENT, (source) => {
        listener({ type: 'visibility_changed', visible: false, source });
      }));
      unsubscribers.push(await subscribePush<ShellEventSource>(this.transport, SHELL_QUITTING_EVENT, (source) => {
        listener({ type: 'quit_requested', source });
      }));
    } catch (error) {
      unsubscribers.forEach((unsubscribe) => unsubscribe());
      this.transport.onListenError?.(error);
      throw error;
    }
    return () => unsubscribers.forEach((unsubscribe) => unsubscribe());
  }
}

const DESKTOP_AUTH_ERROR_CODES = [
  'AUTH_CONFIG_UNAVAILABLE',
  'AUTH_HELPER_UNAVAILABLE',
  'AUTH_HELPER_UNTRUSTED',
  'AUTH_HELPER_FAILED',
  'AUTH_INVALID_REQUEST',
  'AUTH_INVALID_EMAIL',
  'AUTH_INVALID_OTP',
  'AUTH_MFA_REQUIRED',
  'AUTH_REQUEST_FAILED',
  'AUTH_VERIFY_FAILED',
  'AUTH_TOKEN_INVALID',
  'AUTH_TOKEN_SAVE_FAILED',
  'AUTH_UNSUPPORTED_PLATFORM',
] as const satisfies readonly DesktopAuthErrorCode[];

function decodeDesktopAuthResponse(value: unknown): {
  success: boolean;
  email?: string;
  error?: DesktopAuthErrorCode;
} {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new DesktopAuthError('AUTH_HELPER_FAILED');
  }
  const response = value as Record<string, unknown>;
  const keys = Object.keys(response);
  if (keys.some((key) => !['success', 'email', 'error'].includes(key))
    || typeof response.success !== 'boolean'
    || (response.email !== undefined && typeof response.email !== 'string')) {
    throw new DesktopAuthError('AUTH_HELPER_FAILED');
  }
  if (response.success) {
    if (response.error !== undefined) throw new DesktopAuthError('AUTH_HELPER_FAILED');
    return { success: true, ...(response.email === undefined ? {} : { email: response.email }) };
  }
  if (typeof response.error !== 'string'
    || !DESKTOP_AUTH_ERROR_CODES.includes(response.error as DesktopAuthErrorCode)
    || response.email !== undefined) {
    throw new DesktopAuthError('AUTH_HELPER_FAILED');
  }
  return { success: false, error: response.error as DesktopAuthErrorCode };
}

export class TauriDesktopAuthClient implements DesktopAuthClient {
  constructor(private readonly transport: TauriTransport = defaultTransport) {}

  async context(): Promise<DesktopAuthContext> {
    const response = await this.invoke({ action: 'context' });
    return { email: response.email ?? '' };
  }

  async requestOtp(email: string): Promise<void> {
    await this.invoke({ action: 'request_otp', email });
  }

  async verifyOtp(email: string, code: string): Promise<void> {
    await this.invoke({ action: 'verify_otp', email, code });
  }

  private async invoke(request: Record<string, string>): Promise<{ email?: string }> {
    let wire: unknown;
    try {
      wire = await this.transport.invoke<unknown>('desktop_auth', { request });
    } catch {
      throw new DesktopAuthError('AUTH_HELPER_FAILED');
    }
    const response = decodeDesktopAuthResponse(wire);
    if (!response.success) throw new DesktopAuthError(response.error!);
    return { ...(response.email === undefined ? {} : { email: response.email }) };
  }
}

const DESKTOP_ENROLLMENT_ERROR_CODES = [
  'ENROLLMENT_CONFIG_UNAVAILABLE',
  'ENROLLMENT_HELPER_UNAVAILABLE',
  'ENROLLMENT_HELPER_UNTRUSTED',
  'ENROLLMENT_HELPER_FAILED',
  'ENROLLMENT_INVALID_REQUEST',
  'ENROLLMENT_INVALID_URL',
  'ENROLLMENT_KEY_FAILED',
  'ENROLLMENT_REQUEST_FAILED',
  'ENROLLMENT_RESPONSE_INVALID',
  'ENROLLMENT_SAVE_FAILED',
  'ENROLLMENT_UNSUPPORTED_PLATFORM',
] as const satisfies readonly DesktopEnrollmentErrorCode[];

function decodeDesktopEnrollmentResponse(value: unknown): DesktopEnrollmentContext & { success: true } | {
  success: false;
  error: DesktopEnrollmentErrorCode;
} {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new DesktopEnrollmentError('ENROLLMENT_HELPER_FAILED');
  }
  const response = value as Record<string, unknown>;
  const keys = Object.keys(response);
  if (keys.some((key) => !['success', 'enrolled', 'email', 'error'].includes(key))
    || typeof response.success !== 'boolean') {
    throw new DesktopEnrollmentError('ENROLLMENT_HELPER_FAILED');
  }
  if (response.success) {
    if (typeof response.enrolled !== 'boolean' || response.error !== undefined
      || (response.email !== undefined && typeof response.email !== 'string')
      || (response.enrolled && (typeof response.email !== 'string' || response.email.length === 0))
      || (!response.enrolled && response.email !== undefined)) {
      throw new DesktopEnrollmentError('ENROLLMENT_HELPER_FAILED');
    }
    return { success: true, enrolled: response.enrolled, email: (response.email as string | undefined) ?? '' };
  }
  if (typeof response.error !== 'string'
    || !DESKTOP_ENROLLMENT_ERROR_CODES.includes(response.error as DesktopEnrollmentErrorCode)
    || response.enrolled !== undefined || response.email !== undefined) {
    throw new DesktopEnrollmentError('ENROLLMENT_HELPER_FAILED');
  }
  return { success: false, error: response.error as DesktopEnrollmentErrorCode };
}

export class TauriDesktopEnrollmentClient implements DesktopEnrollmentClient {
  constructor(private readonly transport: TauriTransport = defaultTransport) {}

  context(): Promise<DesktopEnrollmentContext> {
    return this.invoke({ action: 'context' });
  }

  enroll(url: string): Promise<DesktopEnrollmentContext> {
    return this.invoke({ action: 'enroll', url });
  }

  private async invoke(request: Record<string, string>): Promise<DesktopEnrollmentContext> {
    let wire: unknown;
    try {
      wire = await this.transport.invoke<unknown>('desktop_enroll', { request });
    } catch {
      throw new DesktopEnrollmentError('ENROLLMENT_HELPER_FAILED');
    }
    const response = decodeDesktopEnrollmentResponse(wire);
    if (!response.success) throw new DesktopEnrollmentError(response.error);
    return { enrolled: response.enrolled, email: response.email };
  }
}

function currentDesktopIpcReadiness(): TauriDesktopIpcReadiness {
  return TAURI_DESKTOP_IPC_READINESS;
}

export function createTauriBootstrapClients(transport: TauriTransport = defaultTransport): DesktopBootstrapClients {
  if (currentDesktopIpcReadiness() !== 'available') {
    throw new Error('DESKTOP_IPC_NOT_READY');
  }
  return {
    desktopIpcClient: new TauriDesktopIpcClient(transport),
    desktopAuthClient: new TauriDesktopAuthClient(transport),
    desktopEnrollmentClient: new TauriDesktopEnrollmentClient(transport),
    shellClient: new TauriShellClient(transport),
  };
}

export function hasTauriRuntime(): boolean {
  return isTauri();
}
