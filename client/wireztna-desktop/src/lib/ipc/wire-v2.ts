import {
  DesktopIpcError,
  IPC_PROTOCOL_VERSION,
  projectControllerSnapshot,
  type ActiveOperation,
  type CommandEnvelope,
  type ConnectionCatalog,
  type ConnectionState,
  type ConnectionStep,
  type ControllerCommandKind,
  type ControllerError,
  type ControllerErrorCode,
  type ControllerHealth,
  type ControllerSnapshot,
  type DesiredState,
  type HandshakeResponse,
  type HealthStatus,
  type IpcCapability,
  type IpcChannel,
  type IpcEvent,
  type LifecycleEventKind,
  type OperationAccepted,
  type PublicCommandKind,
  type SnapshotBaseline,
  type StreamIdentity,
} from './contract';

/** Exact snake_case IPC v2 DTOs mirrored from the final Go wire contract. */
export interface WireClientHelloV2 {
  kind: 'hello';
  channel: IpcChannel;
  supported_versions: [typeof IPC_PROTOCOL_VERSION];
  capabilities: IpcCapability[];
  required_capabilities?: IpcCapability[];
}

export interface WireErrorV2 {
  code: ControllerErrorCode;
  detail?: string;
}

export interface WireServerHelloV2 {
  kind: 'hello';
  negotiated_version: 0 | typeof IPC_PROTOCOL_VERSION;
  service_version: string;
  capabilities: IpcCapability[] | null;
  compatibility?: 'current';
  stream_id?: string;
  epoch?: number;
  error?: WireErrorV2;
}

export interface WireDesiredStateV2 {
  configuration_id: string;
  generation: number;
  group_id: string;
  exit_node_id?: string;
  connected: boolean;
}

export interface WireAppliedStateV2 extends WireDesiredStateV2 {}

export interface WireHealthV2 {
  healthy: boolean;
  wireguard: HealthStatus;
  routes: HealthStatus;
  dns: HealthStatus;
  end_to_end: HealthStatus;
}

export interface WireControllerCommandV2 {
  kind: ControllerCommandKind;
  desired: WireDesiredStateV2;
}

export interface WireOperationV2 {
  id: string;
  command: WireControllerCommandV2;
  started_at: string;
}

export interface WireCatalogResourceV2 {
  publisher_id: string;
  name: string;
  status: 'online' | 'offline';
  exposed_cidrs: string[];
}

export interface WireCatalogProjectV2 {
  group_id: string;
  name: string;
  description?: string;
  online_resources: number;
  cidrs: string[];
  resources: WireCatalogResourceV2[];
}

export interface WireCatalogExitNodeV2 {
  exit_node_id: string;
  name: string;
  location?: string;
  status: 'online' | 'offline';
}

export interface WireConnectionCatalogV2 {
  configuration_id: string;
  projects: WireCatalogProjectV2[];
  exit_nodes: WireCatalogExitNodeV2[];
  has_overlap: boolean;
  selection_recommended: boolean;
  allow_all: boolean;
  vpn_mode: boolean;
}

export interface WireStateSnapshotV2 {
  state: ConnectionState;
  desired: WireDesiredStateV2;
  applied: WireAppliedStateV2;
  health: WireHealthV2;
  active_operation?: WireOperationV2;
  catalog?: WireConnectionCatalogV2;
  stream_id: string;
  epoch: number;
  sequence: number;
}

export interface WireProgressV2 {
  stage: ConnectionStep;
}

export interface WireControllerEventV2 {
  sequence: number;
  kind: LifecycleEventKind;
  operation_id: string;
  state?: ConnectionState;
  occurred_at: string;
  progress?: WireProgressV2;
  error?: WireErrorV2;
}

export interface WireEventEnvelopeV2 {
  protocol_version: typeof IPC_PROTOCOL_VERSION;
  request_id: string;
  stream_id: string;
  epoch: number;
  event?: WireControllerEventV2;
  error?: WireErrorV2;
}

export interface WireDesiredPayloadV2 {
  group_id: string;
  exit_node_id?: string;
}

export type WireCommandPayloadV2 =
  | WireDesiredPayloadV2
  | Record<string, never>
  | { stream_id: string; epoch: number; after_sequence: number };

