import { describe, expect, it } from 'vitest';
import {
  IPC_PROTOCOL_VERSION,
  MockDesktopIpc,
  createEnvelope,
  type IpcEvent,
} from './contract';
import {
  IpcV2DecodeError,
  fromWireSnapshotV2,
  toWireCommandRequestV2,
} from './wire-v2';

const validWireSnapshot = {
  state: 'disconnected',
  desired: {
    configuration_id: 'configuration-1',
    generation: 1,
    group_id: 'group-1',
    connected: false,
  },
  applied: {
    configuration_id: 'configuration-1',
    generation: 1,
    group_id: 'group-1',
    connected: false,
  },
  health: {
    healthy: false,
    wireguard: 'unknown',
    routes: 'unknown',
    dns: 'unknown',
    end_to_end: 'unknown',
  },
  stream_id: 'stream-1',
  epoch: 1,
  sequence: 1,
} as const;

const catalog = {
  configuration_id: 'configuration-1',
  projects: [{
    group_id: 'group-1',
    name: 'Engineering',
    description: 'Internal engineering access',
    online_resources: 1,
    cidrs: ['10.20.0.0/16'],
    resources: [{
      publisher_id: 'publisher-1',
      name: 'Build systems',
      status: 'online',
      exposed_cidrs: ['10.20.1.0/24'],
    }],
  }],
  exit_nodes: [{ exit_node_id: 'madrid', name: 'Madrid', location: 'ES', status: 'online' }],
  has_overlap: false,
  selection_recommended: false,
  allow_all: false,
  vpn_mode: true,
} as const;

const freshCatalogSnapshot = {
  ...validWireSnapshot,
  desired: {
    configuration_id: 'configuration-1',
    generation: 1,
    group_id: 'group-1',
    connected: false,
  },
  applied: { configuration_id: '', generation: 0, group_id: '', connected: false },
  catalog,
  sequence: 0,
} as const;

