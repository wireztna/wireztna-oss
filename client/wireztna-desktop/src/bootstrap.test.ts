import type { Event, UnlistenFn } from '@tauri-apps/api/event';
import { describe, expect, it, vi } from 'vitest';
import { MockDesktopIpc } from './lib/ipc/contract';
import {
  TAURI_DESKTOP_IPC_READINESS,
  TauriDesktopIpcClient,
  TauriShellClient,
  type TauriTransport,
} from './lib/shell/tauri';
import { bootstrapDesktop } from './bootstrap';

class RecordingTransport implements TauriTransport {
  readonly invocations: string[] = [];
  readonly listeners: string[] = [];

  async invoke<T>(command: string): Promise<T> {
    this.invocations.push(command);
    throw new Error(`UNEXPECTED_INVOKE:${command}`);
  }

  async listen<T>(event: string, _handler: (event: Event<T>) => void): Promise<UnlistenFn> {
    this.listeners.push(event);
    return () => undefined;
  }
}

describe('desktop bootstrap selection', () => {
  it('selects registered native IPC in a production Tauri runtime without invoking it before mount', () => {
    const transport = new RecordingTransport();
    const mount = vi.fn((clients) => clients);

    const clients = bootstrapDesktop(
      { development: false, tauriRuntime: true, desktopMock: false },
      mount,
      transport,
    );

    expect(TAURI_DESKTOP_IPC_READINESS).toBe('available');
    expect(clients.desktopIpcClient).toBeInstanceOf(TauriDesktopIpcClient);
    expect(clients.shellClient).toBeInstanceOf(TauriShellClient);
    expect(clients.scenarioClient).toBeUndefined();
    expect(mount).toHaveBeenCalledOnce();
    expect(transport.invocations).toEqual([]);
    expect(transport.listeners).toEqual([]);
  });

  it('uses native IPC by default in Tauri development', () => {
    const clients = bootstrapDesktop(
      { development: true, tauriRuntime: true, desktopMock: false },
      (selected) => selected,
      new RecordingTransport(),
    );

    expect(clients.desktopIpcClient).toBeInstanceOf(TauriDesktopIpcClient);
    expect(clients.scenarioClient).toBeUndefined();
  });

  it('enables mock and scenario controls only with the explicit development flag', () => {
    const clients = bootstrapDesktop(
      { development: true, tauriRuntime: true, desktopMock: true },
      (selected) => selected,
      new RecordingTransport(),
    );

    expect(clients.desktopIpcClient).toBeInstanceOf(MockDesktopIpc);
    expect(clients.scenarioClient).toBe(clients.desktopIpcClient);
    expect(clients.shellClient).toBeInstanceOf(TauriShellClient);
  });

  it('allows explicit mock browser development but never production mock fallback', () => {
    const development = bootstrapDesktop(
      { development: true, tauriRuntime: false, desktopMock: true },
      (selected) => selected,
    );
    expect(development.desktopIpcClient).toBeInstanceOf(MockDesktopIpc);

    const mount = vi.fn();
    expect(() => bootstrapDesktop(
      { development: false, tauriRuntime: false, desktopMock: true },
      mount,
    )).toThrow('DESKTOP_SHELL_UNAVAILABLE');
    expect(mount).not.toHaveBeenCalled();
  });
});