export interface WireRequestV2 {
  protocol_version: typeof IPC_PROTOCOL_VERSION;
  request_id: string;
  idempotency_key: string;
  deadline: string;
  command: PublicCommandKind | 'get_snapshot' | 'subscribe';
  payload: WireCommandPayloadV2;
}

export interface WireResponseV2 {
  protocol_version: typeof IPC_PROTOCOL_VERSION;
  request_id: string;
  idempotency_key: string;
  operation_id: string;
  success: boolean;
  snapshot?: WireStateSnapshotV2;
  error?: WireErrorV2;
}

export class IpcV2DecodeError extends Error {
  readonly code = 'IPC_V2_INVALID_WIRE_PAYLOAD' as const;

  constructor(readonly field: string) {
    super('IPC_V2_INVALID_WIRE_PAYLOAD');
    this.name = 'IpcV2DecodeError';
  }
}

const CAPABILITIES: readonly IpcCapability[] = [
  'strict_framing',
  'controller_state',
  'idempotency',
  'event_replay',
  'authorization_policy',
  'connection_catalog',
];
const CONNECTION_STATES: readonly ConnectionState[] = [
  'disconnected',
  'connecting',
  'connected',
  'reconnecting',
  'degraded',
  'auth_required',
  'update_required',
];
const HEALTH_STATUSES: readonly HealthStatus[] = ['unknown', 'healthy', 'unhealthy'];
const CONTROLLER_COMMANDS: readonly ControllerCommandKind[] = [
  'connect',
  'disconnect',
  'switch',
  'renew',
  'wake',
  'network_change',
  'reconcile',
];
const PUBLIC_COMMANDS: readonly PublicCommandKind[] = ['connect', 'disconnect', 'switch'];
const CONNECTION_STEPS: readonly ConnectionStep[] = [
  'authenticating',
  'requesting_access',
  'configuring_wireguard',
  'configuring_routes',
  'configuring_dns',
  'verifying_connection',
  'connected',
];
const EVENT_KINDS: readonly LifecycleEventKind[] = [
  'operation_started',
  'operation_progress',
  'operation_succeeded',
  'operation_failed',
  'snapshot_changed',
];
const ERROR_CODES: readonly ControllerErrorCode[] = [
  'UNAUTHORIZED',
  'UNSUPPORTED_VERSION',
  'INVALID_ARGUMENT',
  'DEADLINE_EXCEEDED',
  'CANCELED',
  'CONFLICT',
  'SERVICE_UNAVAILABLE',
  'DEGRADED',
  'REAUTH_REQUIRED',
  'RESYNC_REQUIRED',
];

export function toWireClientHelloV2(channel: IpcChannel): WireClientHelloV2 {
  return {
    kind: 'hello',
    channel,
    supported_versions: [IPC_PROTOCOL_VERSION],
    capabilities: [...CAPABILITIES],
    required_capabilities: [...CAPABILITIES],
  };
}

export function fromWireServerHelloV2(value: unknown): HandshakeResponse {
  const wire = exactObject(
    value,
    ['kind', 'negotiated_version', 'service_version', 'capabilities'],
    ['compatibility', 'stream_id', 'epoch', 'error'],
    'hello',
  );
  if (wire.kind !== 'hello') fail('hello.kind');
  const serviceVersion = nonEmptyString(wire.service_version, 'hello.service_version');
  if (wire.error !== undefined) {
    if (wire.negotiated_version !== 0 || wire.compatibility !== undefined
      || wire.stream_id !== undefined || wire.epoch !== undefined) fail('hello.failed_state');
    if (wire.capabilities !== null && (!Array.isArray(wire.capabilities) || wire.capabilities.length !== 0)) {
      fail('hello.capabilities');
    }
    throw new DesktopIpcError(decodeError(wire.error, 'hello.error'));
  }
  if (wire.negotiated_version !== IPC_PROTOCOL_VERSION) fail('hello.negotiated_version');
  if (wire.compatibility !== 'current') fail('hello.compatibility');
  const capabilities = capabilityArray(wire.capabilities, 'hello.capabilities');
  for (const required of CAPABILITIES) {
    if (!capabilities.includes(required)) fail(`hello.capabilities.${required}`);
  }
  return {
    negotiatedVersion: IPC_PROTOCOL_VERSION,
    serviceVersion,
    compatibility: 'current',
    capabilities,
    streamId: nonEmptyString(wire.stream_id, 'hello.stream_id'),
    epoch: safeInteger(wire.epoch, 'hello.epoch', 1),
  };
}

