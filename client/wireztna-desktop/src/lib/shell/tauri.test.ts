import type { Event, UnlistenFn } from '@tauri-apps/api/event';
import { describe, expect, it, vi } from 'vitest';
import type { WireRequestV2 } from '../ipc/wire-v2';
import {
  TauriDesktopIpcClient,
  TauriIpcBridgeError,
  TauriShellClient,
  ipcErrorName,
  ipcEventName,
  type TauriTransport,
} from './tauri';

class FakeTauriTransport implements TauriTransport {
  readonly invocations: Array<{ command: string; args?: Record<string, unknown> }> = [];
  readonly listenOrder: string[] = [];
  private readonly handlers = new Map<string, Set<(event: Event<unknown>) => void>>();
  responder: (command: string, args?: Record<string, unknown>) => unknown | Promise<unknown> = () => undefined;
  listenFailureAt: number | null = null;

  async invoke<T>(command: string, args?: Record<string, unknown>): Promise<T> {
    this.invocations.push({ command, args });
    return await this.responder(command, args) as T;
  }

  async listen<T>(event: string, handler: (event: Event<T>) => void): Promise<UnlistenFn> {
    this.listenOrder.push(event);
    if (this.listenFailureAt === this.listenOrder.length) throw new Error('LISTEN_FAILED');
    const handlers = this.handlers.get(event) ?? new Set();
    const untyped = handler as (event: Event<unknown>) => void;
    handlers.add(untyped);
    this.handlers.set(event, handlers);
    return () => handlers.delete(untyped);
  }

  emit<T>(event: string, payload: T): void {
    for (const handler of this.handlers.get(event) ?? []) {
      handler({ event, id: 1, payload } as Event<unknown>);
    }
  }

  listenerCount(event: string): number {
    return this.handlers.get(event)?.size ?? 0;
  }
}

const hello = {
  kind: 'hello',
  negotiated_version: 2,
  service_version: 'wireztna-core/2.0',
  capabilities: [
    'strict_framing',
    'controller_state',
    'idempotency',
    'event_replay',
    'authorization_policy',
    'connection_catalog',
  ],
  compatibility: 'current',
  stream_id: 'stream-v2',
  epoch: 7,
};

function response(request: WireRequestV2, extra: Record<string, unknown> = {}) {
  return {
    protocol_version: 2,
    request_id: request.request_id,
    idempotency_key: request.idempotency_key,
    operation_id: '',
    success: true,
    ...extra,
  };
}

const disconnectedSnapshot = {
  state: 'disconnected',
  desired: { configuration_id: '', generation: 0, group_id: '', connected: false },
  applied: { configuration_id: '', generation: 0, group_id: '', connected: false },
  health: {
    healthy: false,
    wireguard: 'unknown',
    routes: 'unknown',
    dns: 'unknown',
    end_to_end: 'unknown',
  },
  stream_id: 'stream-v2',
  epoch: 7,
  sequence: 0,
};

function snapshotChangedEnvelope(requestId: string) {
  return {
    protocol_version: 2,
    request_id: requestId,
    stream_id: 'stream-v2',
    epoch: 7,
    event: {
      sequence: 1,
      kind: 'snapshot_changed',
      operation_id: '',
      occurred_at: '2030-01-01T00:00:00.000Z',
    },
  };
}

