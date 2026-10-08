<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { fade, fly } from 'svelte/transition';
  import { AlertTriangle, Trash2, X } from 'lucide-svelte';

  export let open = false;
  export let title = 'Confirm action';
  export let message = 'Are you sure you want to proceed?';
  export let confirmLabel = 'Confirm';
  export let cancelLabel = 'Cancel';
  export let variant: 'danger' | 'warning' | 'default' = 'danger';
  export let requireInput: string | null = null;

  let inputValue = '';

  const dispatch = createEventDispatcher();

  $: if (open) inputValue = '';
  $: canConfirm = !requireInput || inputValue.toLowerCase() === requireInput.toLowerCase();

  function confirm() {
    if (!canConfirm) return;
    dispatch('confirm');
    open = false;
  }

  function cancel() {
    dispatch('cancel');
    open = false;
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) cancel();
    if (e.key === 'Enter' && open && canConfirm) confirm();
  }

  function handleBackdrop(e: MouseEvent) {
    if ((e.target as HTMLElement).classList.contains('confirm-overlay')) cancel();
  }
</script>

<svelte:window on:keydown={handleKeydown} />

{#if open}
  <div
    class="confirm-overlay"
    on:click={handleBackdrop}
    transition:fade={{ duration: 150 }}
    role="presentation"
  >
    <div
      class="confirm-panel"
      transition:fly={{ y: 16, duration: 200, opacity: 0 }}
      role="alertdialog"
      aria-modal="true"
      aria-label={title}
    >
      <div class="confirm-icon {variant}">
        {#if variant === 'danger'}
          <Trash2 size={22} strokeWidth={1.8} />
        {:else}
          <AlertTriangle size={22} strokeWidth={1.8} />
        {/if}
      </div>

      <div class="confirm-content">
        <h3 class="confirm-title">{title}</h3>
        <p class="confirm-message">{message}</p>
      </div>

      {#if requireInput}
        <div class="confirm-input-section">
          <p class="confirm-input-label">Type <strong>{requireInput}</strong> to confirm:</p>
          <input
            type="text"
            class="confirm-input"
            bind:value={inputValue}
            placeholder={requireInput}
            autocomplete="off"
            spellcheck="false"
          />
        </div>
      {/if}

      <div class="confirm-actions">
        <button class="btn-cancel" on:click={cancel}>{cancelLabel}</button>
        <button
          class="btn-confirm {variant}"
          on:click={confirm}
          disabled={!canConfirm}
        >
          {confirmLabel}
        </button>
      </div>

      <button class="confirm-close" on:click={cancel} aria-label="Close">
        <X size={16} strokeWidth={2} />
      </button>
    </div>
  </div>
{/if}

<style>
  .confirm-overlay {
    position: fixed;
    inset: 0;
    z-index: 2000;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 1rem;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(3px);
    -webkit-backdrop-filter: blur(3px);
  }

  .confirm-panel {
    position: relative;
    width: 100%;
    max-width: 400px;
    background: var(--bg-surface, #14151a);
    border: 1px solid var(--border-subtle, #2a2b35);
    border-radius: var(--radius-lg, 12px);
    padding: 1.75rem;
    box-shadow: var(--shadow-lg);
    text-align: center;
  }

  .confirm-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 48px;
    height: 48px;
    border-radius: 50%;
    margin-bottom: 1rem;
  }

  .confirm-icon.danger {
    background: rgba(248, 113, 113, 0.1);
    color: var(--accent-red, #f87171);
  }

  .confirm-icon.warning {
    background: rgba(251, 191, 36, 0.1);
    color: var(--accent-orange, #fbbf24);
  }

  .confirm-icon.default {
    background: rgba(96, 165, 250, 0.1);
    color: var(--accent-blue, #60a5fa);
  }

  .confirm-content {
    margin-bottom: 1.25rem;
  }

  .confirm-title {
    margin: 0 0 0.5rem;
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-primary, #e2e4e9);
  }

  .confirm-message {
    margin: 0;
    font-size: 0.8125rem;
    color: var(--text-secondary, #8b8d97);
    line-height: 1.5;
  }

  .confirm-input-section {
    margin-bottom: 1.25rem;
  }

  .confirm-input-label {
    font-size: 0.75rem;
    color: var(--text-muted, #5c5e69);
    margin: 0 0 0.5rem;
  }

  .confirm-input-label strong {
    color: var(--accent-red, #f87171);
    font-weight: 600;
  }

  .confirm-input {
    width: 100%;
    padding: 0.5rem 0.75rem;
    background: var(--bg-elevated, #1c1d24);
    border: 1px solid var(--border-subtle, #2a2b35);
    border-radius: var(--radius-sm, 6px);
    color: var(--text-primary, #e2e4e9);
    font-size: 0.875rem;
    font-family: inherit;
    text-align: center;
    letter-spacing: 0.02em;
  }

  .confirm-input:focus {
    outline: none;
    border-color: var(--accent-red, #f87171);
    box-shadow: 0 0 0 3px rgba(248, 113, 113, 0.1);
  }

  .confirm-actions {
    display: flex;
    gap: 0.625rem;
    justify-content: center;
  }

  .btn-cancel {
    padding: 0.5rem 1.125rem;
    border-radius: var(--radius-sm, 6px);
    border: 1px solid var(--border-default, #33343e);
    background: var(--bg-elevated, #1c1d24);
    color: var(--text-primary, #e2e4e9);
    font-size: 0.8125rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s ease;
    font-family: inherit;
  }

  .btn-cancel:hover {
    background: var(--bg-hover, #22232b);
    border-color: var(--text-muted);
  }

  .btn-confirm {
    padding: 0.5rem 1.125rem;
    border-radius: var(--radius-sm, 6px);
    border: none;
    font-size: 0.8125rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s ease;
    font-family: inherit;
  }

  .btn-confirm:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .btn-confirm.danger {
    background: #dc2626;
    color: white;
  }

  .btn-confirm.danger:hover:not(:disabled) {
    background: #ef4444;
  }

  .btn-confirm.warning {
    background: #d97706;
    color: white;
  }

  .btn-confirm.warning:hover:not(:disabled) {
    background: #f59e0b;
  }

  .btn-confirm.default {
    background: var(--accent-primary);
    color: white;
  }

  .btn-confirm.default:hover:not(:disabled) {
    background: var(--accent-primary-strong);
  }

  .confirm-close {
    position: absolute;
    top: 0.75rem;
    right: 0.75rem;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    background: none;
    border: none;
    color: var(--text-muted, #5c5e69);
    cursor: pointer;
    border-radius: var(--radius-sm, 6px);
    transition: all 0.15s ease;
  }

  .confirm-close:hover {
    color: var(--text-primary, #e2e4e9);
    background: var(--bg-hover, #22232b);
  }
</style>