export function toWireCommandRequestV2(envelope: CommandEnvelope): WireRequestV2 {
  const command = enumString(envelope.command, PUBLIC_COMMANDS, 'request.command');
  const base = requestBase(envelope);
  if (command === 'disconnect') {
    exactObject(envelope.payload, [], [], 'request.payload');
    return { ...base, command, payload: {} };
  }
  return { ...base, command, payload: encodeDesiredPayload(envelope.payload) };
}

export function createWireGetSnapshotRequestV2(requestId: string, deadline: string): WireRequestV2 {
  return {
    protocol_version: IPC_PROTOCOL_VERSION,
    request_id: nonEmptyString(requestId, 'request.request_id'),
    idempotency_key: '',
    deadline: timestamp(deadline, 'request.deadline'),
    command: 'get_snapshot',
    payload: {},
  };
}

export function createWireSubscribeRequestV2(
  requestId: string,
  deadline: string,
  cursor: StreamIdentity & { afterSequence: number },
): WireRequestV2 {
  return {
    protocol_version: IPC_PROTOCOL_VERSION,
    request_id: nonEmptyString(requestId, 'request.request_id'),
    idempotency_key: '',
    deadline: timestamp(deadline, 'request.deadline'),
    command: 'subscribe',
    payload: {
      stream_id: nonEmptyString(cursor.streamId, 'request.payload.stream_id'),
      epoch: safeInteger(cursor.epoch, 'request.payload.epoch', 1),
      after_sequence: safeInteger(cursor.afterSequence, 'request.payload.after_sequence', 0),
    },
  };
}

export function fromWireMutationResponseV2(value: unknown, request: WireRequestV2): OperationAccepted {
  if (!PUBLIC_COMMANDS.includes(request.command as PublicCommandKind)) fail('response.command');
  const response = decodeResponse(value, request);
  if (response.error) throw new DesktopIpcError(response.error);
  if (response.snapshot !== undefined) fail('response.snapshot');
  return { operationId: nonEmptyString(response.operationId, 'response.operation_id') };
}

export function fromWireSnapshotResponseV2(
  value: unknown,
  request: WireRequestV2,
  expectedIdentity: StreamIdentity,
): SnapshotBaseline {
  if (request.command !== 'get_snapshot') fail('response.command');
  const response = decodeResponse(value, request);
  if (response.error) throw new DesktopIpcError(response.error);
  if (response.operationId !== '' || response.snapshot === undefined) fail('response');
  const baseline = decodeWireSnapshot(response.snapshot);
  if (!sameIdentity(baseline, expectedIdentity)) {
    throw new DesktopIpcError({ code: 'RESYNC_REQUIRED', detail: 'snapshot stream identity changed' });
  }
  return baseline;
}

export function fromWireSubscribeResponseV2(value: unknown, request: WireRequestV2): void {
  if (request.command !== 'subscribe') fail('response.command');
  const response = decodeResponse(value, request);
  if (response.error) throw new DesktopIpcError(response.error);
  if (response.operationId !== '' || response.snapshot !== undefined) fail('response');
}

export function fromWireEventEnvelopeV2(
  value: unknown,
  correlation: { requestId: string } & StreamIdentity,
): IpcEvent {
  const wire = exactObject(
    value,
    ['protocol_version', 'request_id', 'stream_id', 'epoch'],
    ['event', 'error'],
    'event_envelope',
  );
  if (wire.protocol_version !== IPC_PROTOCOL_VERSION) fail('event_envelope.protocol_version');
  if (nonEmptyString(wire.request_id, 'event_envelope.request_id') !== correlation.requestId) {
    fail('event_envelope.request_id');
  }
  const identity = {
    streamId: nonEmptyString(wire.stream_id, 'event_envelope.stream_id'),
    epoch: safeInteger(wire.epoch, 'event_envelope.epoch', 1),
  };
  if (!sameIdentity(identity, correlation)) {
    throw new DesktopIpcError({ code: 'RESYNC_REQUIRED', detail: 'event stream identity changed' });
  }
  if ((wire.event === undefined) === (wire.error === undefined)) fail('event_envelope.result');
  if (wire.error !== undefined) throw new DesktopIpcError(decodeError(wire.error, 'event_envelope.error'));
  return { ...decodeControllerEvent(wire.event), ...identity };
}

