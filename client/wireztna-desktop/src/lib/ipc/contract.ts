export const IPC_PROTOCOL_VERSION = 2 as const;

export type IpcChannel = 'command' | 'event';
export type IpcCompatibility = 'current';
export type IpcCapability =
  | 'strict_framing'
  | 'controller_state'
  | 'idempotency'
  | 'event_replay'
  | 'authorization_policy'
  | 'connection_catalog';

export type ConnectionState =
  | 'disconnected'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'degraded'
  | 'auth_required'
  | 'update_required';

/** UI-only states never accepted by the canonical IPC decoder. */
export type DesktopStatus = ConnectionState | 'setup_required' | 'service_unavailable';

export type ConnectionStep =
  | 'authenticating'
  | 'requesting_access'
  | 'configuring_wireguard'
  | 'configuring_routes'
  | 'configuring_dns'
  | 'verifying_connection'
  | 'connected';

export type HealthStatus = 'unknown' | 'healthy' | 'unhealthy';
export type ConnectionMode = 'split' | 'vpn';
export type Scenario = Exclude<DesktopStatus, 'connected'> | 'connected_split' | 'connected_vpn';
export type PublicCommandKind = 'connect' | 'disconnect' | 'switch';
export type ControllerCommandKind = PublicCommandKind | 'renew' | 'wake' | 'network_change' | 'reconcile';
export type LifecycleEventKind =
  | 'operation_started'
  | 'operation_progress'
  | 'operation_succeeded'
  | 'operation_failed'
  | 'snapshot_changed';

export type ControllerErrorCode =
  | 'UNAUTHORIZED'
  | 'UNSUPPORTED_VERSION'
  | 'INVALID_ARGUMENT'
  | 'DEADLINE_EXCEEDED'
  | 'CANCELED'
  | 'CONFLICT'
  | 'SERVICE_UNAVAILABLE'
  | 'DEGRADED'
  | 'REAUTH_REQUIRED'
  | 'RESYNC_REQUIRED';

export interface ControllerError {
  code: ControllerErrorCode;
  detail?: string;
}

export class DesktopIpcError extends Error {
  constructor(readonly failure: ControllerError) {
    super(failure.code);
    this.name = 'DesktopIpcError';
  }
}

export interface DesiredState {
  configurationId: string;
  generation: number;
  groupId: string;
  exitNodeId: string | null;
  connected: boolean;
}

export interface AppliedState extends DesiredState {}

export interface ControllerHealth {
  healthy: boolean;
  wireGuard: HealthStatus;
  routes: HealthStatus;
  dns: HealthStatus;
  endToEnd: HealthStatus;
}

export interface ControllerCommand {
  kind: ControllerCommandKind;
  desired: DesiredState;
}

export interface ActiveOperation {
  id: string;
  command: ControllerCommand;
  startedAt: string;
}

export type CatalogStatus = 'online' | 'offline';

export interface CatalogResource {
  publisherId: string;
  name: string;
  status: CatalogStatus;
  exposedCidrs: string[];
}

export interface CatalogProject {
  groupId: string;
  name: string;
  description: string | null;
  onlineResources: number;
  cidrs: string[];
  resources: CatalogResource[];
}

export interface CatalogExitNode {
  exitNodeId: string;
  name: string;
  location: string | null;
  status: CatalogStatus;
}

export interface ConnectionCatalog {
  configurationId: string;
  projects: CatalogProject[];
  exitNodes: CatalogExitNode[];
  hasOverlap: boolean;
  selectionRecommended: boolean;
  allowAll: boolean;
  vpnMode: boolean;
}

export interface ControllerSnapshot {
  state: ConnectionState;
  desired: DesiredState;
  applied: AppliedState;
  health: ControllerHealth;
  activeOperation: ActiveOperation | null;
  catalog: ConnectionCatalog | null;
  sequence: number;
}

export interface Resource {
  id: string;
  name: string;
  detail: string;
  kind: 'app' | 'cidr' | 'publisher';
  online: boolean;
}

export interface Project {
  id: string;
  name: string;
  description: string | null;
  resourceCount: number;
  resources: Resource[];
}

