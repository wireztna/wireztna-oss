<script lang="ts">
  import { toasts } from '$lib/stores/toast';
  import { fly, fade } from 'svelte/transition';
  import { flip } from 'svelte/animate';
  import { CheckCircle, XCircle, Info, AlertTriangle, X } from 'lucide-svelte';

  const icons = {
    success: CheckCircle,
    error: XCircle,
    info: Info,
    warning: AlertTriangle,
  };
</script>

<div class="toast-container" aria-live="polite">
  {#each $toasts as toast (toast.id)}
    <div
      class="toast toast-{toast.type}"
      in:fly={{ x: 320, duration: 300, opacity: 0 }}
      out:fade={{ duration: 200 }}
      animate:flip={{ duration: 200 }}
    >
      <div class="toast-icon">
        <svelte:component this={icons[toast.type]} size={18} strokeWidth={2} />
      </div>
      <span class="toast-message">{toast.message}</span>
      <button class="toast-close" on:click={() => toasts.remove(toast.id)} aria-label="Dismiss">
        <X size={14} strokeWidth={2} />
      </button>
    </div>
  {/each}
</div>

<style>
  .toast-container {
    position: fixed;
    top: 1rem;
    right: 1rem;
    z-index: 9999;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    max-width: 380px;
    pointer-events: none;
  }

  .toast {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    padding: 0.75rem 1rem;
    border-radius: var(--radius-md, 8px);
    background: var(--bg-surface, #14151a);
    border: 1px solid var(--border-subtle, #2a2b35);
    box-shadow: var(--shadow-lg);
    pointer-events: all;
    min-width: 280px;
  }

  .toast-success {
    border-left: 3px solid var(--accent-green, #34d399);
  }

  .toast-error {
    border-left: 3px solid var(--accent-red, #f87171);
  }

  .toast-info {
    border-left: 3px solid var(--accent-blue, #60a5fa);
  }

  .toast-warning {
    border-left: 3px solid var(--accent-orange, #fbbf24);
  }

  .toast-icon {
    flex-shrink: 0;
    display: flex;
    align-items: center;
  }

  .toast-success .toast-icon { color: var(--accent-green, #34d399); }
  .toast-error .toast-icon { color: var(--accent-red, #f87171); }
  .toast-info .toast-icon { color: var(--accent-blue, #60a5fa); }
  .toast-warning .toast-icon { color: var(--accent-orange, #fbbf24); }

  .toast-message {
    flex: 1;
    font-size: 0.8125rem;
    color: var(--text-primary, #e2e4e9);
    line-height: 1.4;
  }

  .toast-close {
    flex-shrink: 0;
    background: none;
    border: none;
    color: var(--text-muted, #5c5e69);
    cursor: pointer;
    padding: 0.25rem;
    border-radius: 4px;
    display: flex;
    align-items: center;
    transition: color 0.15s, background 0.15s;
  }

  .toast-close:hover {
    color: var(--text-primary, #e2e4e9);
    background: var(--bg-hover, #22232b);
  }
</style>
