<script lang="ts">
  import { createEventDispatcher, onMount, onDestroy, tick } from 'svelte';
  import { fly } from 'svelte/transition';
  import { MoreVertical } from 'lucide-svelte';

  export let items: Array<{
    label: string;
    icon?: string;
    action: string;
    variant?: 'default' | 'danger';
    disabled?: boolean;
  }> = [];

  let open = false;
  let openAbove = false;
  let menuEl: HTMLDivElement;
  let triggerEl: HTMLButtonElement;
  let dropdownEl: HTMLDivElement;
  let posStyle = '';

  const dispatch = createEventDispatcher();

  async function toggle() {
    open = !open;
    if (open) {
      await tick();
      if (triggerEl) {
        const rect = triggerEl.getBoundingClientRect();
        const spaceBelow = window.innerHeight - rect.bottom;
        openAbove = spaceBelow < 240;

        const top = openAbove
          ? rect.top - 4  // will use bottom positioning via transform
          : rect.bottom + 4;
        const right = window.innerWidth - rect.right;

        if (openAbove) {
          posStyle = `bottom: ${window.innerHeight - rect.top + 4}px; right: ${right}px;`;
        } else {
          posStyle = `top: ${top}px; right: ${right}px;`;
        }
      }
    }
  }

  function handleAction(action: string) {
    open = false;
    dispatch('action', { action });
  }

  function handleClickOutside(e: MouseEvent) {
    if (open && menuEl && !menuEl.contains(e.target as Node)) {
      open = false;
    }
  }

  onMount(() => {
    document.addEventListener('click', handleClickOutside, true);
  });

  onDestroy(() => {
    document.removeEventListener('click', handleClickOutside, true);
  });
</script>

<div class="action-menu" bind:this={menuEl}>
  <button class="action-menu-trigger" bind:this={triggerEl} on:click|stopPropagation={toggle} title="More actions">
    <MoreVertical size={16} strokeWidth={2} />
  </button>

  {#if open}
    <div
      class="action-menu-dropdown"
      bind:this={dropdownEl}
      style={posStyle}
      transition:fly={{ y: openAbove ? 4 : -4, duration: 150, opacity: 0 }}
    >
      {#each items as item}
        <button
          class="action-menu-item"
          class:danger={item.variant === 'danger'}
          disabled={item.disabled}
          on:click|stopPropagation={() => handleAction(item.action)}
        >
          {#if item.icon}<span class="item-icon">{item.icon}</span>{/if}
          {item.label}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .action-menu {
    position: relative;
    display: inline-flex;
  }

  .action-menu-trigger {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    background: var(--bg-elevated, #1c1d24);
    border: 1px solid var(--border-subtle, #2a2b35);
    border-radius: var(--radius-sm, 6px);
    color: var(--text-muted, #5c5e69);
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .action-menu-trigger:hover {
    color: var(--text-primary, #e2e4e9);
    background: var(--bg-hover, #22232b);
    border-color: var(--border-default, #33343e);
  }

  .action-menu-dropdown {
    position: fixed;
    min-width: 180px;
    background: var(--bg-surface, #14151a);
    border: 1px solid var(--border-default, #33343e);
    border-radius: var(--radius-md, 8px);
    box-shadow: var(--shadow-lg);
    z-index: 9999;
    padding: 4px;
    overflow: hidden;
  }

  .action-menu-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.5rem 0.75rem;
    background: none;
    border: none;
    border-radius: var(--radius-sm, 6px);
    color: var(--text-secondary, #8b8d97);
    font-size: 0.8125rem;
    cursor: pointer;
    text-align: left;
    transition: all 0.12s ease;
  }

  .action-menu-item:hover {
    background: var(--bg-hover, #22232b);
    color: var(--text-primary, #e2e4e9);
  }

  .action-menu-item.danger {
    color: var(--accent-red, #f87171);
  }

  .action-menu-item.danger:hover {
    background: var(--accent-red-dim, rgba(248, 113, 113, 0.15));
    color: var(--accent-red, #f87171);
  }

  .action-menu-item:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .item-icon {
    font-size: 0.875rem;
    line-height: 1;
  }
</style>
