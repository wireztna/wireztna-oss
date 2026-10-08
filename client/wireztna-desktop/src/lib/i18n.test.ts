import { describe, expect, it } from 'vitest';
import { ipcErrorPresentation } from './i18n';

describe('IPC error guidance', () => {
  it('presents localized actionable helper guidance with a diagnostic code', () => {
    const english = ipcErrorPresentation('en', 'IPC_HELPER_NOT_FOUND');
    const spanish = ipcErrorPresentation('es', 'IPC_PERMISSION_DENIED');

    expect(english.message).toContain('Reinstall');
    expect(english.message).not.toBe(english.diagnosticCode);
    expect(english.diagnosticCode).toBe('IPC_HELPER_NOT_FOUND');
    expect(spanish.message).toContain('permisos');
    expect(spanish.diagnosticCode).toBe('IPC_PERMISSION_DENIED');
  });

  it('uses safe localized fallback guidance for unknown codes', () => {
    expect(ipcErrorPresentation('en', 'IPC_UNEXPECTED')).toEqual({
      message: 'The local WireZTNA service could not complete the request. Retry or contact your administrator.',
      diagnosticCode: 'IPC_UNEXPECTED',
    });
  });
});