export interface ExitNode {
  id: string;
  name: string;
  location: string;
  online: boolean;
  latencyMs: number;
}

export interface UxCapabilities {
  enrollment: boolean;
  authentication: boolean;
  projectCatalog: boolean;
  exitNodeCatalog: boolean;
  diagnostics: boolean;
}

/**
 * UI projection of controller truth. Catalogs stay empty until a real capability
 * supplies them; applied group/exit-node values are never synthesized.
 */
export interface StateSnapshot extends ControllerSnapshot {
  status: DesktopStatus;
  projectId: string;
  mode: ConnectionMode;
  exitNodeId: string | null;
  projects: Project[];
  exitNodes: ExitNode[];
  resources: Resource[];
  uxCapabilities: UxCapabilities;
}

export interface StreamIdentity {
  streamId: string;
  epoch: number;
}

export interface HandshakeResponse extends StreamIdentity {
  negotiatedVersion: typeof IPC_PROTOCOL_VERSION;
  serviceVersion: string;
  compatibility: IpcCompatibility;
  capabilities: readonly IpcCapability[];
}

export type Command =
  | { type: 'connect'; desired: DesiredState }
  | { type: 'disconnect' }
  | { type: 'switch'; desired: DesiredState };

export interface CommandEnvelope {
  protocolVersion: typeof IPC_PROTOCOL_VERSION;
  requestId: string;
  idempotencyKey: string;
  deadline: string;
  command: PublicCommandKind;
  payload: DesiredState | Record<string, never>;
}

export interface OperationAccepted {
  operationId: string;
}

export interface SnapshotBaseline extends StreamIdentity {
  snapshot: StateSnapshot;
}

export interface SubscribeOptions extends StreamIdentity {
  afterSequence: number;
  listener: (event: IpcEvent) => void;
  onError: (error: unknown) => void;
}

export interface DesktopSubscription extends StreamIdentity {
  close(): Promise<void>;
}

export interface IpcEvent extends StreamIdentity {
  sequence: number;
  kind: LifecycleEventKind;
  operationId: string | null;
  state: ConnectionState | null;
  occurredAt: string;
  progress: { stage: ConnectionStep } | null;
  error: ControllerError | null;
}

export interface DesktopIpcClient {
  handshake(): Promise<HandshakeResponse>;
  getSnapshot(): Promise<SnapshotBaseline>;
  subscribe(options: SubscribeOptions): Promise<DesktopSubscription>;
  request(envelope: CommandEnvelope): Promise<OperationAccepted>;
}

/** Development-only control surface, deliberately excluded from production IPC. */
export interface DesktopScenarioClient {
  setScenario(scenario: Scenario): Promise<StateSnapshot>;
}

const DISABLED_UX_CAPABILITIES: UxCapabilities = Object.freeze({
  enrollment: false,
  authentication: false,
  projectCatalog: false,
  exitNodeCatalog: false,
  diagnostics: false,
});

function projectResources(project: CatalogProject): Resource[] {
  return [
    ...project.resources.map((resource): Resource => ({
      id: resource.publisherId,
      name: resource.name,
      detail: resource.exposedCidrs.join(', '),
      kind: 'publisher',
      online: resource.status === 'online',
    })),
    ...project.cidrs.map((cidr): Resource => ({
      id: `${project.groupId}:cidr:${cidr}`,
      name: cidr,
      detail: cidr,
      kind: 'cidr',
      online: true,
    })),
  ];
}

/** Project the service-owned catalog and applied/desired controller truth for the UI. */
export function projectControllerSnapshot(
  snapshot: ControllerSnapshot,
  statusOverride?: DesktopStatus,
): StateSnapshot {
  const catalog = snapshot.catalog;
  const projects = catalog?.projects.map((project): Project => ({
    id: project.groupId,
    name: project.name,
    description: project.description,
    resourceCount: project.onlineResources,
    resources: projectResources(project),
  })) ?? [];
  const projectId = snapshot.applied.groupId || (!snapshot.applied.connected ? snapshot.desired.groupId : '');
  return {
    ...clone(snapshot),
    status: statusOverride ?? snapshot.state,
    projectId,
    mode: snapshot.applied.exitNodeId || snapshot.desired.exitNodeId ? 'vpn' : 'split',
    exitNodeId: snapshot.applied.exitNodeId ?? snapshot.desired.exitNodeId,
    projects,
    exitNodes: catalog?.exitNodes.map((node): ExitNode => ({
      id: node.exitNodeId,
      name: node.name,
      location: node.location ?? '',
      online: node.status === 'online',
      latencyMs: 0,
    })) ?? [],
    resources: projects.find((project) => project.id === projectId)?.resources ?? [],
    uxCapabilities: catalog
      ? { ...DISABLED_UX_CAPABILITIES, projectCatalog: true, exitNodeCatalog: true }
      : { ...DISABLED_UX_CAPABILITIES },
  };
}