export function fromWireSnapshotV2(value: unknown): SnapshotBaseline {
  return decodeWireSnapshot(value);
}

function decodeWireSnapshot(value: unknown): SnapshotBaseline {
  const wire = exactObject(
    value,
    ['state', 'desired', 'applied', 'health', 'stream_id', 'epoch', 'sequence'],
    ['active_operation', 'catalog'],
    'snapshot',
  );
  const state = enumString(wire.state, CONNECTION_STATES, 'snapshot.state');
  const desired = decodeState(wire.desired, 'snapshot.desired', true);
  const applied = decodeState(wire.applied, 'snapshot.applied', false);
  const health = decodeHealth(wire.health);
  const catalog = wire.catalog === undefined ? null : decodeCatalog(wire.catalog);
  const controller: ControllerSnapshot = {
    state,
    desired,
    applied,
    health,
    activeOperation: wire.active_operation === undefined ? null : decodeOperation(wire.active_operation),
    catalog,
    sequence: safeInteger(wire.sequence, 'snapshot.sequence', 0),
  };
  const identity = {
    streamId: nonEmptyString(wire.stream_id, 'snapshot.stream_id'),
    epoch: safeInteger(wire.epoch, 'snapshot.epoch', 1),
  };

  const derivedHealthy = health.wireGuard === 'healthy'
    && health.routes === 'healthy'
    && health.dns === 'healthy'
    && health.endToEnd === 'healthy';
  if (health.healthy !== derivedHealthy) fail('snapshot.health.healthy');
  if (state === 'connected' && (!applied.connected || !health.healthy)) fail('snapshot.state');
  if (state === 'disconnected' && (applied.connected || health.healthy)) fail('snapshot.state');
  if (['connecting', 'reconnecting', 'degraded'].includes(state) && health.healthy) fail('snapshot.state');
  if (catalog) {
    // Applied/desired identifiers may legitimately outlive catalog membership
    // after authorization is revoked. They remain observable but are no longer
    // selectable; only a pristine suggestion must belong to the current catalog.
    const fresh = state === 'disconnected' && controller.sequence === 0 && isZeroState(applied);
    if (fresh) {
      const soleProjectId = catalog.projects.length === 1 ? catalog.projects[0].groupId : '';
      if (desired.configurationId !== catalog.configurationId || desired.generation < 1
        || desired.connected || desired.exitNodeId !== null
        || desired.groupId !== soleProjectId) fail('snapshot.desired');
    }
  }
  return { ...identity, snapshot: projectControllerSnapshot(controller) };
}

function decodeResponse(value: unknown, request: WireRequestV2): {
  operationId: string;
  snapshot?: unknown;
  error?: ControllerError;
} {
  const wire = exactObject(
    value,
    ['protocol_version', 'request_id', 'idempotency_key', 'operation_id', 'success'],
    ['snapshot', 'error'],
    'response',
  );
  if (wire.protocol_version !== IPC_PROTOCOL_VERSION) fail('response.protocol_version');
  if (nonEmptyString(wire.request_id, 'response.request_id') !== request.request_id) fail('response.request_id');
  if (stringValue(wire.idempotency_key, 'response.idempotency_key') !== request.idempotency_key) {
    fail('response.idempotency_key');
  }
  const success = booleanValue(wire.success, 'response.success');
  const hasError = wire.error !== undefined;
  if (success === hasError) fail('response.result');
  const operationId = stringValue(wire.operation_id, 'response.operation_id');
  const error = wire.error === undefined ? undefined : decodeError(wire.error, 'response.error');
  if (error && (wire.snapshot !== undefined || (!PUBLIC_COMMANDS.includes(request.command as PublicCommandKind) && operationId !== ''))) {
    fail('response.error_result');
  }
  return { operationId, snapshot: wire.snapshot, error };
}

