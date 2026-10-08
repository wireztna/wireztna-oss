import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

const versionFile = fileURLToPath(new URL('../wireztna-go/VERSION', import.meta.url));
const appVersion = (process.env.WIREZTNA_APP_VERSION ?? readFileSync(versionFile, 'utf8')).trim();
if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(appVersion)) {
  throw new Error('WireZTNA desktop version must be canonical major.minor.patch');
}

export default defineConfig({
  plugins: [svelte()],
  clearScreen: false,
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  server: {
    host: '127.0.0.1',
    port: 1420,
    strictPort: true,
  },
  envPrefix: ['VITE_', 'TAURI_ENV_'],
  build: {
    target: 'es2022',
    sourcemap: true,
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
