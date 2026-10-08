/// <reference types="vite/client" />

declare module 'node:fs' {
  export function readFileSync(path: string | URL, encoding: 'utf8'): string;
}

declare module 'node:url' {
  export function fileURLToPath(url: string | URL): string;
}

interface ImportMetaEnv {
  readonly VITE_DESKTOP_MOCK?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

declare const process: {
  readonly env: Record<string, string | undefined>;
};

declare const __APP_VERSION__: string;