export function createEnvelope(
  command: Command,
  idempotencyKey: string = crypto.randomUUID(),
): CommandEnvelope {
  return {
    protocolVersion: IPC_PROTOCOL_VERSION,
    requestId: crypto.randomUUID(),
    idempotencyKey,
    deadline: new Date(Date.now() + 30_000).toISOString(),
    command: command.type,
    payload: command.type === 'disconnect' ? {} : clone(command.desired),
  };
}

export function isValidEnrollmentUrl(value: string): boolean {
  if (value.length === 0 || value.length > 2048 || value.trim() !== value) return false;
  try {
    const url = new URL(value);
    const tokenValues = url.searchParams.getAll('token');
    return url.protocol === 'https:'
      && url.username === ''
      && url.password === ''
      && url.hostname !== ''
      && url.port !== '8443'
      && url.pathname === '/api/v1/clients/enroll'
      && url.hash === ''
      && [...url.searchParams.keys()].every((key) => key === 'token')
      && tokenValues.length === 1
      && /^[A-Za-z0-9_-]{32,256}$/.test(tokenValues[0]);
  } catch {
    return false;
  }
}

export function isValidOtp(value: string): boolean {
  return /^\d{6}$/.test(value);
}

function clone<T>(value: T): T {
  return structuredClone(value);
}

