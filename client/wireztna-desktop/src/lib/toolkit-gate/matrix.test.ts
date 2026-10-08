import { describe, expect, it } from 'vitest';
import matrixJson from './matrix.json';

type GateStatus = 'not_run' | 'pass' | 'fail' | 'blocked';

type Threshold = {
  statistic: string;
  operator: string;
  value: number;
  unit: string;
};

type Criterion = {
  id: string;
  kind: string;
  mandatory: boolean;
  passRule?: string;
  requiredScenarios?: string[];
  capabilityAbsentOutcome?: GateStatus;
  measurement?: string;
  thresholds?: Threshold[];
  minimumSamples?: number;
};

type Result = {
  candidateId: string;
  targetId: string;
  criterionId: string;
  status: GateStatus;
  evidence: string[];
};

type ToolkitGate = {
  schemaVersion: number;
  gateId: string;
  statusValues: GateStatus[];
  candidates: Array<{ id: string; label: string }>;
  targets: Array<{
    id: string;
    platform: string;
    osRelease: string;
    architecture: string;
    desktopEnvironment: string;
    displayProtocol: string;
    required: boolean;
    signing?: { allowed: string; claimsExcluded: string[] };
  }>;
  exclusions: Array<{ id: string; platform: string; architecture: string; reason: string }>;
  criteria: Criterion[];
  measurementProtocol: {
    percentiles: {
      method: string;
      ordering: string;
      pRepresentation: string;
      rankFormula: string;
      rankIndexBase: number;
    };
    startup: {
      minimumColdStarts: number;
      startEvent: string;
      interactiveDefinition: string;
      coldStartDefinition: Record<string, string>;
      beforeEachAttempt: string[];
      samplePolicy: Record<string, string>;
    };
    idleResources: {
      stabilizationSeconds: number;
      measurementSeconds: number;
      sampleIntervalSeconds: number;
      expectedSamples: number;
      sampleCountRule: string;
      networkCondition: string;
      processScope: {
        root: string;
        include: string;
        requiredDescendants: string[];
        membershipDiscovery: string;
      };
      perTimestampAggregation: Record<string, string>;
      seriesAggregation: { rssMiB: string; cpuPercent: string[] };
      samplePolicy: Record<string, string>;
    };
    measurementTool: { selection: string; evidenceRequires: string[] };
    build: { minimumCleanBuilds: number; dependencyMode: string; digest: string };
  };
  evidencePolicy: {
    directory: string;
    pathRequirement: string;
    missingResultStatus: GateStatus;
    passRequiresEvidence: boolean;
    blockedCountsAsPass: boolean;
    absenceCountsAsPass: boolean;
    partialCollectionCountsAsPass: boolean;
    exactEnvironmentMetadataRequired: boolean;
    requiredMetadata: string[];
  };
  results: Result[];
  decision: {
    status: GateStatus;
    selectedCandidate: string | null;
    adrPath: string | null;
    reason: string;
  };
};

const matrix = matrixJson as ToolkitGate;
const allowedStatuses = new Set<GateStatus>(['not_run', 'pass', 'fail', 'blocked']);

function criterion(id: string): Criterion {
  const found = matrix.criteria.find((entry) => entry.id === id);
  expect(found, `missing criterion ${id}`).toBeDefined();
  return found!;
}

function isEvidencePath(gate: ToolkitGate, evidencePath: string): boolean {
  const directory = gate.evidencePolicy.directory.replace(/\/+$/, '');
  if (!evidencePath.startsWith(`${directory}/`) || evidencePath.endsWith('/')) return false;
  if (evidencePath.startsWith('/') || /^[A-Za-z]:[\\/]/.test(evidencePath)) return false;

  const segments = evidencePath.split('/');
  return segments.every((segment) => segment !== '' && segment !== '.' && segment !== '..');
}

function resultPasses(gate: ToolkitGate, result: Result | undefined): boolean {
  if (!result || result.status !== 'pass') return false;
  if (!gate.evidencePolicy.passRequiresEvidence) return true;
  return result.evidence.length > 0 && result.evidence.every((path) => isEvidencePath(gate, path));
}

function selectedCandidateIsValid(gate: ToolkitGate): boolean {
  return (
    gate.decision.selectedCandidate === null ||
    gate.candidates.some(({ id }) => id === gate.decision.selectedCandidate)
  );
}

function decisionIsCoherent(gate: ToolkitGate): boolean {
  if (!selectedCandidateIsValid(gate)) return false;
  if (gate.decision.status !== 'pass') return true;
  if (gate.decision.selectedCandidate === null || !gate.decision.adrPath?.trim()) return false;

  const requiredTargets = gate.targets.filter(({ required }) => required);
  const mandatoryCriteria = gate.criteria.filter(({ mandatory }) => mandatory);

  return requiredTargets.every((target) =>
    mandatoryCriteria.every((criterionEntry) => {
      const matchingResults = gate.results.filter(
        (result) =>
          result.candidateId === gate.decision.selectedCandidate &&
          result.targetId === target.id &&
          result.criterionId === criterionEntry.id,
      );
      return matchingResults.length === 1 && resultPasses(gate, matchingResults[0]);
    }),
  );
}

