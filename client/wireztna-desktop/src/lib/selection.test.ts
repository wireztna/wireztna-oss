import { describe, expect, it } from 'vitest';
import type { ExitNode, Project } from './ipc/contract';
import { exitNodeForProject, firstExitNodeForProject } from './selection';

const projects: Project[] = [
  {
    id: 'group-a',
    name: 'A',
    description: null,
    resourceCount: 2,
    resources: [
      { id: 'exit-shared', name: 'Shared', detail: '', kind: 'publisher', online: true },
      { id: 'exit-a', name: 'A', detail: '', kind: 'publisher', online: true },
    ],
  },
  {
    id: 'group-b',
    name: 'B',
    description: null,
    resourceCount: 1,
    resources: [{ id: 'exit-shared', name: 'Shared', detail: '', kind: 'publisher', online: true }],
  },
];
const exitNodes: ExitNode[] = [
  { id: 'exit-a', name: 'A', location: '', online: true, latencyMs: 0 },
  { id: 'exit-shared', name: 'Shared', location: '', online: true, latencyMs: 0 },
  { id: 'exit-offline', name: 'Offline', location: '', online: false, latencyMs: 0 },
];

describe('project exit-node selection', () => {
  it('preserves only online exits exposed by the selected project', () => {
    expect(exitNodeForProject(projects, exitNodes, 'group-b', 'exit-shared')).toBe('exit-shared');
    expect(exitNodeForProject(projects, exitNodes, 'group-b', 'exit-a')).toBeNull();
    expect(exitNodeForProject(projects, exitNodes, 'group-b', 'exit-offline')).toBeNull();
    expect(exitNodeForProject(projects, exitNodes, 'missing', 'exit-shared')).toBeNull();
  });

  it('chooses the first online exit accessible through the project', () => {
    expect(firstExitNodeForProject(projects, exitNodes, 'group-a')).toBe('exit-a');
    expect(firstExitNodeForProject(projects, exitNodes, 'group-b')).toBe('exit-shared');
    expect(firstExitNodeForProject(projects, exitNodes, 'missing')).toBeNull();
  });
});