function decodeControllerEvent(value: unknown): Omit<IpcEvent, keyof StreamIdentity> {
  const wire = exactObject(
    value,
    ['sequence', 'kind', 'operation_id', 'occurred_at'],
    ['state', 'progress', 'error'],
    'event',
  );
  const kind = enumString(wire.kind, EVENT_KINDS, 'event.kind');
  const operationIdValue = stringValue(wire.operation_id, 'event.operation_id');
  const common = {
    sequence: safeInteger(wire.sequence, 'event.sequence', 1),
    kind,
    operationId: operationIdValue === '' ? null : operationIdValue,
    state: wire.state === undefined ? null : enumString(wire.state, CONNECTION_STATES, 'event.state'),
    occurredAt: timestamp(wire.occurred_at, 'event.occurred_at'),
  };
  switch (kind) {
    case 'operation_started':
    case 'operation_succeeded':
      if (!common.operationId || wire.progress !== undefined || wire.error !== undefined) fail('event');
      return { ...common, progress: null, error: null };
    case 'operation_progress': {
      if (!common.operationId || wire.progress === undefined || wire.error !== undefined) fail('event');
      const progress = exactObject(wire.progress, ['stage'], [], 'event.progress');
      return {
        ...common,
        progress: { stage: enumString(progress.stage, CONNECTION_STEPS, 'event.progress.stage') },
        error: null,
      };
    }
    case 'operation_failed':
      if (!common.operationId || wire.progress !== undefined || wire.error === undefined) fail('event');
      return { ...common, progress: null, error: decodeError(wire.error, 'event.error') };
    case 'snapshot_changed':
      if (wire.progress !== undefined || wire.error !== undefined) fail('event');
      return { ...common, progress: null, error: null };
  }
}

function decodeState(value: unknown, field: string, allowSelectionPending = false): DesiredState {
  const wire = exactObject(
    value,
    ['configuration_id', 'generation', 'group_id', 'connected'],
    ['exit_node_id'],
    field,
  );
  const result: DesiredState = {
    configurationId: stringValue(wire.configuration_id, `${field}.configuration_id`),
    generation: safeInteger(wire.generation, `${field}.generation`, 0),
    groupId: stringValue(wire.group_id, `${field}.group_id`),
    exitNodeId: wire.exit_node_id === undefined ? null : nonEmptyString(wire.exit_node_id, `${field}.exit_node_id`),
    connected: booleanValue(wire.connected, `${field}.connected`),
  };
  const unconfigured = isZeroState(result);
  const configured = result.configurationId !== '' && result.generation > 0
    && (result.groupId !== '' || (allowSelectionPending && !result.connected && result.exitNodeId === null));
  if (!unconfigured && !configured) fail(field);
  if (result.connected && result.groupId === '') fail(`${field}.group_id`);
  return result;
}

function isZeroState(state: DesiredState): boolean {
  return state.configurationId === '' && state.generation === 0 && state.groupId === ''
    && state.exitNodeId === null && !state.connected;
}

