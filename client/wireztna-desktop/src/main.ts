import './app.css';
import App from './App.svelte';
import { mount } from 'svelte';
import { bootstrapDesktop } from './bootstrap';
import { hasTauriRuntime } from './lib/shell/tauri';

const app = bootstrapDesktop(
  {
    development: import.meta.env.DEV,
    tauriRuntime: hasTauriRuntime(),
    desktopMock: import.meta.env.VITE_DESKTOP_MOCK === '1',
  },
  (clients) => mount(App, {
    target: document.getElementById('app')!,
    props: clients,
  }),
);

export default app;
