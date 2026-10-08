import type { ControllerError, ControllerHealth } from './ipc/contract';
import { translate, type Locale } from './i18n';

/** Derive a degraded-state headline without claiming that unverified components are healthy. */
export function deriveDegradedHeadline(
  locale: Locale,
  health: ControllerHealth,
  lastControllerError: ControllerError | null,
): string {
  const terminalDetail = lastControllerError?.detail?.trim();
  if (terminalDetail) return terminalDetail;
  if (health.dns === 'unhealthy') return translate(locale, 'dnsIssue');
  if (health.routes === 'unhealthy') return translate(locale, 'routesIssue');
  if (health.wireGuard === 'unhealthy') return translate(locale, 'wireGuardIssue');
  if (health.endToEnd === 'unhealthy') return translate(locale, 'endToEndIssue');
  return translate(locale, 'degradedIssue');
}
