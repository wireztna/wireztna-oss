import '$lib/styles/mesh.css';

// Disable SSR for the entire app.
// This is a client-only admin panel that depends on localStorage for auth tokens.
// Without this, SvelteKit tries to render pages on the server where localStorage
// doesn't exist, causing API calls with null tokens → 500 errors.
export const ssr = false;
