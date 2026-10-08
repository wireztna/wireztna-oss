import { describe, expect, it } from 'vitest';
import type { ControllerHealth } from './ipc/contract';
import { deriveDegradedHeadline } from './degraded-headline';

const unknownHealth: ControllerHealth = {
  healthy: false,
  wireGuard: 'unknown',
  routes: 'unknown',
  dns: 'unknown',
  endToEnd: 'unknown',
};

function withHealth(overrides: Partial<ControllerHealth>): ControllerHealth {
  return { ...unknownHealth, ...overrides };
}

describe('deriveDegradedHeadline', () => {
  it('prefers terminal detail over component health', () => {
    expect(deriveDegradedHeadline(
      'en',
      withHealth({ dns: 'unhealthy' }),
      { code: 'DEGRADED', detail: 'Publisher handshake expired.' },
    )).toBe('Publisher handshake expired.');
  });

  it('uses DNS copy only when DNS is explicitly unhealthy', () => {
    expect(deriveDegradedHeadline('en', withHealth({ dns: 'unhealthy' }), null))
      .toBe('Private DNS is not responding.');
    expect(deriveDegradedHeadline('en', withHealth({ dns: 'unknown', routes: 'unhealthy' }), null))
      .toBe('Private route configuration needs attention.');
    expect(deriveDegradedHeadline('en', withHealth({ dns: 'healthy' }), null))
      .toBe('Connection health needs attention.');
  });

  it('uses specific, evidence-based route and tunnel headlines', () => {
    expect(deriveDegradedHeadline('es', withHealth({ routes: 'unhealthy' }), null))
      .toBe('La configuración de rutas privadas requiere atención.');
    expect(deriveDegradedHeadline('es', withHealth({ wireGuard: 'unhealthy' }), null))
      .toBe('El túnel WireGuard requiere atención.');
    expect(deriveDegradedHeadline('es', withHealth({ endToEnd: 'unhealthy' }), null))
      .toBe('No se pudo verificar la conexión privada.');
  });

  it('falls back to a neutral localized headline without terminal detail or unhealthy evidence', () => {
    expect(deriveDegradedHeadline('en', unknownHealth, { code: 'DEGRADED', detail: '   ' }))
      .toBe('Connection health needs attention.');
    expect(deriveDegradedHeadline('es', unknownHealth, null))
      .toBe('La salud de la conexión requiere atención.');
  });
});
