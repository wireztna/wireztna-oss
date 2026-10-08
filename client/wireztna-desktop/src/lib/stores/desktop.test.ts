import { get } from 'svelte/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  DesktopIpcError,
  MockDesktopIpc,
  type CommandEnvelope,
  type ControllerError,
  type DesktopIpcClient,
  type DesktopStatus,
  type IpcEvent,
  type DesktopSubscription,
  type HandshakeResponse,
  type OperationAccepted,
  type SnapshotBaseline,
  type SubscribeOptions,
} from '../ipc/contract';
import { createDesktopStore } from './desktop';

class ControlledClient implements DesktopIpcClient {
  readonly delegate: MockDesktopIpc;
  subscriptionOptions: SubscribeOptions | null = null;
  requests: CommandEnvelope[] = [];
  requestHandler: (envelope: CommandEnvelope) => Promise<OperationAccepted> = async () => ({ operationId: 'operation-1' });
  handshakeCount = 0;
  handshakeGate: Promise<void> | null = null;
  snapshotGate: Promise<SnapshotBaseline> | null = null;

  constructor(initialStatus: DesktopStatus = 'disconnected') {
    this.delegate = new MockDesktopIpc({ initialStatus, transitionDelayMs: 0 });
  }

  async handshake(): Promise<HandshakeResponse> {
    this.handshakeCount += 1;
    if (this.handshakeCount > 1 && this.handshakeGate) await this.handshakeGate;
    return this.delegate.handshake();
  }

  getSnapshot(): Promise<SnapshotBaseline> {
    return this.snapshotGate ?? this.delegate.getSnapshot();
  }

  async subscribe(options: SubscribeOptions): Promise<DesktopSubscription> {
    this.subscriptionOptions = options;
    return {
      streamId: options.streamId,
      epoch: options.epoch,
      close: async () => undefined,
    };
  }

  request(envelope: CommandEnvelope): Promise<OperationAccepted> {
    this.requests.push(envelope);
    return this.requestHandler(envelope);
  }
}

