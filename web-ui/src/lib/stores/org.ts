/**
 * Organization store — manages selected org for admin filtering.
 *
 * Admin users can select an org to filter all views (users, groups, publishers).
 * Selection persists in localStorage so it survives page reloads.
 * "All" (null) shows resources across all organizations.
 */
import { writable } from 'svelte/store';
import type { OrgResponse } from '$lib/api/client';

interface OrgState {
  organizations: OrgResponse[];
  selectedOrgId: string | null; // null = show all
  loaded: boolean;
}

const STORAGE_KEY = 'wireztna_selected_org';

function loadSelectedOrg(): string | null {
  if (typeof window === 'undefined') return null;
  return localStorage.getItem(STORAGE_KEY);
}

function createOrgStore() {
  const { subscribe, set, update } = writable<OrgState>({
    organizations: [],
    selectedOrgId: loadSelectedOrg(),
    loaded: false,
  });

  return {
    subscribe,
    setOrganizations(orgs: OrgResponse[]) {
      update(state => {
        // If no org selected yet (or the saved one no longer exists), auto-select the first
        let selectedId = state.selectedOrgId;
        if (!selectedId || !orgs.find(o => o.id === selectedId)) {
          selectedId = orgs.length > 0 ? orgs[0].id : null;
          if (selectedId) {
            localStorage.setItem(STORAGE_KEY, selectedId);
          }
        }
        return { ...state, organizations: orgs, selectedOrgId: selectedId, loaded: true };
      });
    },
    select(orgId: string | null) {
      if (orgId) {
        localStorage.setItem(STORAGE_KEY, orgId);
      } else {
        localStorage.removeItem(STORAGE_KEY);
      }
      update(state => ({ ...state, selectedOrgId: orgId }));
    },
    reset() {
      localStorage.removeItem(STORAGE_KEY);
      set({ organizations: [], selectedOrgId: null, loaded: false });
    },
  };
}

export const orgStore = createOrgStore();
