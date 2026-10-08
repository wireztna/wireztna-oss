<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';
  import { authStore } from '$lib/stores/auth';

  onMount(() => {
    if ($authStore.isAuthenticated) {
      goto('/dashboard');
    } else {
      goto('/login');
    }
  });
</script>
