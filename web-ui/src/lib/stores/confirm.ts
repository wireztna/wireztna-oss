import { writable } from 'svelte/store';

interface ConfirmState {
  open: boolean;
  title: string;
  message: string;
  confirmLabel: string;
  variant: 'danger' | 'warning' | 'default';
  requireInput: string | null; // If set, user must type this word to confirm
  onConfirm: () => void;
}

const defaultState: ConfirmState = {
  open: false,
  title: 'Confirm',
  message: '',
  confirmLabel: 'Confirm',
  variant: 'danger',
  requireInput: null,
  onConfirm: () => {},
};

function createConfirmStore() {
  const { subscribe, set, update } = writable<ConfirmState>(defaultState);

  return {
    subscribe,
    show(opts: {
      title?: string;
      message: string;
      confirmLabel?: string;
      variant?: 'danger' | 'warning' | 'default';
      requireInput?: string;
      onConfirm: () => void;
    }) {
      set({
        open: true,
        title: opts.title || 'Confirm',
        message: opts.message,
        confirmLabel: opts.confirmLabel || 'Confirm',
        variant: opts.variant || 'danger',
        requireInput: opts.requireInput || null,
        onConfirm: opts.onConfirm,
      });
    },
    close() {
      update(s => ({ ...s, open: false }));
    },
  };
}

export const confirmDialog = createConfirmStore();