describe('desktop IPC v2 contract', () => {
  it('negotiates the exact v2 capabilities', async () => {
    const client = new MockDesktopIpc();
    const hello = await client.handshake();

    expect(hello.negotiatedVersion).toBe(IPC_PROTOCOL_VERSION);
    expect(hello.compatibility).toBe('current');
    expect(hello.capabilities).toEqual([
      'strict_framing',
      'controller_state',
      'idempotency',
      'event_replay',
      'authorization_policy',
      'connection_catalog',
    ]);
  });

  it('accepts a mutation by operation ID and finishes through lifecycle plus healthy snapshot', async () => {
    const client = new MockDesktopIpc({ transitionDelayMs: 0 });
    const baseline = await client.getSnapshot();
    const events: IpcEvent[] = [];
    let terminalResolve: (() => void) | undefined;
    const terminal = new Promise<void>((resolve) => { terminalResolve = resolve; });
    const subscription = await client.subscribe({
      afterSequence: baseline.snapshot.sequence,
      streamId: baseline.streamId,
      epoch: baseline.epoch,
      listener(event) {
        events.push(event);
        if (event.kind === 'operation_succeeded') terminalResolve?.();
      },
      onError(error) {
        throw error;
      },
    });

    const accepted = await client.request(createEnvelope({
      type: 'connect',
      desired: { ...baseline.snapshot.desired, connected: true },
    }));
    await terminal;
    const final = await client.getSnapshot();

    expect(accepted.operationId).not.toBe('');
    expect(events.some((event) => event.operationId === accepted.operationId)).toBe(true);
    expect(final.streamId).toBe(baseline.streamId);
    expect(final.snapshot).toMatchObject({ state: 'connected', health: { healthy: true } });
    expect(final.snapshot.projects).toHaveLength(1);
    expect(final.snapshot.exitNodes).toHaveLength(1);
    expect(final.snapshot.resources).toHaveLength(2);
    await subscription.close();
  });

  it('encodes first connect as selection-only and omits service-owned fields', () => {
    const wire = toWireCommandRequestV2(createEnvelope({
      type: 'connect',
      desired: {
        configurationId: 'service-configuration',
        generation: 42,
        groupId: 'group-1',
        exitNodeId: 'madrid',
        connected: true,
      },
    }, 'stable-key'));

    expect(wire.payload).toEqual({ group_id: 'group-1', exit_node_id: 'madrid' });
    expect(wire.payload).not.toHaveProperty('configuration_id');
    expect(wire.payload).not.toHaveProperty('generation');
    expect(wire.payload).not.toHaveProperty('connected');
  });

  it('strictly decodes a fresh catalog and projects projects, resources, and exit nodes', () => {
    const decoded = fromWireSnapshotV2(freshCatalogSnapshot);

    expect(decoded.snapshot).toMatchObject({
      state: 'disconnected',
      desired: { configurationId: 'configuration-1', generation: 1, groupId: 'group-1' },
      applied: { configurationId: '', generation: 0, groupId: '', connected: false },
      projectId: 'group-1',
      projects: [{ id: 'group-1', name: 'Engineering', resourceCount: 1 }],
      exitNodes: [{ id: 'madrid', name: 'Madrid', location: 'ES', online: true }],
      uxCapabilities: { projectCatalog: true, exitNodeCatalog: true },
    });
    expect(decoded.snapshot.resources).toEqual([
      {
        id: 'publisher-1',
        name: 'Build systems',
        detail: '10.20.1.0/24',
        kind: 'publisher',
        online: true,
      },
      {
        id: 'group-1:cidr:10.20.0.0/16',
        name: '10.20.0.0/16',
        detail: '10.20.0.0/16',
        kind: 'cidr',
        online: true,
      },
    ]);
  });

  it('requires an empty fresh selection for an ambiguous project catalog', () => {
    const secondProject = { ...catalog.projects[0], group_id: 'group-2', name: 'Operations' };
    const ambiguous = {
      ...freshCatalogSnapshot,
      desired: { ...freshCatalogSnapshot.desired, group_id: '' },
      catalog: {
        ...catalog,
        projects: [catalog.projects[0], secondProject],
        has_overlap: true,
        selection_recommended: true,
      },
    };
    expect(fromWireSnapshotV2(ambiguous).snapshot.desired.groupId).toBe('');
    expect(() => fromWireSnapshotV2({
      ...ambiguous,
      desired: { ...ambiguous.desired, group_id: 'group-1' },
    })).toThrow(IpcV2DecodeError);
  });

  it('rejects unknown catalog fields, duplicate identifiers, and unsafe integers', () => {
    expect(() => fromWireSnapshotV2({ ...validWireSnapshot, invented: true })).toThrow(IpcV2DecodeError);
    expect(() => fromWireSnapshotV2({
      ...freshCatalogSnapshot,
      catalog: { ...catalog, invented: true },
    })).toThrow(IpcV2DecodeError);
    expect(() => fromWireSnapshotV2({
      ...freshCatalogSnapshot,
      catalog: { ...catalog, exit_nodes: [catalog.exit_nodes[0], catalog.exit_nodes[0]] },
    })).toThrow(IpcV2DecodeError);
    expect(() => fromWireSnapshotV2({
      ...validWireSnapshot,
      sequence: Number.MAX_SAFE_INTEGER + 1,
    })).toThrow(IpcV2DecodeError);
  });

  it('projects only service desired/applied group and exit-node state', () => {
    const decoded = fromWireSnapshotV2({
      ...validWireSnapshot,
      desired: { ...validWireSnapshot.desired, group_id: 'desired-group', exit_node_id: 'desired-exit' },
      applied: { ...validWireSnapshot.applied, group_id: 'applied-group' },
    });

    expect(decoded.snapshot.desired.groupId).toBe('desired-group');
    expect(decoded.snapshot.applied.groupId).toBe('applied-group');
  });
});
