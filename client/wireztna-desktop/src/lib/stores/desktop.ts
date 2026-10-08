import { get, writable, type Readable } from 'svelte/store';
import {
  createEnvelope,
  DesktopIpcError,
  type Command,
  type ConnectionStep,
  type ControllerError,
  type DesktopIpcClient,
  type DesktopScenarioClient,
  type DesktopSubscription,
  type HandshakeResponse,
  type IpcEvent,
  type Scenario,
  type StateSnapshot,
} from '../ipc/contract';

export interface ActivityEntry {
  id: string;
  at: string;
  code: string;
  detail: string;
}

export interface DesktopModel {
  ready: boolean;
  busy: boolean;
  snapshot: StateSnapshot | null;
  handshake: HandshakeResponse | null;
  activeStep: ConnectionStep | null;
  progress: number;
  activities: ActivityEntry[];
  lastError: string | null;
  lastControllerError: ControllerError | null;
}

export interface DesktopStore extends Readable<DesktopModel> {
  readonly supportsScenarios: boolean;
  initialize(force?: boolean): Promise<void>;
  dispose(): void;
  send(command: Command): Promise<void>;
  setScenario(scenario: Scenario): Promise<void>;
}

const initialModel: DesktopModel = {
  ready: false,
  busy: false,
  snapshot: null,
  handshake: null,
  activeStep: null,
  progress: 0,
  activities: [],
  lastError: null,
  lastControllerError: null,
};

export interface DesktopStoreOptions {
  /** Bounds only command acceptance; accepted operations wait for a terminal event. */
  operationTimeoutMs?: number;
}

interface RetryOperation {
  idempotencyKey: string;
  command: Command;
}

interface InFlightOperation {
  key: string;
  idempotencyKey: string;
  promise: Promise<void>;
}

interface Initialization {
  generation: number;
  promise: Promise<void>;
}

interface OperationRecord {
  command?: Command;
  terminal?: IpcEvent;
  resolve?: () => void;
  reject?: (error: Error) => void;
}

const PROGRESS_STAGES: readonly ConnectionStep[] = [
  'authenticating',
  'requesting_access',
  'configuring_wireguard',
  'configuring_routes',
  'configuring_dns',
  'verifying_connection',
  'connected',
];