async function flushQueue(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

afterEach(() => {
  vi.useRealTimers();
});

describe('desktop state store', () => {
  it('takes a baseline before subscribing and exposes the negotiated stream', async () => {
    const client = new MockDesktopIpc({ initialStatus: 'disconnected', transitionDelayMs: 0 });
    const store = createDesktopStore(client);

    await store.initialize();

    expect(get(store)).toMatchObject({
      ready: true,
      busy: false,
      lastError: null,
      handshake: { negotiatedVersion: 2, compatibility: 'current' },
      snapshot: { state: 'disconnected', projectId: 'engineering', mode: 'split' },
    });
    store.dispose();
  });

  it('does not resolve a connect at request acceptance and waits for terminal healthy state', async () => {
    const client = new MockDesktopIpc({ initialStatus: 'disconnected', transitionDelayMs: 1 });
    const store = createDesktopStore(client);
    await store.initialize();
    const desired = get(store).snapshot!.desired;

    let resolved = false;
    const connect = store.send({ type: 'connect', desired: { ...desired, connected: true } })
      .then(() => { resolved = true; });
    await Promise.resolve();

    expect(resolved).toBe(false);
    expect(get(store).busy).toBe(true);
    await connect;
    expect(get(store)).toMatchObject({
      busy: false,
      progress: 100,
      snapshot: { state: 'connected', health: { healthy: true } },
    });
    store.dispose();
  });

  it('enables project and exit-node catalogs without enabling unrelated UX capabilities', async () => {
    const store = createDesktopStore(new MockDesktopIpc());
    await store.initialize();

    expect(get(store).snapshot?.uxCapabilities).toEqual({
      enrollment: false,
      authentication: false,
      projectCatalog: true,
      exitNodeCatalog: true,
      diagnostics: false,
    });
    store.dispose();
  });

  it('clears a connected snapshot and handshake after an established stream failure', async () => {
    const client = new ControlledClient('connected');
    const store = createDesktopStore(client);
    await store.initialize();
    expect(get(store).snapshot?.state).toBe('connected');

    client.subscriptionOptions!.onError(new Error('IPC_SHUTDOWN'));
    await flushQueue();

    expect(get(store)).toMatchObject({
      ready: true,
      busy: false,
      snapshot: null,
      handshake: null,
      lastError: 'IPC_SHUTDOWN',
    });
    store.dispose();
  });

  it('invalidates immediately when stream loss races a pending snapshot refresh', async () => {
    const client = new ControlledClient('connected');
    const store = createDesktopStore(client);
    await store.initialize();
    const lateBaseline = await client.delegate.getSnapshot();
    let releaseSnapshot!: (baseline: SnapshotBaseline) => void;
    client.snapshotGate = new Promise<SnapshotBaseline>((resolve) => { releaseSnapshot = resolve; });

    client.subscriptionOptions!.listener({
      streamId: lateBaseline.streamId,
      epoch: lateBaseline.epoch,
      sequence: 1,
      kind: 'snapshot_changed',
      operationId: null,
      state: 'connected',
      occurredAt: '2030-01-01T00:00:00.000Z',
      progress: null,
      error: null,
    });
    await Promise.resolve();
    client.subscriptionOptions!.onError(new Error('IPC_SHUTDOWN'));

    expect(get(store)).toMatchObject({ snapshot: null, handshake: null });
    releaseSnapshot(lateBaseline);
    await flushQueue();
    expect(get(store).snapshot).toBeNull();
    store.dispose();
  });

  it('invalidates stale state during RESYNC and restores only a fresh baseline', async () => {
    const client = new ControlledClient();
    const store = createDesktopStore(client);
    await store.initialize();
    let releaseHandshake!: () => void;
    client.handshakeGate = new Promise<void>((resolve) => { releaseHandshake = resolve; });

    client.subscriptionOptions!.onError(new DesktopIpcError({ code: 'RESYNC_REQUIRED' }));
    await flushQueue();

    expect(get(store)).toMatchObject({ ready: false, snapshot: null, handshake: null });
    releaseHandshake();
    await flushQueue();
    await flushQueue();

    expect(get(store)).toMatchObject({ ready: true, snapshot: { state: 'disconnected' }, handshake: { negotiatedVersion: 2 } });
    expect(client.handshakeCount).toBe(2);
    store.dispose();
  });

  it('retains a disconnect idempotency key while a recovered baseline shows it active', async () => {
    const client = new ControlledClient('connected');
    client.requestHandler = async () => { throw new Error('IPC_IO_FAILED'); };
    const store = createDesktopStore(client);
    await store.initialize();

    await expect(store.send({ type: 'disconnect' })).rejects.toThrow('IPC_IO_FAILED');
    const baseline = await client.delegate.getSnapshot();
    client.snapshotGate = Promise.resolve({
      ...baseline,
      snapshot: {
        ...baseline.snapshot,
        activeOperation: {
          id: 'disconnect-active',
          command: { kind: 'disconnect', desired: baseline.snapshot.desired },
          startedAt: '2030-01-01T00:00:00.000Z',
        },
      },
    });
    client.subscriptionOptions!.onError(new DesktopIpcError({ code: 'RESYNC_REQUIRED' }));
    await flushQueue();
    await flushQueue();

    await expect(store.send({ type: 'disconnect' })).rejects.toThrow('IPC_IO_FAILED');
    expect(client.requests).toHaveLength(2);
    expect(client.requests[1].idempotencyKey).toBe(client.requests[0].idempotencyKey);
    store.dispose();
  });

  it('reuses an idempotency key after uncertain failure with a fresh request ID and deadline', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2030-01-01T00:00:00.000Z'));
    const client = new ControlledClient();
    client.requestHandler = async () => { throw new Error('IPC_IO_FAILED'); };
    const store = createDesktopStore(client);
    await store.initialize();
    const desired = get(store).snapshot!.desired;
    const command = { type: 'connect', desired: { ...desired, connected: true } } as const;

    await expect(store.send(command)).rejects.toThrow('IPC_IO_FAILED');
    vi.setSystemTime(new Date('2030-01-01T00:00:01.000Z'));
    const refreshedCommand = {
      ...command,
      desired: { ...command.desired, configurationId: 'refreshed-by-service', generation: 2 },
    } as const;
    await expect(store.send(refreshedCommand)).rejects.toThrow('IPC_IO_FAILED');

    expect(client.requests).toHaveLength(2);
    expect(client.requests[1].idempotencyKey).toBe(client.requests[0].idempotencyKey);
    expect(client.requests[1].requestId).not.toBe(client.requests[0].requestId);
    expect(client.requests[1].deadline).not.toBe(client.requests[0].deadline);
    store.dispose();
  });

  it('keeps an accepted operation pending until its terminal event arrives', async () => {
    vi.useFakeTimers();
    const client = new ControlledClient();
    const store = createDesktopStore(client, null, { operationTimeoutMs: 25 });
    await store.initialize();
    const desired = get(store).snapshot!.desired;

    let settled = false;
    const pending = store.send({ type: 'connect', desired: { ...desired, connected: true } });
    void pending.then(() => { settled = true; }, () => { settled = true; });
    const failed = expect(pending).rejects.toThrow('DEGRADED');
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(250);

    expect(settled).toBe(false);
    expect(get(store)).toMatchObject({ busy: true, lastError: null });
    emitLifecycleEvent(client, 1, 'operation_failed', 'operation-1', {
      code: 'DEGRADED',
      detail: 'handshake did not converge',
    });
    await flushQueue();

    await failed;
    expect(get(store)).toMatchObject({
      busy: false,
      lastError: 'DEGRADED',
      lastControllerError: { code: 'DEGRADED', detail: 'handshake did not converge' },
    });
    store.dispose();
  });
});