function decodeCatalog(value: unknown): ConnectionCatalog {
  const wire = exactObject(
    value,
    ['configuration_id', 'projects', 'exit_nodes', 'has_overlap', 'selection_recommended', 'allow_all', 'vpn_mode'],
    [],
    'snapshot.catalog',
  );
  if (!Array.isArray(wire.projects)) fail('snapshot.catalog.projects');
  if (!Array.isArray(wire.exit_nodes)) fail('snapshot.catalog.exit_nodes');
  const projects = wire.projects.map((value, index) => {
    const field = `snapshot.catalog.projects[${index}]`;
    const project = exactObject(value, ['group_id', 'name', 'online_resources', 'cidrs', 'resources'], ['description'], field);
    const groupId = nonEmptyString(project.group_id, `${field}.group_id`);
    if (!Array.isArray(project.cidrs)) fail(`${field}.cidrs`);
    if (!Array.isArray(project.resources)) fail(`${field}.resources`);
    const resources = project.resources.map((resourceValue, resourceIndex) => {
      const resourceField = `${field}.resources[${resourceIndex}]`;
      const resource = exactObject(resourceValue, ['publisher_id', 'name', 'status', 'exposed_cidrs'], [], resourceField);
      if (!Array.isArray(resource.exposed_cidrs)) fail(`${resourceField}.exposed_cidrs`);
      return {
        publisherId: nonEmptyString(resource.publisher_id, `${resourceField}.publisher_id`),
        name: nonEmptyString(resource.name, `${resourceField}.name`),
        status: enumString(resource.status, ['online', 'offline'] as const, `${resourceField}.status`),
        exposedCidrs: uniqueStringArray(resource.exposed_cidrs, `${resourceField}.exposed_cidrs`),
      };
    });
    const onlineResources = safeInteger(project.online_resources, `${field}.online_resources`, 0);
    return {
      groupId,
      name: nonEmptyString(project.name, `${field}.name`),
      description: project.description === undefined ? null : nonEmptyString(project.description, `${field}.description`),
      onlineResources,
      cidrs: uniqueStringArray(project.cidrs, `${field}.cidrs`),
      resources,
    };
  });
  const exitNodes = wire.exit_nodes.map((value, index) => {
    const field = `snapshot.catalog.exit_nodes[${index}]`;
    const node = exactObject(value, ['exit_node_id', 'name', 'status'], ['location'], field);
    return {
      exitNodeId: nonEmptyString(node.exit_node_id, `${field}.exit_node_id`),
      name: nonEmptyString(node.name, `${field}.name`),
      location: node.location === undefined ? null : nonEmptyString(node.location, `${field}.location`),
      status: enumString(node.status, ['online', 'offline'] as const, `${field}.status`),
    };
  });
  ensureUnique(projects.map((project) => project.groupId), 'snapshot.catalog.projects');
  ensureUnique(exitNodes.map((node) => node.exitNodeId), 'snapshot.catalog.exit_nodes');
  return {
    configurationId: nonEmptyString(wire.configuration_id, 'snapshot.catalog.configuration_id'),
    projects,
    exitNodes,
    hasOverlap: booleanValue(wire.has_overlap, 'snapshot.catalog.has_overlap'),
    selectionRecommended: booleanValue(wire.selection_recommended, 'snapshot.catalog.selection_recommended'),
    allowAll: booleanValue(wire.allow_all, 'snapshot.catalog.allow_all'),
    vpnMode: booleanValue(wire.vpn_mode, 'snapshot.catalog.vpn_mode'),
  };
}

function uniqueStringArray(value: unknown[], field: string): string[] {
  const decoded = value.map((item, index) => nonEmptyString(item, `${field}[${index}]`));
  ensureUnique(decoded, field);
  return decoded;
}

function ensureUnique(value: string[], field: string): void {
  if (new Set(value).size !== value.length) fail(field);
}

function decodeHealth(value: unknown): ControllerHealth {
  const wire = exactObject(value, ['healthy', 'wireguard', 'routes', 'dns', 'end_to_end'], [], 'snapshot.health');
  return {
    healthy: booleanValue(wire.healthy, 'snapshot.health.healthy'),
    wireGuard: enumString(wire.wireguard, HEALTH_STATUSES, 'snapshot.health.wireguard'),
    routes: enumString(wire.routes, HEALTH_STATUSES, 'snapshot.health.routes'),
    dns: enumString(wire.dns, HEALTH_STATUSES, 'snapshot.health.dns'),
    endToEnd: enumString(wire.end_to_end, HEALTH_STATUSES, 'snapshot.health.end_to_end'),
  };
}

function decodeOperation(value: unknown): ActiveOperation {
  const wire = exactObject(value, ['id', 'command', 'started_at'], [], 'snapshot.active_operation');
  const command = exactObject(wire.command, ['kind', 'desired'], [], 'snapshot.active_operation.command');
  return {
    id: nonEmptyString(wire.id, 'snapshot.active_operation.id'),
    command: {
      kind: enumString(command.kind, CONTROLLER_COMMANDS, 'snapshot.active_operation.command.kind'),
      desired: decodeState(command.desired, 'snapshot.active_operation.command.desired'),
    },
    startedAt: timestamp(wire.started_at, 'snapshot.active_operation.started_at'),
  };
}

