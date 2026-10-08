<script lang="ts">
  import { createEventDispatcher, onMount, onDestroy } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { X } from 'lucide-svelte';

  export let title: string = '';
  export let open: boolean = false;
  export let width: string = '480px';

  const dispatch = createEventDispatcher();

  function close() {
    dispatch('close');
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) close();
  }

  function handleBackdrop(e: MouseEvent) {
    if ((e.target as HTMLElement).classList.contains('modal-overlay')) close();
  }

  onMount(() => {
    document.addEventListener('keydown', handleKeydown);
  });

  onDestroy(() => {
    document.removeEventListener('keydown', handleKeydown);
  });
</script>

{#if open}
  <div
    class="modal-overlay"
    on:click={handleBackdrop}
    transition:fade={{ duration: 150 }}
    role="presentation"
  >
    <div
      class="modal-panel"
      style="max-width: {width};"
      transition:fly={{ y: 20, duration: 250, opacity: 0 }}
      role="dialog"
      aria-modal="true"
      aria-label={title}
    >
      <header class="modal-header">
        <h2 class="modal-title">{title}</h2>
        <button class="modal-close" on:click={close} aria-label="Close">
          <X size={18} strokeWidth={2} />
        </button>
      </header>
      <div class="modal-body">
        <slot />
      </div>
    </div>
  </div>
{/if}

<style>
  .modal-overlay {
    position: fixed;
    inset: 0;
    z-index: 1000;
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding-top: 10vh;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(4px);
    -webkit-backdrop-filter: blur(4px);
  }

  .modal-panel {
    width: 100%;
    margin: 0 1rem;
    background: var(--bg-surface, #14151a);
    border: 1px solid var(--border-subtle, #2a2b35);
    border-radius: var(--radius-lg, 12px);
    box-shadow: var(--shadow-lg);
    overflow: hidden;
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 1.25rem 1.5rem;
    border-bottom: 1px solid var(--border-subtle, #2a2b35);
  }

  .modal-title {
    margin: 0;
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary, #e2e4e9);
    letter-spacing: -0.01em;
  }

  .modal-close {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    background: var(--bg-elevated, #1c1d24);
    border: 1px solid var(--border-subtle, #2a2b35);
    border-radius: var(--radius-sm, 6px);
    color: var(--text-muted, #5c5e69);
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .modal-close:hover {
    color: var(--text-primary, #e2e4e9);
    background: var(--bg-hover, #22232b);
    border-color: var(--border-default, #33343e);
  }

  .modal-body {
    padding: 1.5rem;
  }

  .modal-body :global(form) {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .modal-body :global(label) {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    font-size: 0.8125rem;
    font-weight: 500;
    color: var(--text-secondary, #8b8d97);
  }

  .modal-body :global(.modal-actions) {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.5rem;
    padding-top: 1rem;
    border-top: 1px solid var(--border-subtle, #2a2b35);
  }
</style>