function emitLifecycleEvent(
  client: ControlledClient,
  sequence: number,
  kind: IpcEvent['kind'],
  operationId: string | null,
  error: ControllerError | null = null,
): void {
  const subscription = client.subscriptionOptions!;
  subscription.listener({
    streamId: subscription.streamId,
    epoch: subscription.epoch,
    sequence,
    kind,
    operationId,
    state: 'degraded',
    occurredAt: `2030-01-01T00:00:0${sequence}.000Z`,
    progress: null,
    error,
  });
}

describe('desktop controller error state', () => {
  it('preserves every automatic terminal error and prefers its detail in Activity', async () => {
    const client = new ControlledClient('degraded');
    const store = createDesktopStore(client);
    await store.initialize();
    const error: ControllerError = { code: 'DEGRADED', detail: 'Publisher handshake expired.' };

    emitLifecycleEvent(client, 1, 'operation_failed', 'automatic-reconcile-1', error);
    await flushQueue();

    expect(get(store)).toMatchObject({
      lastError: 'DEGRADED',
      lastControllerError: error,
      activities: [{ code: 'operation_failed', detail: 'Publisher handshake expired.' }],
    });
    store.dispose();
  });

  it('keeps the terminal error across degraded snapshots and clears it when a mutation starts', async () => {
    const client = new ControlledClient('degraded');
    const store = createDesktopStore(client);
    await store.initialize();
    emitLifecycleEvent(client, 1, 'operation_failed', 'automatic-renew-1', {
      code: 'SERVICE_UNAVAILABLE',
      detail: 'Renewal failed.',
    });
    await flushQueue();

    emitLifecycleEvent(client, 2, 'snapshot_changed', null);
    await flushQueue();
    expect(get(store).lastControllerError?.detail).toBe('Renewal failed.');

    const pending = store.send({ type: 'disconnect' }).catch(() => undefined);
    await flushQueue();
    expect(get(store)).toMatchObject({ lastError: null, lastControllerError: null });
    store.dispose();
    await pending;
  });

  it('clears a terminal error on confirmed success or a non-degraded snapshot', async () => {
    const successClient = new ControlledClient('degraded');
    const successStore = createDesktopStore(successClient);
    await successStore.initialize();
    emitLifecycleEvent(successClient, 1, 'operation_failed', 'automatic-wake-1', { code: 'DEGRADED', detail: 'Wake failed.' });
    await flushQueue();
    emitLifecycleEvent(successClient, 2, 'operation_succeeded', 'automatic-wake-2');
    await flushQueue();
    expect(get(successStore)).toMatchObject({ lastError: null, lastControllerError: null });
    successStore.dispose();

    const snapshotClient = new ControlledClient('degraded');
    const snapshotStore = createDesktopStore(snapshotClient);
    await snapshotStore.initialize();
    emitLifecycleEvent(snapshotClient, 1, 'operation_failed', 'automatic-network-1', { code: 'DEGRADED', detail: 'Network changed.' });
    await flushQueue();
    await snapshotClient.delegate.setScenario('disconnected');
    emitLifecycleEvent(snapshotClient, 2, 'snapshot_changed', null);
    await flushQueue();
    expect(get(snapshotStore)).toMatchObject({ lastError: null, lastControllerError: null });
    snapshotStore.dispose();
  });

  it('retains structured detail for a locally requested failed operation', async () => {
    const client = new ControlledClient('degraded');
    const store = createDesktopStore(client);
    await store.initialize();
    const pending = store.send({ type: 'disconnect' });
    await flushQueue();

    emitLifecycleEvent(client, 1, 'operation_failed', 'operation-1', {
      code: 'CONFLICT',
      detail: 'The controller rejected stale desired state.',
    });

    await expect(pending).rejects.toMatchObject({
      failure: { code: 'CONFLICT', detail: 'The controller rejected stale desired state.' },
    });
    expect(get(store)).toMatchObject({
      lastError: 'CONFLICT',
      lastControllerError: { code: 'CONFLICT', detail: 'The controller rejected stale desired state.' },
    });
    store.dispose();
  });
});