function completePassingResults(gate: ToolkitGate, candidateId: string): Result[] {
  return gate.targets
    .filter(({ required }) => required)
    .flatMap((target) =>
      gate.criteria
        .filter(({ mandatory }) => mandatory)
        .map((criterionEntry) => ({
          candidateId,
          targetId: target.id,
          criterionId: criterionEntry.id,
          status: 'pass' as const,
          evidence: [
            `${gate.evidencePolicy.directory}${candidateId}-${target.id}-${criterionEntry.id}.json`,
          ],
        })),
    );
}

describe('Toolkit_Gate measurement matrix', () => {
  it('declares only the required candidates and status vocabulary', () => {
    expect(matrix.schemaVersion).toBe(1);
    expect(matrix.gateId).toBe('Toolkit_Gate');
    expect(matrix.candidates).toEqual([
      { id: 'tauri-svelte', label: 'Tauri + Svelte' },
      { id: 'wails-svelte', label: 'Wails + Svelte' },
    ]);
    expect(matrix.statusValues).toEqual(['not_run', 'pass', 'fail', 'blocked']);
    expect(new Set(matrix.candidates.map(({ id }) => id)).size).toBe(matrix.candidates.length);
  });

  it('covers macOS arm64 and every Ubuntu desktop/display pair on each baseline architecture', () => {
    const macTargets = matrix.targets.filter(({ platform }) => platform === 'macos');
    expect(macTargets).toHaveLength(1);
    expect(macTargets[0]).toMatchObject({
      id: 'macos-arm64',
      architecture: 'arm64',
      desktopEnvironment: 'Aqua',
      displayProtocol: 'Quartz',
      required: true,
    });
    expect(macTargets[0].signing).toEqual({
      allowed: 'ad-hoc-local-only',
      claimsExcluded: [
        'developer-id',
        'notarized',
        'gatekeeper-clean-distribution',
        'ga-ready',
      ],
    });

    const ubuntuTargets = matrix.targets.filter(({ platform }) => platform === 'ubuntu');
    expect(ubuntuTargets).toHaveLength(8);
    const actualCombinations = ubuntuTargets.map((target) =>
      [target.architecture, target.desktopEnvironment, target.displayProtocol].join('|'),
    );
    const expectedCombinations = ['amd64', 'arm64'].flatMap((architecture) =>
      ['GNOME 46', 'KDE Plasma 5.27'].flatMap((desktop) =>
        ['Wayland', 'X11'].map((display) => [architecture, desktop, display].join('|')),
      ),
    );
    expect(new Set(actualCombinations)).toEqual(new Set(expectedCombinations));
    expect(
      ubuntuTargets.every(
        ({ osRelease, required }) => osRelease === '24.04 LTS' && required,
      ),
    ).toBe(true);
    expect(new Set(matrix.targets.map(({ id }) => id)).size).toBe(matrix.targets.length);
  });

  it('excludes macOS Intel without transferring arm64 evidence', () => {
    expect(matrix.exclusions).toContainEqual({
      id: 'macos-intel',
      platform: 'macos',
      architecture: 'x86_64',
      reason: expect.stringContaining('must not inherit arm64 evidence'),
    });
    expect(matrix.targets).not.toContainEqual(
      expect.objectContaining({ platform: 'macos', architecture: 'x86_64' }),
    );
  });

  it('defines every mandatory behavior, accessibility, build, and IPC criterion', () => {
    const requiredIds = [
      'tray-menu',
      'normal-window',
      'quit-ui-semantics',
      'single-instance',
      'enrollment-deep-link',
      'notifications',
      'ipc-event-stream',
      'ipc-coexistence',
      'connection-screen',
      'keyboard-accessibility',
      'screen-reader-accessibility',
      'scaling',
      'startup-p95',
      'idle-rss',
      'idle-cpu',
      'reproducible-build',
    ];

    expect(matrix.criteria.map(({ id }) => id)).toEqual(requiredIds);
    expect(matrix.criteria.every(({ mandatory }) => mandatory)).toBe(true);
    expect(new Set(matrix.criteria.map(({ id }) => id)).size).toBe(matrix.criteria.length);
  });

  it('requires real tray evidence and measures the tray-absent fallback separately', () => {
    expect(criterion('tray-menu')).toMatchObject({
      requiredScenarios: ['tray-present'],
      capabilityAbsentOutcome: 'fail',
      passRule: expect.stringContaining('fallback evidence cannot substitute'),
    });
    expect(criterion('normal-window')).toMatchObject({
      requiredScenarios: ['tray-present', 'tray-absent'],
      passRule: expect.stringContaining('fallback independently from tray-menu'),
    });
  });

  it('encodes the requested performance thresholds exactly', () => {
    expect(criterion('startup-p95')).toMatchObject({
      measurement: 'cold-start-to-interactive',
      minimumSamples: 10,
      thresholds: [{ statistic: 'p95', operator: 'lte', value: 2, unit: 'seconds' }],
    });
    expect(criterion('idle-rss')).toMatchObject({
      measurement: 'candidate-process-tree-resident-set-size-after-idle-stabilization',
      thresholds: [{ statistic: 'maximum', operator: 'lte', value: 150, unit: 'MiB' }],
    });
    expect(criterion('idle-cpu')).toMatchObject({
      measurement: 'candidate-process-tree-cpu-after-idle-stabilization',
      thresholds: [
        { statistic: 'median', operator: 'lte', value: 1, unit: 'percent' },
        { statistic: 'p95', operator: 'lte', value: 3, unit: 'percent' },
      ],
    });
  });

  it('defines nearest-rank cold-start preparation and fail-closed attempts', () => {
    expect(matrix.measurementProtocol.percentiles).toEqual({
      method: 'nearest-rank',
      ordering: 'ascending',
      pRepresentation: 'fraction-between-zero-and-one',
      rankFormula: 'ceil(p * n)',
      rankIndexBase: 1,
    });
    expect(matrix.measurementProtocol.startup).toMatchObject({
      minimumColdStarts: 10,
      startEvent: 'candidate-root-process-launch-request',
      coldStartDefinition: {
        candidateProcessState: 'no-candidate-process-tree-running',
        launchProcessState: 'new-root-process',
        applicationProfileState: 'restore-recorded-baseline',
        mockTransportState: 'restore-recorded-baseline',
        osPageCacheState: 'record-preparation-and-observed-state-no-universal-reset-assumed',
      },
      samplePolicy: {
        failedLaunch: 'invalidate-run-no-pass',
        timeout: 'invalidate-run-no-pass',
        missingSample: 'invalidate-run-no-pass',
        outlierRemoval: 'forbidden',
      },
    });
    expect(matrix.measurementProtocol.startup.beforeEachAttempt).toEqual([
      'terminate-previous-candidate-process-tree',
      'confirm-candidate-process-tree-absent',
      'restore-recorded-application-profile-baseline',
      'restore-recorded-mock-transport-baseline',
      'wait-for-recorded-idle-baseline',
    ]);
  });

  it('measures the complete process tree with explicit aggregation and no lost samples', () => {
    expect(matrix.measurementProtocol.idleResources).toMatchObject({
      stabilizationSeconds: 60,
      measurementSeconds: 300,
      sampleIntervalSeconds: 1,
      expectedSamples: 300,
      sampleCountRule: 'exactly',
      processScope: {
        root: 'candidate-root-process',
        include: 'complete-recursive-process-tree',
        requiredDescendants: ['webview-processes', 'child-processes', 'all-other-descendants'],
        membershipDiscovery: 'refresh-each-sample',
      },
      perTimestampAggregation: {
        rssMiB: 'sum-rss-across-complete-process-tree',
        cpuPercent: 'sum-cpu-percent-across-complete-process-tree',
      },
      seriesAggregation: {
        rssMiB: 'maximum',
        cpuPercent: ['median', 'p95-nearest-rank'],
      },
      samplePolicy: {
        missingSample: 'invalidate-run-no-pass',
        collectorError: 'invalidate-run-no-pass',
        inaccessibleProcess: 'invalidate-run-no-pass',
        incompleteProcessTree: 'invalidate-run-no-pass',
      },
    });
    expect(matrix.measurementProtocol.measurementTool).toEqual({
      selection: 'platform-specific-not-prescribed',
      evidenceRequires: ['tool-name', 'tool-version', 'collection-method-or-command'],
    });
  });

  it('accepts only allowed result states, valid matrix references, and bounded evidence paths', () => {
    const candidateIds = new Set(matrix.candidates.map(({ id }) => id));
    const targetIds = new Set(matrix.targets.map(({ id }) => id));
    const criterionIds = new Set(matrix.criteria.map(({ id }) => id));
    const resultKeys = new Set<string>();

    for (const result of matrix.results) {
      expect(allowedStatuses.has(result.status)).toBe(true);
      expect(candidateIds.has(result.candidateId)).toBe(true);
      expect(targetIds.has(result.targetId)).toBe(true);
      expect(criterionIds.has(result.criterionId)).toBe(true);
      expect(Array.isArray(result.evidence)).toBe(true);
      if (result.status === 'pass') {
        expect(result.evidence.length).toBeGreaterThan(0);
        expect(result.evidence.every((path) => isEvidencePath(matrix, path))).toBe(true);
      }

      const key = `${result.candidateId}|${result.targetId}|${result.criterionId}`;
      expect(resultKeys.has(key), `duplicate result ${key}`).toBe(false);
      resultKeys.add(key);
    }
  });

  it('rejects non-relative, escaping, directory-only, or out-of-bound pass evidence', () => {
    const base: Result = {
      candidateId: 'tauri-svelte',
      targetId: 'macos-arm64',
      criterionId: 'normal-window',
      status: 'pass',
      evidence: ['toolkit-gate/evidence/example.json'],
    };

    expect(matrix.evidencePolicy).toMatchObject({
      directory: 'toolkit-gate/evidence/',
      pathRequirement: 'relative-file-descendant-of-directory',
      missingResultStatus: 'not_run',
      passRequiresEvidence: true,
      blockedCountsAsPass: false,
      absenceCountsAsPass: false,
      partialCollectionCountsAsPass: false,
      exactEnvironmentMetadataRequired: true,
    });
    expect(resultPasses(matrix, base)).toBe(true);
    expect(resultPasses(matrix, { ...base, evidence: [] })).toBe(false);
    expect(resultPasses(matrix, { ...base, evidence: ['/tmp/example.json'] })).toBe(false);
    expect(resultPasses(matrix, { ...base, evidence: ['evidence/example.json'] })).toBe(false);
    expect(
      resultPasses(matrix, { ...base, evidence: ['toolkit-gate/evidence/../outside.json'] }),
    ).toBe(false);
    expect(resultPasses(matrix, { ...base, evidence: ['toolkit-gate/evidence/'] })).toBe(false);
    expect(matrix.evidencePolicy.requiredMetadata).toEqual(
      expect.arrayContaining([
        'measurement-tool-name',
        'measurement-tool-version',
        'measurement-method-or-command',
        'raw-samples-or-observations',
      ]),
    );
  });

  it('never treats absence, not_run, blocked, or fail as passing', () => {
    const base: Result = {
      candidateId: 'tauri-svelte',
      targetId: 'macos-arm64',
      criterionId: 'normal-window',
      status: 'not_run',
      evidence: ['toolkit-gate/evidence/example.json'],
    };

    expect(resultPasses(matrix, undefined)).toBe(false);
    expect(resultPasses(matrix, base)).toBe(false);
    expect(resultPasses(matrix, { ...base, status: 'blocked' })).toBe(false);
    expect(resultPasses(matrix, { ...base, status: 'fail' })).toBe(false);
  });

  it('requires every non-null selected candidate to belong to the catalog', () => {
    expect(selectedCandidateIsValid(matrix)).toBe(true);
    expect(
      selectedCandidateIsValid({
        ...matrix,
        decision: { ...matrix.decision, selectedCandidate: 'unknown-toolkit' },
      }),
    ).toBe(false);
  });

  it('requires a selected candidate, ADR, and complete evidenced coverage for decision pass', () => {
    const results = completePassingResults(matrix, 'tauri-svelte');
    const passingGate: ToolkitGate = {
      ...matrix,
      results,
      decision: {
        status: 'pass',
        selectedCandidate: 'tauri-svelte',
        adrPath: 'toolkit-gate/ADR-toolkit-selection.md',
        reason: 'Synthetic validation fixture only.',
      },
    };

    expect(decisionIsCoherent(passingGate)).toBe(true);
    expect(
      decisionIsCoherent({
        ...passingGate,
        decision: { ...passingGate.decision, selectedCandidate: null },
      }),
    ).toBe(false);
    expect(
      decisionIsCoherent({
        ...passingGate,
        decision: { ...passingGate.decision, adrPath: null },
      }),
    ).toBe(false);
    expect(decisionIsCoherent({ ...passingGate, results: results.slice(1) })).toBe(false);
    expect(
      decisionIsCoherent({
        ...passingGate,
        results: [{ ...results[0], status: 'blocked' }, ...results.slice(1)],
      }),
    ).toBe(false);
    expect(
      decisionIsCoherent({
        ...passingGate,
        results: [
          { ...results[0], evidence: ['outside/evidence.json'] },
          ...results.slice(1),
        ],
      }),
    ).toBe(false);
  });

  it('starts honestly with no measurements, ADR, selected candidate, or winner', () => {
    expect(matrix.results).toEqual([]);
    expect(allowedStatuses.has(matrix.decision.status)).toBe(true);
    expect(decisionIsCoherent(matrix)).toBe(true);
    expect(matrix.decision).toMatchObject({
      status: 'not_run',
      selectedCandidate: null,
      adrPath: null,
      reason: expect.stringContaining('No measurements have been recorded'),
    });
  });
});
