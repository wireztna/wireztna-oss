/**
 * Theme store — persists light/dark mode preference.
 */
import { writable } from 'svelte/store';

type Theme = 'dark' | 'light';

function createThemeStore() {
  const stored = typeof localStorage !== 'undefined' ? localStorage.getItem('wireztna_theme') as Theme : null;
  const initial: Theme = stored || 'dark';

  const { subscribe, set } = writable<Theme>(initial);

  // Apply theme class on init
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', initial);
  }

  return {
    subscribe,
    toggle() {
      let current: Theme = 'dark';
      subscribe(v => { current = v; })();
      const next: Theme = current === 'dark' ? 'light' : 'dark';
      localStorage.setItem('wireztna_theme', next);
      document.documentElement.setAttribute('data-theme', next);
      set(next);
    },
    set(theme: Theme) {
      localStorage.setItem('wireztna_theme', theme);
      document.documentElement.setAttribute('data-theme', theme);
      set(theme);
    },
  };
}

export const themeStore = createThemeStore();
