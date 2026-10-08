<script lang="ts">
  import { onMount } from 'svelte';
  import { authStore } from '$lib/stores/auth';
  import { orgStore } from '$lib/stores/org';
  import { organizations } from '$lib/api/client';
  import type { OrgResponse } from '$lib/api/client';
  import { toasts } from '$lib/stores/toast';
  import { confirmDialog } from '$lib/stores/confirm';
  import Modal from '$lib/components/Modal.svelte';
  import { Building2, Plus, Pencil, Trash2, Users, FolderOpen, Plug } from 'lucide-svelte';

  let list: OrgResponse[] = [];
  let loading = true;
  let showForm = false;
  let editingOrg: OrgResponse | null = null;
  let form = { name: '', slug: '', description: '' };
  let saving = false;

  onMount(async () => { await loadAll(); });

  async function loadAll() {
    loading = true;
    try {
      list = await organizations.list($authStore.token!);
      orgStore.setOrganizations(list);
    } catch { list = []; }
    loading = false;
  }

  function openCreate() {
    editingOrg = null;
    form = { name: '', slug: '', description: '' };
    showForm = true;
  }

  function openEdit(org: OrgResponse) {
    editingOrg = org;
    form = { name: org.name, slug: org.slug, description: org.description || '' };
    showForm = true;
  }

  async function save() {
    saving = true;
    try {
      if (editingOrg) {
        await organizations.update(editingOrg.id, { name: form.name, description: form.description || null }, $authStore.token!);
        toasts.success('Organization updated');
      } else {
        await organizations.create({ name: form.name, slug: form.slug || undefined, description: form.description || undefined }, $authStore.token!);
        toasts.success('Organization created');
      }
      showForm = false;
      await loadAll();
    } catch (e: any) { toasts.error(e.message || 'Failed to save'); }
    saving = false;
  }

  function remove(org: OrgResponse) {
    confirmDialog.show({
      title: 'Delete Organization',
      message: `Delete "${org.name}"? All users, groups, and publishers must be moved to another organization first. This action cannot be undone.`,
      confirmLabel: 'Delete',
      variant: 'danger',
      requireInput: 'eliminar',
      onConfirm: async () => {
        try {
          await organizations.delete(org.id, $authStore.token!);
          toasts.success(`Organization "${org.name}" deleted`);
          await loadAll();
        } catch (e: any) { toasts.error(e.message || 'Cannot delete — org still has resources assigned'); }
      }
    });
  }

  function autoSlug(name: string): string {
    return name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  }

  $: if (!editingOrg && form.name && !form.slug) {
    form.slug = autoSlug(form.name);
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>Organizations</h1>
    <p class="page-subtitle">Logical client isolation. Each org has its own users, groups, and publishers.</p>
  </div>
  <button class="btn-primary" on:click={openCreate}>
    <Plus size={14} /> New Organization
  </button>
</div>

{#if showForm}
  <Modal title={editingOrg ? 'Edit Organization' : 'New Organization'} open={true} on:close={() => showForm = false}>
    <form on:submit|preventDefault={save}>
      <label>
        Name
        <input type="text" bind:value={form.name} required placeholder="e.g., Acme Corp" />
      </label>
      {#if !editingOrg}
        <label>
          Slug <span class="label-hint">(URL-friendly, auto-generated)</span>
          <input type="text" bind:value={form.slug} placeholder="e.g., acme-corp" pattern="[a-z0-9\-]+" />
        </label>
      {/if}
      <label>
        Description <span class="label-hint">(optional)</span>
        <input type="text" bind:value={form.description} placeholder="Brief description of this client/tenant" />
      </label>
      <div class="modal-actions">
        <button type="button" class="btn-cancel" on:click={() => showForm = false}>Cancel</button>
        <button type="submit" class="btn-primary" disabled={saving || !form.name}>
          {saving ? 'Saving...' : (editingOrg ? 'Save' : 'Create')}
        </button>
      </div>
    </form>
  </Modal>
{/if}

{#if loading}
  <p class="loading">Loading organizations...</p>
{:else if list.length === 0}
  <div class="empty-state">
    <Building2 size={40} strokeWidth={1.5} />
    <p>No organizations yet</p>
    <p class="empty-sub">Create organizations to logically separate clients within this broker.</p>
    <button class="btn-primary" on:click={openCreate}>Create first organization</button>
  </div>
{:else}
  <div class="org-grid">
    {#each list as org}
      <div class="org-card">
        <div class="org-card-header">
          <div class="org-icon"><Building2 size={18} strokeWidth={1.5} /></div>
          <div class="org-info">
            <h3>{org.name}</h3>
            <span class="org-slug">{org.slug}</span>
          </div>
          <div class="org-card-actions">
            <button class="btn-icon" title="Edit" on:click={() => openEdit(org)}>
              <Pencil size={13} />
            </button>
            <button class="btn-icon danger" title="Delete" on:click={() => remove(org)}>
              <Trash2 size={13} />
            </button>
          </div>
        </div>
        {#if org.description}
          <p class="org-desc">{org.description}</p>
        {/if}
        <div class="org-stats">
          <div class="stat">
            <Users size={13} strokeWidth={1.8} />
            <span>{org.user_count} users</span>
          </div>
          <div class="stat">
            <FolderOpen size={13} strokeWidth={1.8} />
            <span>{org.group_count} groups</span>
          </div>
          <div class="stat">
            <Plug size={13} strokeWidth={1.8} />
            <span>{org.publisher_count} publishers</span>
          </div>
        </div>
        <div class="org-meta">Created {new Date(org.created_at).toLocaleDateString()}</div>
      </div>
    {/each}
  </div>
{/if}

<style>
  .btn-primary { display: inline-flex; align-items: center; gap: 0.4rem; padding: 0.5rem 1rem; background: var(--accent-primary); color: white; border: none; border-radius: var(--radius-sm); font-size: 0.82rem; font-weight: 600; font-family: inherit; cursor: pointer; }
  .btn-primary:hover:not(:disabled) { opacity: 0.9; }
  .btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

  .empty-state { display: flex; flex-direction: column; align-items: center; gap: 0.5rem; padding: 3rem 2rem; background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); text-align: center; color: var(--text-muted); }
  .empty-state p { margin: 0; font-size: 0.9rem; }
  .empty-sub { font-size: 0.8rem; color: var(--text-muted); max-width: 400px; }

  .org-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 1rem; }

  .org-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 1.25rem; transition: box-shadow 0.15s; }
  .org-card:hover { box-shadow: var(--shadow-sm); }

  .org-card-header { display: flex; align-items: flex-start; gap: 0.75rem; }
  .org-icon { width: 36px; height: 36px; border-radius: 8px; background: var(--accent-primary-dim); display: flex; align-items: center; justify-content: center; color: var(--accent-primary); flex-shrink: 0; }
  .org-info { flex: 1; min-width: 0; }
  .org-info h3 { margin: 0; font-size: 0.95rem; font-weight: 600; color: var(--text-primary); }
  .org-slug { font-size: 0.72rem; color: var(--text-muted); font-family: 'SF Mono', monospace; }

  .org-card-actions { display: flex; gap: 0.25rem; }
  .btn-icon { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; background: transparent; border: 1px solid transparent; border-radius: var(--radius-sm); color: var(--text-muted); cursor: pointer; transition: all 0.15s; }
  .btn-icon:hover { background: var(--bg-elevated); border-color: var(--border-subtle); color: var(--text-primary); }
  .btn-icon.danger:hover { border-color: rgba(248, 113, 113, 0.4); color: var(--accent-red); }

  .org-desc { margin: 0.5rem 0 0; font-size: 0.8rem; color: var(--text-secondary); line-height: 1.4; }

  .org-stats { display: flex; gap: 1rem; margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px solid var(--border-subtle); }
  .stat { display: flex; align-items: center; gap: 0.3rem; font-size: 0.75rem; color: var(--text-secondary); }

  .org-meta { margin-top: 0.5rem; font-size: 0.68rem; color: var(--text-muted); }

  /* Form inside modal */
  form { display: flex; flex-direction: column; gap: 1rem; }
  form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--text-secondary); font-weight: 500; }
  .label-hint { font-weight: 400; color: var(--text-muted); }
  form input { padding: 0.5rem 0.75rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.85rem; font-family: inherit; }
  form input:focus { outline: none; border-color: var(--accent-blue); }
  .modal-actions { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 0.5rem; }
  .btn-cancel { padding: 0.5rem 1rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); font-size: 0.82rem; color: var(--text-secondary); cursor: pointer; font-family: inherit; }
  .btn-cancel:hover { border-color: var(--border-default); }

  .loading { color: var(--text-secondary); font-size: 0.85rem; }
</style>
