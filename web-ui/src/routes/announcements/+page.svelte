<script lang="ts">
  import { onMount } from 'svelte';
  import { authStore } from '$lib/stores/auth';
  import { announcements } from '$lib/api/client';
  import { toasts } from '$lib/stores/toast';
  import Modal from '$lib/components/Modal.svelte';
  import { Bell, Plus, Trash2, Eye, EyeOff } from 'lucide-svelte';

  let list: any[] = [];
  let loading = true;
  let showForm = false;
  let form = { title: '', message: '', type: 'info' };
  let saving = false;

  $: canManage = $authStore.isSuperAdmin;

  onMount(async () => { await loadAll(); });

  async function loadAll() {
    loading = true;
    try {
      list = canManage
        ? await announcements.list($authStore.token!)
        : await announcements.active($authStore.token!);
    } catch { list = []; }
    loading = false;
  }

  async function create() {
    saving = true;
    try {
      await announcements.create(form, $authStore.token!);
      toasts.success('Announcement created');
      showForm = false;
      form = { title: '', message: '', type: 'info' };
      await loadAll();
    } catch (e: any) { toasts.error(e.message || 'Failed to create'); }
    saving = false;
  }

  async function toggleActive(ann: any) {
    try {
      await announcements.update(ann.id, { active: !ann.active }, $authStore.token!);
      toasts.success(ann.active ? 'Announcement hidden' : 'Announcement published');
      await loadAll();
    } catch (e: any) { toasts.error(e.message); }
  }

  async function remove(ann: any) {
    if (!confirm(`Delete announcement "${ann.title}"?`)) return;
    try {
      await announcements.delete(ann.id, $authStore.token!);
      toasts.success('Announcement deleted');
      await loadAll();
    } catch (e: any) { toasts.error(e.message); }
  }

  function typeLabel(type: string): string {
    return { info: 'Info', warning: 'Warning', success: 'Success', update: 'Update' }[type] || type;
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>System Announcements</h1>
    <p class="page-subtitle">Manage messages shown to all users in their portal.</p>
  </div>
  {#if canManage}
  <button class="btn-primary" on:click={() => showForm = true}>
    <Plus size={14} /> New Announcement
  </button>
  {/if}
</div>

{#if showForm}
  <Modal title="New Announcement" open={true} on:close={() => showForm = false}>
    <form on:submit|preventDefault={create}>
      <label>
        Title
        <input type="text" bind:value={form.title} required placeholder="e.g., Client v0.6.0 released" />
      </label>
      <label>
        Message
        <textarea bind:value={form.message} required placeholder="Describe the announcement..." rows="3"></textarea>
      </label>
      <label>
        Type
        <select bind:value={form.type}>
          <option value="info">Info</option>
          <option value="update">Update</option>
          <option value="success">Success</option>
          <option value="warning">Warning</option>
        </select>
      </label>
      <div class="modal-actions">
        <button type="button" class="btn-cancel" on:click={() => showForm = false}>Cancel</button>
        <button type="submit" class="btn-primary" disabled={saving || !form.title || !form.message}>
          {saving ? 'Publishing...' : 'Publish'}
        </button>
      </div>
    </form>
  </Modal>
{/if}

{#if loading}
  <p class="loading">Loading announcements...</p>
{:else if list.length === 0}
  <div class="empty-state">
    <Bell size={40} strokeWidth={1.5} />
    <p>No announcements yet</p>
    <p class="empty-sub">Create an announcement to notify users about updates, maintenance, or new features.</p>
    {#if canManage}
    <button class="btn-primary" on:click={() => showForm = true}>Create first announcement</button>
    {/if}
  </div>
{:else}
  <div class="ann-list">
    {#each list as ann}
      <div class="ann-card" class:inactive={!ann.active}>
        <div class="ann-top">
          <span class="ann-type type-{ann.type}">{typeLabel(ann.type)}</span>
          <span class="ann-status" class:active={ann.active} class:hidden={!ann.active}>
            {ann.active ? 'Visible' : 'Hidden'}
          </span>
          <span class="ann-date">{new Date(ann.created_at).toLocaleDateString()}</span>
        </div>
        <h3 class="ann-title">{ann.title}</h3>
        <p class="ann-message">{ann.message}</p>
        <div class="ann-actions">
          {#if canManage}
          <button class="btn-icon" title={ann.active ? 'Hide' : 'Show'} on:click={() => toggleActive(ann)}>
            {#if ann.active}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
            {ann.active ? 'Hide' : 'Show'}
          </button>
          <button class="btn-icon danger" title="Delete" on:click={() => remove(ann)}>
            <Trash2 size={14} /> Delete
          </button>
          {/if}
        </div>
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

  .ann-list { display: flex; flex-direction: column; gap: 0.75rem; }
  .ann-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-md); padding: 1rem 1.25rem; transition: opacity 0.2s; }
  .ann-card.inactive { opacity: 0.55; }

  .ann-top { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.4rem; }
  .ann-type { font-size: 0.68rem; font-weight: 600; padding: 0.12rem 0.4rem; border-radius: 3px; text-transform: uppercase; letter-spacing: 0.03em; }
  .ann-type.type-info { background: var(--accent-primary-dim); color: var(--accent-primary); }
  .ann-type.type-warning { background: rgba(251, 191, 36, 0.15); color: var(--accent-orange); }
  .ann-type.type-success { background: rgba(52, 211, 153, 0.12); color: var(--accent-green); }
  .ann-type.type-update { background: rgba(167, 139, 250, 0.12); color: var(--accent-purple); }

  .ann-status { font-size: 0.7rem; font-weight: 500; }
  .ann-status.active { color: var(--accent-green); }
  .ann-status.hidden { color: var(--text-muted); }
  .ann-date { margin-left: auto; font-size: 0.7rem; color: var(--text-muted); }

  .ann-title { margin: 0; font-size: 0.9rem; font-weight: 600; color: var(--text-primary); }
  .ann-message { margin: 0.3rem 0 0.6rem; font-size: 0.82rem; color: var(--text-secondary); line-height: 1.4; }

  .ann-actions { display: flex; gap: 0.5rem; }
  .btn-icon { display: inline-flex; align-items: center; gap: 0.3rem; padding: 0.3rem 0.6rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); font-size: 0.72rem; color: var(--text-secondary); cursor: pointer; font-family: inherit; transition: all 0.15s; }
  .btn-icon:hover { border-color: var(--border-default); color: var(--text-primary); }
  .btn-icon.danger:hover { border-color: rgba(248, 113, 113, 0.5); color: var(--accent-red); }

  /* Form inside modal */
  form { display: flex; flex-direction: column; gap: 1rem; }
  form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8rem; color: var(--text-secondary); font-weight: 500; }
  form input, form textarea, form select { padding: 0.5rem 0.75rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); font-size: 0.85rem; font-family: inherit; }
  form input:focus, form textarea:focus, form select:focus { outline: none; border-color: var(--accent-blue); }
  form textarea { resize: vertical; min-height: 60px; }
  .modal-actions { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 0.5rem; }
  .btn-cancel { padding: 0.5rem 1rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); font-size: 0.82rem; color: var(--text-secondary); cursor: pointer; font-family: inherit; }
  .btn-cancel:hover { border-color: var(--border-default); }

  .loading { color: var(--text-secondary); font-size: 0.85rem; }
</style>
