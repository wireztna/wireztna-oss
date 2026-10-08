import { describe, expect, it } from 'vitest';
import type { DesktopStatus } from './ipc/contract';
import { deriveTrayVisualState } from './tray-state';

describe('tray visual state', () => {
  it('uses grey only for a stable disconnected snapshot', () => {
    expect(deriveTrayVisualState('disconnected', false, false)).toBe('disconnected');
    expect(deriveTrayVisualState('disconnected', false, true)).toBe('attention');
  });

  it('uses green only for a healthy connected snapshot without errors', () => {
    expect(deriveTrayVisualState('connected', true, false)).toBe('connected');
    expect(deriveTrayVisualState('connected', false, false)).toBe('attention');
    expect(deriveTrayVisualState('connected', true, true)).toBe('attention');
  });

  it.each<DesktopStatus | null>([
    null,
    'setup_required',
    'auth_required',
    'update_required',
    'connecting',
    'reconnecting',
    'degraded',
    'service_unavailable',
  ])('uses orange attention for %s', (status) => {
    expect(deriveTrayVisualState(status, false, false)).toBe('attention');
  });
});