export function createDesktopStore(
  client: DesktopIpcClient,
  scenarioClient: DesktopScenarioClient | null = hasScenarioControl(client) ? client : null,
  options: DesktopStoreOptions = {},
): DesktopStore {
  const operationTimeoutMs = options.operationTimeoutMs ?? 30_000;
  const state = writable<DesktopModel>(initialModel);
  let generation = 0;
  let initialization: Initialization | null = null;
  let subscription: DesktopSubscription | null = null;
  let streamId: string | null = null;
  let epoch: number | null = null;
  let streamRevision = 0;
  let lastSequence = 0;
  let eventQueue = Promise.resolve();
  let recovery: Promise<void> | null = null;
  let inFlight: InFlightOperation | null = null;
  const retryKeys = new Map<string, RetryOperation>();
  const operations = new Map<string, OperationRecord>();
  const terminalOperationIds = new Set<string>();
  const terminalOrder: string[] = [];
  const terminalHistoryLimit = 128;

  function isCurrent(candidate: number): boolean {
    return candidate === generation;
  }

  async function closeSubscription(): Promise<void> {
    const current = subscription;
    subscription = null;
    if (current) await current.close();
  }

  function applySnapshot(snapshot: StateSnapshot): void {
    state.update((model) => ({
      ...model,
      snapshot,
      lastError: snapshot.state === 'degraded' ? model.lastError : null,
      lastControllerError: snapshot.state === 'degraded' ? model.lastControllerError : null,
    }));
    evaluateSuccessfulOperations(snapshot);
  }

  function confirmRetryKeysFromBaseline(snapshot: StateSnapshot): void {
    for (const [key, retry] of retryKeys) {
      // A missing active operation is not proof that an ambiguously accepted
      // mutation did not run: it may still be queued or already terminal. Keep
      // the service-scoped key until the snapshot proves the requested state.
      if (snapshotConfirmsSuccess(retry.command, snapshot)) retryKeys.delete(key);
    }
  }

  function addActivity(event: IpcEvent): void {
    const code = event.kind;
    const detail = event.error?.detail?.trim()
      || event.error?.code
      || event.progress?.stage
      || event.operationId
      || event.state
      || 'controller state changed';
    const entry: ActivityEntry = {
      id: `${event.streamId}-${event.epoch}-${event.sequence}`,
      at: event.occurredAt,
      code,
      detail,
    };
    state.update((model) => ({
      ...model,
      activities: [entry, ...model.activities].slice(0, 24),
    }));
  }

  async function refreshSnapshot(
    expectedStreamId: string,
    eventGeneration: number,
    eventRevision: number,
  ): Promise<void> {
    const baseline = await client.getSnapshot();
    if (!isCurrent(eventGeneration) || eventRevision !== streamRevision) return;
    if (baseline.streamId !== expectedStreamId || baseline.epoch !== epoch) {
      await recoverStream(eventGeneration, new DesktopIpcError({ code: 'RESYNC_REQUIRED' }));
      return;
    }
    applySnapshot(baseline.snapshot);
  }

  async function handleEvent(event: IpcEvent, eventGeneration: number, eventRevision: number): Promise<void> {
    if (!isCurrent(eventGeneration) || eventRevision !== streamRevision || !streamId) return;
    if (event.streamId !== streamId || event.epoch !== epoch) {
      await recoverStream(eventGeneration, new DesktopIpcError({ code: 'RESYNC_REQUIRED' }));
      return;
    }
    if (event.sequence !== lastSequence + 1) {
      await recoverStream(eventGeneration, new DesktopIpcError({ code: 'RESYNC_REQUIRED' }));
      return;
    }
    lastSequence = event.sequence;
    addActivity(event);

    if (event.kind === 'operation_failed') {
      const failure = event.error ?? { code: 'SERVICE_UNAVAILABLE' };
      state.update((model) => ({
        ...model,
        lastError: failure.code,
        lastControllerError: failure,
      }));
    } else if (event.kind === 'operation_succeeded') {
      state.update((model) => ({ ...model, lastError: null, lastControllerError: null }));
    }

    if (event.operationId) {
      const record = operations.get(event.operationId);
      if (event.kind === 'operation_progress' && event.progress) {
        const index = PROGRESS_STAGES.indexOf(event.progress.stage);
        state.update((model) => ({
          ...model,
          activeStep: event.progress?.stage ?? null,
          progress: index < 0 ? model.progress : Math.round(((index + 1) / PROGRESS_STAGES.length) * 100),
        }));
      }
      if (event.kind === 'operation_succeeded' || event.kind === 'operation_failed') {
        if (terminalOperationIds.has(event.operationId)) {
          failStream(new Error('IPC_DUPLICATE_OPERATION_TERMINAL'), eventGeneration);
          return;
        }
        rememberTerminal(event.operationId);
        const terminalRecord = record ?? {};
        terminalRecord.terminal = event;
        operations.set(event.operationId, terminalRecord);
        if (event.kind === 'operation_failed' && terminalRecord.reject) {
          const failure = new DesktopIpcError(event.error ?? { code: 'SERVICE_UNAVAILABLE' });
          terminalRecord.reject(failure);
          operations.delete(event.operationId);
        } else if (event.kind === 'operation_succeeded') {
          const snapshot = get(state).snapshot;
          if (snapshot) evaluateOperation(event.operationId, terminalRecord, snapshot);
        }
      }
    }

    if (event.kind === 'snapshot_changed') {
      await refreshSnapshot(event.streamId, eventGeneration, eventRevision);
    }
  }

  function rememberTerminal(operationId: string): void {
    terminalOperationIds.add(operationId);
    terminalOrder.push(operationId);
    if (terminalOrder.length <= terminalHistoryLimit) return;
    const expired = terminalOrder.shift();
    if (!expired) return;
    terminalOperationIds.delete(expired);
    const expiredRecord = operations.get(expired);
    if (expiredRecord && !expiredRecord.command) operations.delete(expired);
  }

  function queueEvent(event: IpcEvent, eventGeneration: number): void {
    const eventRevision = streamRevision;
    eventQueue = eventQueue
      .then(() => handleEvent(event, eventGeneration, eventRevision))
      .catch((error) => handleStreamError(error, eventGeneration));
  }

  function queueStreamError(error: unknown, eventGeneration: number): void {
    if (!isCurrent(eventGeneration)) return;
    invalidateStream();
    eventQueue = Promise.resolve();
    void handleStreamError(error, eventGeneration, true)
      .catch((nested) => failStream(nested, eventGeneration));
  }

  async function subscribeFromBaseline(
    baseline: { streamId: string; epoch: number; snapshot: StateSnapshot },
    eventGeneration: number,
  ): Promise<void> {
    streamId = baseline.streamId;
    epoch = baseline.epoch;
    lastSequence = baseline.snapshot.sequence;
    confirmRetryKeysFromBaseline(baseline.snapshot);
    applySnapshot(baseline.snapshot);
    const registered = await client.subscribe({
      afterSequence: baseline.snapshot.sequence,
      streamId: baseline.streamId,
      epoch: baseline.epoch,
      listener: (event) => queueEvent(event, eventGeneration),
      onError: (error) => queueStreamError(error, eventGeneration),
    });
    if (!isCurrent(eventGeneration)) {
      await registered.close();
      return;
    }
    if (registered.streamId !== baseline.streamId || registered.epoch !== baseline.epoch) {
      await registered.close();
      throw new DesktopIpcError({ code: 'RESYNC_REQUIRED' });
    }
    subscription = registered;
  }

  function invalidateStream(): void {
    streamRevision += 1;
    streamId = null;
    epoch = null;
    state.update((model) => ({
      ...model,
      ready: false,
      snapshot: null,
      handshake: null,
    }));
  }

  async function handleStreamError(
    error: unknown,
    eventGeneration: number,
    alreadyInvalidated = false,
  ): Promise<void> {
    if (!isCurrent(eventGeneration)) return;
    if (!alreadyInvalidated) invalidateStream();
    if (error instanceof DesktopIpcError && error.failure.code === 'RESYNC_REQUIRED') {
      await recoverStream(eventGeneration, error);
      return;
    }
    failStream(error, eventGeneration);
  }

  async function recoverStream(eventGeneration: number, reason: Error): Promise<void> {
    if (!isCurrent(eventGeneration)) return;
    if (recovery) return recovery;
    invalidateStream();
    recovery = (async () => {
      await closeSubscription();
      rejectPendingOperations(reason);
      const handshake = await client.handshake();
      const baseline = await client.getSnapshot();
      if (!isCurrent(eventGeneration)) return;
      await subscribeFromBaseline(baseline, eventGeneration);
      if (!isCurrent(eventGeneration)) return;
      state.update((model) => ({ ...model, ready: true, busy: false, handshake, lastError: null }));
    })();
    try {
      await recovery;
    } catch (error) {
      failStream(error, eventGeneration);
    } finally {
      recovery = null;
    }
  }

  function failStream(error: unknown, eventGeneration: number): void {
    if (!isCurrent(eventGeneration)) return;
    void closeSubscription().catch(() => undefined);
    invalidateStream();
    rejectPendingOperations(error instanceof Error ? error : new Error('IPC_UNKNOWN_ERROR'));
    state.update((model) => ({
      ...model,
      ready: true,
      busy: false,
      snapshot: null,
      handshake: null,
      lastError: errorMessage(error),
    }));
  }

  function rejectPendingOperations(error: Error): void {
    for (const record of operations.values()) {
      record.reject?.(error);
    }
    operations.clear();
    terminalOperationIds.clear();
    terminalOrder.length = 0;
    inFlight = null;
  }

  function initialize(force = false): Promise<void> {
    if (initialization) return initialization.promise;
    if (!force && subscription && get(state).ready && get(state).snapshot) return Promise.resolve();

    generation += 1;
    const initializeGeneration = generation;
    const priorSubscriptionClose = closeSubscription();
    streamId = null;
    epoch = null;
    lastSequence = 0;
    state.update((model) => ({
      ...model,
      ready: false,
      busy: true,
      snapshot: null,
      handshake: null,
      lastError: null,
    }));

    const operation: Initialization = { generation: initializeGeneration, promise: Promise.resolve() };
    initialization = operation;
    operation.promise = (async () => {
      try {
        await priorSubscriptionClose;
        let handshake = await client.handshake();
        if (!isCurrent(initializeGeneration)) return;
        const baseline = await client.getSnapshot();
        if (!isCurrent(initializeGeneration)) return;
        try {
          await subscribeFromBaseline(baseline, initializeGeneration);
        } catch (error) {
          if (!(error instanceof DesktopIpcError) || error.failure.code !== 'RESYNC_REQUIRED') throw error;
          invalidateStream();
          handshake = await client.handshake();
          const freshBaseline = await client.getSnapshot();
          if (!isCurrent(initializeGeneration)) return;
          await subscribeFromBaseline(freshBaseline, initializeGeneration);
        }
        if (!isCurrent(initializeGeneration)) return;
        state.update((model) => ({ ...model, ready: true, busy: false, handshake, lastError: null }));
      } catch (error) {
        failStream(error, initializeGeneration);
        state.update((model) => ({ ...model, snapshot: null, handshake: null }));
      } finally {
        if (initialization === operation) initialization = null;
      }
    })();
    return operation.promise;
  }

  function runMutation(command: Command): Promise<void> {
    const key = commandRetryKey(command);
    if (inFlight) {
      if (inFlight.key === key) return inFlight.promise;
      return Promise.reject(new Error('IPC_OPERATION_IN_PROGRESS'));
    }

    const operationGeneration = generation;
    const retained = retryKeys.get(key);
    const idempotencyKey = retained?.idempotencyKey ?? crypto.randomUUID();
    retryKeys.set(key, { idempotencyKey, command });
    const active: InFlightOperation = { key, idempotencyKey, promise: Promise.resolve() };
    inFlight = active;
    state.update((model) => ({
      ...model,
      busy: true,
      activeStep: null,
      progress: 0,
      lastError: null,
      lastControllerError: null,
    }));
    active.promise = (async () => {
      try {
        const envelope = createEnvelope(command, active.idempotencyKey);
        const accepted = await bounded(client.request(envelope), operationTimeoutMs);
        if (!isCurrent(operationGeneration)) throw new Error('IPC_CANCELED');
        await new Promise<void>((resolve, reject) => {
          const record = operations.get(accepted.operationId) ?? {};
          record.command = command;
          record.resolve = resolve;
          record.reject = reject;
          operations.set(accepted.operationId, record);
          if (record.terminal?.kind === 'operation_failed') {
            const failure = new DesktopIpcError(record.terminal.error ?? { code: 'SERVICE_UNAVAILABLE' });
            operations.delete(accepted.operationId);
            reject(failure);
            return;
          }
          const snapshot = get(state).snapshot;
          if (snapshot) evaluateOperation(accepted.operationId, record, snapshot);
        });
        retryKeys.delete(key);
      } catch (error) {
        if (isDefinitiveFailure(error)) retryKeys.delete(key);
        let reportedError = error;
        if (isCurrent(operationGeneration)) {
          state.update((model) => {
            const localCode = errorMessage(error);
            const terminalFailure = localCode === 'IPC_TIMEOUT' ? model.lastControllerError : null;
            if (terminalFailure) {
              // The event channel can deliver the controller terminal while the
              // command channel is still waiting for its acceptance response.
              // Preserve that authoritative failure instead of masking it as a
              // local acceptance timeout.
              reportedError = new DesktopIpcError(terminalFailure);
              return { ...model, lastError: terminalFailure.code };
            }
            return { ...model, lastError: localCode };
          });
        }
        throw reportedError;
      } finally {
        if (inFlight === active) inFlight = null;
        if (isCurrent(operationGeneration)) {
          state.update((model) => ({ ...model, busy: false }));
        }
      }
    })();
    return active.promise;
  }

  function evaluateSuccessfulOperations(snapshot: StateSnapshot): void {
    for (const [operationId, record] of operations) evaluateOperation(operationId, record, snapshot);
  }

  function evaluateOperation(operationId: string, record: OperationRecord, snapshot: StateSnapshot): void {
    if (record.terminal?.kind !== 'operation_succeeded' || !record.command || !record.resolve) return;
    if (!snapshotConfirmsSuccess(record.command, snapshot)) return;
    record.resolve();
    operations.delete(operationId);
    state.update((model) => ({
      ...model,
      activeStep: record.command?.type === 'disconnect' ? null : 'connected',
      progress: 100,
      lastError: null,
      lastControllerError: null,
    }));
  }

  function setScenario(scenario: Scenario): Promise<void> {
    if (!scenarioClient) return Promise.reject(new Error('MOCK_SCENARIOS_UNAVAILABLE'));
    if (inFlight) return Promise.reject(new Error('IPC_OPERATION_IN_PROGRESS'));
    state.update((model) => ({ ...model, busy: true, lastError: null, lastControllerError: null }));
    return scenarioClient.setScenario(scenario)
      .then((snapshot) => applySnapshot(snapshot))
      .catch((error) => {
        state.update((model) => ({ ...model, lastError: errorMessage(error) }));
        throw error;
      })
      .finally(() => state.update((model) => ({ ...model, busy: false })))
      .then(() => undefined);
  }

  return {
    subscribe: state.subscribe,
    supportsScenarios: scenarioClient !== null,
    initialize,
    dispose() {
      generation += 1;
      initialization = null;
      recovery = null;
      void closeSubscription().catch(() => undefined);
      streamId = null;
      epoch = null;
      rejectPendingOperations(new Error('IPC_CANCELED'));
      retryKeys.clear();
      state.update((model) => ({ ...model, busy: false }));
    },
    send: runMutation,
    setScenario,
  };
}

