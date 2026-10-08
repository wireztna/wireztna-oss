/**
 * Auth store — manages JWT token, login state, and role detection.
 */
import { writable } from 'svelte/store';

interface AuthState {
  token: string | null;
  isAuthenticated: boolean;
  isAdmin: boolean;        // true for both super_admin and org_admin
  isSuperAdmin: boolean;   // true only for super_admin (sees dashboard, debug, announcements mgmt)
  isOrgAdmin: boolean;     // true only for org_admin (sees users/publishers/groups of their org)
  role: string;            // "super_admin" | "org_admin" | "user"
  orgId: string | null;
  username: string | null;
}

function decodeJwtPayload(token: string): Record<string, any> | null {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return null;
    const payload = parts[1].replace(/-/g, '+').replace(/_/g, '/');
    return JSON.parse(atob(payload));
  } catch {
    return null;
  }
}

function stateFromToken(token: string | null): AuthState {
  const empty: AuthState = {
    token: null, isAuthenticated: false, isAdmin: false,
    isSuperAdmin: false, isOrgAdmin: false, role: 'user', orgId: null, username: null
  };
  if (!token) return empty;

  const payload = decodeJwtPayload(token);
  if (!payload) return empty;

  // Check expiry
  if (payload.exp && payload.exp * 1000 < Date.now()) return empty;

  // Determine role: prefer 'role' claim, fallback to is_admin for legacy tokens
  let role = payload.role || 'user';
  if (role === 'user' && payload.is_admin) {
    role = 'super_admin'; // Legacy token without role claim
  }

  const isSuperAdmin = role === 'super_admin';
  const isOrgAdmin = role === 'org_admin';

  return {
    token,
    isAuthenticated: true,
    isAdmin: isSuperAdmin || isOrgAdmin,
    isSuperAdmin,
    isOrgAdmin,
    role,
    orgId: payload.org_id || null,
    username: payload.username || null,
  };
}

function createAuthStore() {
  const stored = typeof localStorage !== 'undefined' ? localStorage.getItem('wireztna_token') : null;
  const initial = stateFromToken(stored);

  // Clean up expired token from localStorage
  if (stored && !initial.isAuthenticated) {
    localStorage.removeItem('wireztna_token');
  }

  const { subscribe, set } = writable<AuthState>(initial);

  return {
    subscribe,
    login(token: string) {
      localStorage.setItem('wireztna_token', token);
      set(stateFromToken(token));
    },
    logout() {
      localStorage.removeItem('wireztna_token');
      set({ token: null, isAuthenticated: false, isAdmin: false, isSuperAdmin: false, isOrgAdmin: false, role: 'user', orgId: null, username: null });
    },
    getToken(): string | null {
      let token: string | null = null;
      subscribe((state) => { token = state.token; })();
      return token;
    },
  };
}

export const authStore = createAuthStore();
