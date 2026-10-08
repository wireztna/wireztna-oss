import type { DesktopStatus } from './ipc/contract';
import type { TrayVisualState } from './shell/contract';

/** Reduce controller truth to the three visual states exposed by the native tray. */
export function deriveTrayVisualState(
  status: DesktopStatus | null,
  healthHealthy: boolean,
  hasError: boolean,
): TrayVisualState {
  if (hasError) return 'attention';
  if (status === 'disconnected') return 'disconnected';
  if (status === 'connected' && healthHealthy) return 'connected';
  return 'attention';
}