function snapshotConfirmsSuccess(command: Command, snapshot: StateSnapshot): boolean {
  if (command.type === 'disconnect') {
    return snapshot.state === 'disconnected'
      && !snapshot.health.healthy
      && !snapshot.desired.connected
      && !snapshot.applied.connected;
  }
  const desired = command.desired;
  return snapshot.state === 'connected'
    && snapshot.health.healthy
    && snapshot.applied.connected
    && snapshot.applied.groupId === desired.groupId
    && snapshot.applied.exitNodeId === desired.exitNodeId;
}

function commandRetryKey(command: Command): string {
  return command.type === 'disconnect'
    ? 'disconnect'
    : JSON.stringify({
      type: command.type,
      groupId: command.desired.groupId,
      exitNodeId: command.desired.exitNodeId,
    });
}

const DEFINITIVE_LOCAL_FAILURES = new Set([
  'IPC_HELPER_NOT_FOUND',
  'IPC_HELPER_NOT_RUNNING',
  'IPC_HELPER_PERMISSION_DENIED',
  'IPC_PERMISSION_DENIED',
  'IPC_UNSUPPORTED_PLATFORM',
  'IPC_PENDING_LIMIT',
  'IPC_REQUEST_IN_PROGRESS',
  'IPC_INVALID_ARGUMENT',
  'IPC_CANCELED',
]);

function isDefinitiveFailure(error: unknown): boolean {
  if (error instanceof DesktopIpcError) return error.failure.code !== 'RESYNC_REQUIRED';
  return error instanceof Error && DEFINITIVE_LOCAL_FAILURES.has(error.message);
}

function bounded<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('IPC_TIMEOUT')), timeoutMs);
    promise.then(resolve, reject).finally(() => clearTimeout(timeout));
  });
}

function errorMessage(error: unknown): string {
  if (error instanceof DesktopIpcError) return error.failure.code;
  return error instanceof Error ? error.message : 'IPC_UNKNOWN_ERROR';
}

function hasScenarioControl(client: DesktopIpcClient): client is DesktopIpcClient & DesktopScenarioClient {
  return 'setScenario' in client && typeof client.setScenario === 'function';
}