function delay(milliseconds: number): Promise<void> {
  if (milliseconds === 0) return Promise.resolve();
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function initialControllerSnapshot(status: DesktopStatus): ControllerSnapshot {
  const connected = status === 'connected' || status === 'degraded' || status === 'reconnecting';
  const state: ConnectionState = status === 'setup_required'
    ? 'auth_required'
    : status === 'service_unavailable'
      ? 'degraded'
      : status.startsWith('connected_')
        ? 'connected'
        : status as ConnectionState;
  const healthy = state === 'connected';
  const healthValue: HealthStatus = healthy ? 'healthy' : state === 'degraded' ? 'unhealthy' : 'unknown';
  const desired: DesiredState = {
    configurationId: 'mock-configuration',
    generation: 1,
    groupId: 'engineering',
    exitNodeId: null,
    connected,
  };
  const applied: AppliedState = connected
    ? { ...desired }
    : { configurationId: '', generation: 0, groupId: '', exitNodeId: null, connected: false };
  return {
    state,
    desired,
    applied,
    health: {
      healthy,
      wireGuard: healthValue,
      routes: healthValue,
      dns: healthValue,
      endToEnd: healthValue,
    },
    activeOperation: null,
    catalog: {
      configurationId: 'mock-configuration',
      projects: [{
        groupId: 'engineering',
        name: 'Engineering',
        description: 'Engineering private access',
        onlineResources: 1,
        cidrs: ['10.20.0.0/16'],
        resources: [{
          publisherId: 'publisher-engineering',
          name: 'Engineering publisher',
          status: 'online',
          exposedCidrs: ['10.20.0.0/16'],
        }],
      }],
      exitNodes: [{ exitNodeId: 'madrid', name: 'Madrid', location: 'ES', status: 'online' }],
      hasOverlap: false,
      selectionRecommended: false,
      allowAll: false,
      vpnMode: true,
    },
    sequence: 0,
  };
}

export interface MockIpcOptions {
  initialStatus?: DesktopStatus;
  transitionDelayMs?: number;
  handshake?: 'compatible' | 'version_mismatch';
}

interface MockListener {
  listener: (event: IpcEvent) => void;
  onError: (error: unknown) => void;
}

export class MockDesktopIpc implements DesktopIpcClient, DesktopScenarioClient {
  private readonly streamId = 'mock-stream-v2';
  private readonly epoch = 1;
  private snapshot: ControllerSnapshot;
  private statusOverride: DesktopStatus | undefined;
  private sequence = 0;
  private operationSequence = 0;
  private readonly journal: IpcEvent[] = [];
  private readonly listeners = new Set<MockListener>();
  private readonly responses = new Map<string, string>();
  private readonly transitionDelayMs: number;
  private handshakeScenario: NonNullable<MockIpcOptions['handshake']>;

  constructor(options: MockIpcOptions = {}) {
    const status = options.initialStatus ?? 'disconnected';
    this.snapshot = initialControllerSnapshot(status);
    this.statusOverride = status === this.snapshot.state ? undefined : status;
    this.transitionDelayMs = options.transitionDelayMs ?? 220;
    this.handshakeScenario = options.handshake ?? 'compatible';
  }

  setHandshakeScenario(scenario: NonNullable<MockIpcOptions['handshake']>): void {
    this.handshakeScenario = scenario;
  }

  async handshake(): Promise<HandshakeResponse> {
    if (this.handshakeScenario === 'version_mismatch') {
      throw new DesktopIpcError({ code: 'UNSUPPORTED_VERSION' });
    }
    return {
      negotiatedVersion: IPC_PROTOCOL_VERSION,
      serviceVersion: 'mock-service/0.2.0',
      compatibility: 'current',
      streamId: this.streamId,
      epoch: this.epoch,
      capabilities: [
        'strict_framing',
        'controller_state',
        'idempotency',
        'event_replay',
        'authorization_policy',
        'connection_catalog',
      ],
    };
  }

  async getSnapshot(): Promise<SnapshotBaseline> {
    return {
      streamId: this.streamId,
      epoch: this.epoch,
      snapshot: projectControllerSnapshot(this.snapshot, this.statusOverride),
    };
  }

  async subscribe(options: SubscribeOptions): Promise<DesktopSubscription> {
    if (!Number.isSafeInteger(options.afterSequence) || options.afterSequence < 0) {
      throw new DesktopIpcError({ code: 'INVALID_ARGUMENT' });
    }
    if (options.streamId !== this.streamId || options.epoch !== this.epoch) {
      throw new DesktopIpcError({ code: 'RESYNC_REQUIRED' });
    }
    const firstSequence = this.journal[0]?.sequence ?? this.sequence + 1;
    if (options.afterSequence + 1 < firstSequence) {
      throw new DesktopIpcError({ code: 'RESYNC_REQUIRED' });
    }
    for (const event of this.journal) {
      if (event.sequence > options.afterSequence) options.listener(clone(event));
    }
    const registered: MockListener = { listener: options.listener, onError: options.onError };
    this.listeners.add(registered);
    return {
      streamId: this.streamId,
      epoch: this.epoch,
      close: async () => {
        this.listeners.delete(registered);
      },
    };
  }

  async request(envelope: CommandEnvelope): Promise<OperationAccepted> {
    if (envelope.protocolVersion !== IPC_PROTOCOL_VERSION) {
      throw new DesktopIpcError({ code: 'UNSUPPORTED_VERSION' });
    }
    if (!envelope.requestId || !envelope.idempotencyKey || !Number.isFinite(Date.parse(envelope.deadline))) {
      throw new DesktopIpcError({ code: 'INVALID_ARGUMENT' });
    }
    if (Date.parse(envelope.deadline) <= Date.now()) {
      throw new DesktopIpcError({ code: 'DEADLINE_EXCEEDED' });
    }
    const responseKey = `${envelope.command}:${envelope.idempotencyKey}`;
    const existing = this.responses.get(responseKey);
    if (existing) return { operationId: existing };

    this.operationSequence += 1;
    const operationId = `mock-operation-${this.operationSequence}`;
    this.responses.set(responseKey, operationId);
    queueMicrotask(() => {
      void this.execute(envelope, operationId).catch((error) => {
        this.emit({
          kind: 'operation_failed',
          operationId,
          state: this.snapshot.state,
          progress: null,
          error: error instanceof DesktopIpcError ? error.failure : { code: 'SERVICE_UNAVAILABLE' },
        });
      });
    });
    return { operationId };
  }

  async setScenario(scenario: Scenario): Promise<StateSnapshot> {
    const status: DesktopStatus = scenario.startsWith('connected_') ? 'connected' : scenario as DesktopStatus;
    this.snapshot = initialControllerSnapshot(status);
    if (scenario === 'connected_vpn') {
      this.snapshot.desired.exitNodeId = 'madrid';
      this.snapshot.applied.exitNodeId = 'madrid';
    }
    this.statusOverride = status === this.snapshot.state ? undefined : status;
    this.publishSnapshotChanged(null);
    return (await this.getSnapshot()).snapshot;
  }

  private async execute(envelope: CommandEnvelope, operationId: string): Promise<void> {
    const desired = envelope.command === 'disconnect'
      ? { ...this.snapshot.desired, connected: false }
      : clone(envelope.payload as DesiredState);
    this.statusOverride = undefined;
    this.emit({
      kind: 'operation_started',
      operationId,
      state: this.snapshot.state,
      progress: null,
      error: null,
    });
    this.snapshot.desired = desired;
    this.snapshot.state = envelope.command === 'connect' ? 'connecting' : envelope.command === 'switch' ? 'reconnecting' : 'disconnected';
    this.snapshot.activeOperation = {
      id: operationId,
      command: { kind: envelope.command, desired },
      startedAt: new Date().toISOString(),
    };
    this.publishSnapshotChanged(operationId);

    if (envelope.command !== 'disconnect') {
      const stages: ConnectionStep[] = envelope.command === 'connect'
        ? ['authenticating', 'requesting_access', 'configuring_wireguard', 'configuring_routes', 'configuring_dns', 'verifying_connection', 'connected']
        : ['configuring_wireguard', 'configuring_routes', 'configuring_dns', 'verifying_connection', 'connected'];
      for (const stage of stages) {
        await delay(this.transitionDelayMs);
        this.emit({
          kind: 'operation_progress',
          operationId,
          state: this.snapshot.state,
          progress: { stage },
          error: null,
        });
      }
      this.snapshot.applied = { ...desired, connected: true };
      this.snapshot.desired = { ...desired, connected: true };
      this.snapshot.state = 'connected';
      this.snapshot.health = {
        healthy: true,
        wireGuard: 'healthy',
        routes: 'healthy',
        dns: 'healthy',
        endToEnd: 'healthy',
      };
    } else {
      this.snapshot.applied = { ...this.snapshot.applied, connected: false };
      this.snapshot.desired = { ...desired, connected: false };
      this.snapshot.state = 'disconnected';
      this.snapshot.health = {
        healthy: false,
        wireGuard: 'unknown',
        routes: 'unknown',
        dns: 'unknown',
        endToEnd: 'unknown',
      };
    }

    this.publishSnapshotChanged(operationId);
    this.emit({
      kind: 'operation_succeeded',
      operationId,
      state: this.snapshot.state,
      progress: null,
      error: null,
    });
    this.snapshot.activeOperation = null;
    this.publishSnapshotChanged(operationId);
  }

  private publishSnapshotChanged(operationId: string | null): void {
    this.emit({
      kind: 'snapshot_changed',
      operationId,
      state: this.snapshot.state,
      progress: null,
      error: null,
    });
  }

  private emit(event: Omit<IpcEvent, 'streamId' | 'epoch' | 'sequence' | 'occurredAt'>): void {
    this.sequence += 1;
    this.snapshot.sequence = this.sequence;
    const complete: IpcEvent = {
      ...event,
      streamId: this.streamId,
      epoch: this.epoch,
      sequence: this.sequence,
      occurredAt: new Date().toISOString(),
    };
    this.journal.push(complete);
    if (this.journal.length > 256) this.journal.shift();
    for (const registered of this.listeners) registered.listener(clone(complete));
  }
}

export function createMockIpc(options: MockIpcOptions = {}): MockDesktopIpc {
  return new MockDesktopIpc(options);
}