describe('Tauri desktop IPC adapter', () => {
  it('sends exact handshake and request args over the managed command channel', async () => {
    const transport = new FakeTauriTransport();
    transport.responder = (command, args) => {
      if (command === 'desktop_ipc_handshake') return hello;
      const request = args?.request as WireRequestV2;
      if (request.command === 'get_snapshot') return response(request, { snapshot: disconnectedSnapshot });
      return response(request, { operation_id: 'operation-1' });
    };
    const client = new TauriDesktopIpcClient(transport);

    await client.handshake();
    await client.getSnapshot();
    await client.request({
      protocolVersion: 2,
      requestId: 'mutation-request',
      idempotencyKey: 'mutation-key',
      deadline: '2030-01-01T00:00:00.000Z',
      command: 'disconnect',
      payload: {},
    });

    expect(transport.invocations[0]).toEqual({
      command: 'desktop_ipc_handshake',
      args: {
        hello: {
          kind: 'hello',
          channel: 'command',
          supported_versions: [2],
          capabilities: hello.capabilities,
          required_capabilities: hello.capabilities,
        },
      },
    });
    expect(transport.invocations[1].args?.request).toMatchObject({
      protocol_version: 2,
      idempotency_key: '',
      command: 'get_snapshot',
      payload: {},
    });
    expect(transport.invocations[2]).toEqual({
      command: 'desktop_ipc_request',
      args: {
        request: {
          protocol_version: 2,
          request_id: 'mutation-request',
          idempotency_key: 'mutation-key',
          deadline: '2030-01-01T00:00:00.000Z',
          command: 'disconnect',
          payload: {},
        },
      },
    });
  });

  it('listens on request-scoped events before subscribe and cancels exactly once', async () => {
    const transport = new FakeTauriTransport();
    let subscribeRequest: WireRequestV2 | undefined;
    transport.responder = (command, args) => {
      if (command === 'desktop_ipc_handshake') return hello;
      if (command === 'desktop_ipc_cancel_subscription') return undefined;
      const request = args?.request as WireRequestV2;
      subscribeRequest = request;
      expect(transport.listenOrder).toEqual([
        ipcEventName(request.request_id),
        ipcErrorName(request.request_id),
      ]);
      return response(request);
    };
    const client = new TauriDesktopIpcClient(transport);
    const events: unknown[] = [];
    const errors: unknown[] = [];

    const subscription = await client.subscribe({
      streamId: 'stream-v2',
      epoch: 7,
      afterSequence: 0,
      listener: (event) => events.push(event),
      onError: (error) => errors.push(error),
    });
    const requestId = subscribeRequest!.request_id;
    transport.emit(ipcEventName(requestId), {
      protocol_version: 2,
      request_id: requestId,
      stream_id: 'stream-v2',
      epoch: 7,
      event: {
        sequence: 1,
        kind: 'snapshot_changed',
        operation_id: '',
        occurred_at: '2030-01-01T00:00:00.000Z',
      },
    });

    await subscription.close();
    await subscription.close();
    transport.emit(ipcEventName(requestId), {
      protocol_version: 2,
      request_id: requestId,
      stream_id: 'stream-v2',
      epoch: 7,
      error: { code: 'RESYNC_REQUIRED' },
    });

    expect(events).toHaveLength(1);
    expect(errors).toEqual([]);
    expect(transport.listenerCount(ipcEventName(requestId))).toBe(0);
    expect(transport.listenerCount(ipcErrorName(requestId))).toBe(0);
    expect(transport.invocations.filter(({ command }) => command === 'desktop_ipc_cancel_subscription')).toEqual([
      { command: 'desktop_ipc_cancel_subscription', args: { requestId } },
    ]);
  });

  it('buffers pre-ACK events and discards them when the strict ACK is invalid', async () => {
    const transport = new FakeTauriTransport();
    const events: unknown[] = [];
    const errors: unknown[] = [];
    let requestId = '';
    transport.responder = (command, args) => {
      if (command === 'desktop_ipc_handshake') {
        requestId = args?.requestId as string;
        return hello;
      }
      if (command === 'desktop_ipc_cancel_subscription') return undefined;
      const request = args?.request as WireRequestV2;
      transport.emit(ipcEventName(request.request_id), snapshotChangedEnvelope(request.request_id));
      return { ...response(request), unexpected: true };
    };
    const client = new TauriDesktopIpcClient(transport);

    await expect(client.subscribe({
      streamId: 'stream-v2',
      epoch: 7,
      afterSequence: 0,
      listener: (event) => events.push(event),
      onError: (error) => errors.push(error),
    })).rejects.toThrow('IPC_V2_INVALID_WIRE_PAYLOAD');

    expect(events).toEqual([]);
    expect(errors).toEqual([]);
    expect(transport.invocations.filter(({ command }) => command === 'desktop_ipc_cancel_subscription')).toEqual([
      { command: 'desktop_ipc_cancel_subscription', args: { requestId } },
    ]);
  });

  it('cancels the owned event channel when scoped listener setup fails', async () => {
    const transport = new FakeTauriTransport();
    transport.listenFailureAt = 2;
    let requestId = '';
    transport.responder = (command, args) => {
      if (command === 'desktop_ipc_handshake') {
        requestId = args?.requestId as string;
        return hello;
      }
      if (command === 'desktop_ipc_cancel_subscription') return undefined;
      throw new Error(`UNEXPECTED_INVOKE:${command}`);
    };
    const client = new TauriDesktopIpcClient(transport);

    await expect(client.subscribe({
      streamId: 'stream-v2',
      epoch: 7,
      afterSequence: 0,
      listener: vi.fn(),
      onError: vi.fn(),
    })).rejects.toThrow('IPC_NATIVE_FAILURE');

    expect(transport.invocations.filter(({ command }) => command === 'desktop_ipc_request')).toEqual([]);
    expect(transport.listenerCount(ipcEventName(requestId))).toBe(0);
    expect(transport.invocations.filter(({ command }) => command === 'desktop_ipc_cancel_subscription')).toEqual([
      { command: 'desktop_ipc_cancel_subscription', args: { requestId } },
    ]);
  });

  it('isolates late events and cancels if an event closes the stream before subscribe returns', async () => {
    const transport = new FakeTauriTransport();
    const errors: unknown[] = [];
    let requestId = '';
    transport.responder = (command, args) => {
      if (command === 'desktop_ipc_handshake') return hello;
      if (command === 'desktop_ipc_cancel_subscription') return undefined;
      const request = args?.request as WireRequestV2;
      requestId = request.request_id;
      transport.emit(ipcEventName(requestId), { unexpected: true });
      return response(request);
    };
    const client = new TauriDesktopIpcClient(transport);

    await expect(client.subscribe({
      streamId: 'stream-v2',
      epoch: 7,
      afterSequence: 0,
      listener: vi.fn(),
      onError: (error) => errors.push(error),
    })).rejects.toThrow('IPC_V2_INVALID_WIRE_PAYLOAD');

    expect(errors).toHaveLength(1);
    expect(transport.invocations.filter(({ command }) => command === 'desktop_ipc_cancel_subscription')).toEqual([
      { command: 'desktop_ipc_cancel_subscription', args: { requestId } },
    ]);
    transport.emit(ipcErrorName(requestId), { code: 'IPC_TIMEOUT' });
    expect(errors).toHaveLength(1);
  });

  it('keeps the negotiated command channel usable after an uncertain native mutation failure', async () => {
    const transport = new FakeTauriTransport();
    let mutationAttempts = 0;
    transport.responder = (command, args) => {
      if (command === 'desktop_ipc_handshake') return hello;
      const request = args?.request as WireRequestV2;
      mutationAttempts += 1;
      if (mutationAttempts === 1) return Promise.reject({ code: 'IPC_IO_FAILED' });
      return response(request, { operation_id: 'operation-retried' });
    };
    const client = new TauriDesktopIpcClient(transport);
    await client.handshake();
    const desired = {
      configurationId: 'service-configuration', generation: 1, groupId: 'group-1', exitNodeId: null, connected: true,
    };

    await expect(client.request({
      protocolVersion: 2,
      requestId: 'attempt-1',
      idempotencyKey: 'stable-key',
      deadline: '2030-01-01T00:00:00.000Z',
      command: 'connect',
      payload: desired,
    })).rejects.toEqual(new TauriIpcBridgeError('IPC_IO_FAILED'));
    await expect(client.request({
      protocolVersion: 2,
      requestId: 'attempt-2',
      idempotencyKey: 'stable-key',
      deadline: '2030-01-01T00:00:01.000Z',
      command: 'connect',
      payload: desired,
    })).resolves.toEqual({ operationId: 'operation-retried' });

    expect(transport.invocations.filter(({ command }) => command === 'desktop_ipc_handshake')).toHaveLength(1);
  });

  it.each([
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
  ])('preserves stable native failure code %s', async (code) => {
    const transport = new FakeTauriTransport();
    transport.responder = () => Promise.reject({ code });
    const client = new TauriDesktopIpcClient(transport);

    await expect(client.handshake()).rejects.toEqual(new TauriIpcBridgeError(code as never));
  });

  it('normalizes unknown native rejections fail-closed', async () => {
    const transport = new FakeTauriTransport();
    transport.responder = () => Promise.reject('socket path leaked');
    const client = new TauriDesktopIpcClient(transport);

    await expect(client.handshake()).rejects.toEqual(new TauriIpcBridgeError('IPC_NATIVE_FAILURE'));
  });
});

describe('Tauri shell adapter', () => {
  it('maps quit to the shell-only command and never to disconnect', async () => {
    const transport = new FakeTauriTransport();
    const shell = new TauriShellClient(transport);

    await shell.show();
    await shell.hide();
    await shell.setTrayVisualState('connected');
    await shell.quit();

    expect(transport.invocations).toEqual([
      { command: 'shell_show', args: undefined },
      { command: 'shell_hide', args: undefined },
      { command: 'shell_set_tray_visual_state', args: { state: 'connected' } },
      { command: 'shell_quit', args: undefined },
    ]);
    expect(transport.invocations.some(({ command }) => command.includes('disconnect'))).toBe(false);
  });

  it('delivers native visibility and quit events without service effects', async () => {
    const transport = new FakeTauriTransport();
    const shell = new TauriShellClient(transport);
    const events: unknown[] = [];
    const close = await shell.subscribe((event) => events.push(event));

    transport.emit('wireztna://shell-hidden', 'window-close');
    transport.emit('wireztna://shell-quitting', 'tray');
    close();

    expect(events).toEqual([
      { type: 'visibility_changed', visible: false, source: 'window-close' },
      { type: 'quit_requested', source: 'tray' },
    ]);
  });
});
