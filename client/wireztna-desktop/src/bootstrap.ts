import type { DesktopBootstrapClients } from './lib/shell/contract';
import { createOfflineMockBootstrap } from './lib/shell/mock';
import {
  createTauriBootstrapClients,
  TauriShellClient,
  type TauriTransport,
} from './lib/shell/tauri';

export interface DesktopBootstrapEnvironment {
  development: boolean;
  tauriRuntime: boolean;
  desktopMock: boolean;
}

export function selectBootstrapClients(
  environment: DesktopBootstrapEnvironment,
  transport?: TauriTransport,
): DesktopBootstrapClients {
  if (environment.development && environment.desktopMock) {
    const offline = createOfflineMockBootstrap({ initialStatus: 'disconnected', transitionDelayMs: 260 });
    return environment.tauriRuntime
      ? { ...offline, shellClient: new TauriShellClient(transport) }
      : offline;
  }

  if (!environment.tauriRuntime) throw new Error('DESKTOP_SHELL_UNAVAILABLE');
  return createTauriBootstrapClients(transport);
}

export function bootstrapDesktop<T>(
  environment: DesktopBootstrapEnvironment,
  mountDesktop: (clients: DesktopBootstrapClients) => T,
  transport?: TauriTransport,
): T {
  const clients = selectBootstrapClients(environment, transport);
  return mountDesktop(clients);
}