function decodeError(value: unknown, field: string): ControllerError {
  const wire = exactObject(value, ['code'], ['detail'], field);
  return {
    code: enumString(wire.code, ERROR_CODES, `${field}.code`),
    detail: wire.detail === undefined ? undefined : stringValue(wire.detail, `${field}.detail`),
  };
}

function encodeDesiredPayload(value: unknown): WireDesiredPayloadV2 {
  const desired = exactObject(
    value,
    ['configurationId', 'generation', 'groupId', 'exitNodeId', 'connected'],
    [],
    'request.payload',
  );
  if (!booleanValue(desired.connected, 'request.payload.connected')) fail('request.payload.connected');
  // configurationId and generation remain useful local convergence hints, but the
  // service owns both and the wire contract accepts selection only.
  nonEmptyString(desired.configurationId, 'request.payload.configurationId');
  safeInteger(desired.generation, 'request.payload.generation', 1);
  const exitNodeId = desired.exitNodeId === null ? undefined : nonEmptyString(desired.exitNodeId, 'request.payload.exitNodeId');
  return {
    group_id: nonEmptyString(desired.groupId, 'request.payload.groupId'),
    ...(exitNodeId === undefined ? {} : { exit_node_id: exitNodeId }),
  };
}

function requestBase(envelope: CommandEnvelope) {
  if (envelope.protocolVersion !== IPC_PROTOCOL_VERSION) fail('request.protocolVersion');
  return {
    protocol_version: IPC_PROTOCOL_VERSION,
    request_id: nonEmptyString(envelope.requestId, 'request.requestId'),
    idempotency_key: nonEmptyString(envelope.idempotencyKey, 'request.idempotencyKey'),
    deadline: timestamp(envelope.deadline, 'request.deadline'),
  };
}

function sameIdentity(left: StreamIdentity, right: StreamIdentity): boolean {
  return left.streamId === right.streamId && left.epoch === right.epoch;
}

function exactObject(
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
  field: string,
): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) fail(field);
  const object = value as Record<string, unknown>;
  const allowed = new Set([...required, ...optional]);
  for (const key of Object.keys(object)) {
    if (!allowed.has(key)) fail(`${field}.${key}`);
  }
  for (const key of required) {
    if (!Object.prototype.hasOwnProperty.call(object, key)) fail(`${field}.${key}`);
  }
  return object;
}

function capabilityArray(value: unknown, field: string): IpcCapability[] {
  if (!Array.isArray(value)) fail(field);
  const result = value.map((item, index) => enumString(item, CAPABILITIES, `${field}[${index}]`));
  if (new Set(result).size !== result.length) fail(field);
  return result;
}

function timestamp(value: unknown, field: string): string {
  const decoded = nonEmptyString(value, field);
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(decoded);
  if (!match) fail(field);
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1] ?? 0;
  if (month < 1 || month > 12 || day < 1 || day > daysInMonth
    || hour > 23 || minute > 59 || second > 59) fail(field);
  if (match[7] !== 'Z') {
    const offsetHour = Number(match[7].slice(1, 3));
    const offsetMinute = Number(match[7].slice(4, 6));
    if (offsetHour > 23 || offsetMinute > 59) fail(field);
  }
  if (!Number.isFinite(Date.parse(decoded))) fail(field);
  return decoded;
}

function stringValue(value: unknown, field: string): string {
  if (typeof value !== 'string') fail(field);
  return value;
}

function nonEmptyString(value: unknown, field: string): string {
  const decoded = stringValue(value, field);
  if (decoded.length === 0) fail(field);
  return decoded;
}

function booleanValue(value: unknown, field: string): boolean {
  if (typeof value !== 'boolean') fail(field);
  return value;
}

function safeInteger(value: unknown, field: string, minimum: number): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < minimum) fail(field);
  return value;
}

function enumString<T extends string>(value: unknown, allowed: readonly T[], field: string): T {
  if (typeof value !== 'string' || !allowed.includes(value as T)) fail(field);
  return value as T;
}

function fail(field: string): never {
  throw new IpcV2DecodeError(field);
}
