/**
 * Toast notification store — manages global toast state.
 */
import { writable } from 'svelte/store';

export interface Toast {
  id: string;
  type: 'success' | 'error' | 'info' | 'warning';
  message: string;
  duration?: number;
}

function createToastStore() {
  const { subscribe, update } = writable<Toast[]>([]);

  let counter = 0;

  function add(type: Toast['type'], message: string, duration = 4000) {
    const id = `toast-${++counter}-${Date.now()}`;
    const toast: Toast = { id, type, message, duration };

    update((toasts) => [...toasts, toast]);

    if (duration > 0) {
      setTimeout(() => remove(id), duration);
    }

    return id;
  }

  function remove(id: string) {
    update((toasts) => toasts.filter((t) => t.id !== id));
  }

  return {
    subscribe,
    success: (message: string, duration?: number) => add('success', message, duration),
    error: (message: string, duration?: number) => add('error', message, duration ?? 6000),
    info: (message: string, duration?: number) => add('info', message, duration),
    warning: (message: string, duration?: number) => add('warning', message, duration ?? 5000),
    remove,
  };
}

export const toasts = createToastStore();
