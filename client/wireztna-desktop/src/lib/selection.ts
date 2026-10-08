import type { ExitNode, Project } from './ipc/contract';

export function exitNodesForProject(
  projects: Project[],
  exitNodes: ExitNode[],
  groupId: string,
): ExitNode[] {
  const project = projects.find((candidate) => candidate.id === groupId);
  if (!project) return [];
  const publisherIds = new Set(
    project.resources
      .filter((resource) => resource.kind === 'publisher')
      .map((resource) => resource.id),
  );
  return exitNodes.filter((exitNode) => exitNode.online && publisherIds.has(exitNode.id));
}

export function exitNodeForProject(
  projects: Project[],
  exitNodes: ExitNode[],
  groupId: string,
  exitNodeId: string | null,
): string | null {
  if (!exitNodeId) return null;
  return exitNodesForProject(projects, exitNodes, groupId).some((exitNode) => exitNode.id === exitNodeId)
    ? exitNodeId
    : null;
}

export function firstExitNodeForProject(
  projects: Project[],
  exitNodes: ExitNode[],
  groupId: string,
): string | null {
  return exitNodesForProject(projects, exitNodes, groupId)[0]?.id ?? null;
}
