import { describe, expect, it, vi } from 'vitest';
import { WAILS_PARITY_SCAFFOLD, type ShellEvent } from './contract';
import { createOfflineMockBootstrap, MockShellClient } from './mock';

function recordedEvents(shell: MockShellClient): ShellEvent[] {
  const events: ShellEvent[] = [];
  void shell.subscribe((event) => events.push(event));
  return events;
}

describe('offline shell contract', () => {
  it('really unsubscribes listeners', async () => {
    const shell = new MockShellClient();
    const listener = vi.fn();
    const unsubscribe = await shell.subscribe(listener);

    expect(shell.listenerCount()).toBe(1);
    unsubscribe();
    unsubscribe();
    shell.closeWindow();

    expect(shell.listenerCount()).toBe(0);
    expect(listener).not.toHaveBeenCalled();
  });

  it('maps window close to hide without sending disconnect', () => {
    const clients = createOfflineMockBootstrap({ initialStatus: 'connected', transitionDelayMs: 0 });
    const shell = clients.shellClient as MockShellClient;
    const request = vi.spyOn(clients.desktopIpcClient, 'request');
    const events = recordedEvents(shell);

    shell.closeWindow();

    expect(events).toEqual([
      { type: 'visibility_changed', visible: false, source: 'window-close' },
    ]);
    expect(request).not.toHaveBeenCalled();
  });

  it('keeps quit in the shell and never sends disconnect', async () => {
    const clients = createOfflineMockBootstrap({ initialStatus: 'connected', transitionDelayMs: 0 });
    const shell = clients.shellClient as MockShellClient;
    const request = vi.spyOn(clients.desktopIpcClient, 'request');
    const events = recordedEvents(shell);

    await shell.quit();

    expect(shell.commands()).toEqual(['quit']);
    expect(events).toEqual([{ type: 'quit_requested', source: 'api' }]);
    expect(request).not.toHaveBeenCalled();
  });

  it('keeps tray show, hide and quit behavior coherent', () => {
    const shell = new MockShellClient();
    const events = recordedEvents(shell);

    shell.activateTray('show');
    shell.activateTray('hide');
    shell.activateTray('quit');

    expect(shell.commands()).toEqual(['show', 'hide', 'quit']);
    expect(events).toEqual([
      { type: 'visibility_changed', visible: true, source: 'tray' },
      { type: 'visibility_changed', visible: false, source: 'tray' },
      { type: 'quit_requested', source: 'tray' },
    ]);
  });

  it('records tray visual states without mutating connection state', async () => {
    const clients = createOfflineMockBootstrap({ initialStatus: 'connected', transitionDelayMs: 0 });
    const shell = clients.shellClient as MockShellClient;
    const request = vi.spyOn(clients.desktopIpcClient, 'request');

    await shell.setTrayVisualState('attention');
    await shell.setTrayVisualState('connected');
    await shell.setTrayVisualState('disconnected');

    expect(shell.trayStates()).toEqual(['attention', 'connected', 'disconnected']);
    expect(request).not.toHaveBeenCalled();
  });

  it('marks unsupported native features and Wails parity as unimplemented', () => {
    const shell = new MockShellClient();
    expect(shell.backend).toBe('offline-mock');
    expect(shell.capabilities.singleInstance).toBe('not_implemented');
    expect(shell.capabilities.deepLinks).toBe('not_implemented');
    expect(shell.capabilities.notifications).toBe('not_implemented');
    expect(Object.values(WAILS_PARITY_SCAFFOLD)).toEqual([
      'not_implemented',
      'not_implemented',
      'not_implemented',
      'not_implemented',
      'not_implemented',
    ]);
  });
});
