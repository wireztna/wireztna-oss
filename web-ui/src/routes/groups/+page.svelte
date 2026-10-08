<script lang="ts">
  import { onMount } from 'svelte';
  import { authStore } from '$lib/stores/auth';
  import { orgStore } from '$lib/stores/org';
  import { groups, users, publishers } from '$lib/api/client';
  import Modal from '$lib/components/Modal.svelte';
  import { toasts } from '$lib/stores/toast';
  import { Users } from 'lucide-svelte';

  let groupList: any[] = [];
  let userList: any[] = [];
  let publisherList: any[] = [];
  let showForm = false;
  let form = { name: '', description: '' };
  let loading = true;
  let expandedGroup: string | null = null;
  let activeTab: Record<string, 'members' | 'publishers' | 'policies'> = {};
  let groupMembers: Record<string, any[]> = {};
  let groupPublishers: Record<string, any[]> = {};
  let selectedUserIds: string[] = [];
  let selectedPublisherIds: string[] = [];
  let memberSearchQuery = '';
  let publisherSearchQuery = '';

  // Group editing state
  let editingGroupId: string | null = null;
  let editGroupForm = { name: '', description: '' };

  // Access policy state
  let policyExpanded: string | null = null; // publisher_id currently showing policy
  let policyData: Record<string, { policy: string; rules: any[] }> = {};
  let ruleForm = { target: '', port: '443', protocol: 'tcp', name: '' };

  onMount(async () => { await loadAll(); });

  // Reload when org selection changes
  $: $orgStore.selectedOrgId, (() => { if ($authStore.token) loadAll(); })();

  async function loadAll() {
    loading = true;
    const token = $authStore.token!;
    const orgId = $orgStore.selectedOrgId || undefined;
    [groupList, userList, publisherList] = await Promise.all([
      groups.list(token, orgId),
      users.list(token, orgId),
      publishers.list(token, orgId),
    ]);
    loading = false;
  }

  async function createGroup() {
    await groups.create(form, $authStore.token!, $orgStore.selectedOrgId || undefined);
    showForm = false;
    toasts.success(`Group "${form.name}" created`);
    form = { name: '', description: '' };
    await loadAll();
  }

  async function deleteGroup(id: string) {
    if (confirm('Delete this group? Users will lose access to its publishers.')) {
      const group = groupList.find(g => g.id === id);
      await groups.delete(id, $authStore.token!);
      toasts.success(`Group "${group?.name || id}" deleted`);
      await loadAll();
    }
  }

  function startEditGroup(group: any) {
    editingGroupId = group.id;
    editGroupForm = { name: group.name, description: group.description || '' };
  }

  async function saveEditGroup() {
    if (!editingGroupId) return;
    await groups.update(editingGroupId, editGroupForm, $authStore.token!);
    editingGroupId = null;
    await loadAll();
  }

  function cancelEditGroup() {
    editingGroupId = null;
  }

  async function toggleExpand(groupId: string) {
    if (expandedGroup === groupId) {
      expandedGroup = null;
      return;
    }
    expandedGroup = groupId;
    if (!activeTab[groupId]) activeTab[groupId] = 'members';
    await loadGroupDetails(groupId);
  }

  async function loadGroupDetails(groupId: string) {
    const token = $authStore.token!;
    try {
      [groupMembers[groupId], groupPublishers[groupId]] = await Promise.all([
        groups.getMembers(groupId, token),
        groups.getPublishers(groupId, token),
      ]);
    } catch {
      groupMembers[groupId] = groupMembers[groupId] || [];
      groupPublishers[groupId] = groupPublishers[groupId] || [];
    }
    groupMembers = groupMembers;
    groupPublishers = groupPublishers;
  }

  function setTab(groupId: string, tab: 'members' | 'publishers' | 'policies') {
    activeTab[groupId] = tab;
    activeTab = activeTab;
    if (tab === 'policies') loadAllPolicies(groupId);
  }

  // ─── Members ───
  async function addMembers(groupId: string) {
    if (selectedUserIds.length === 0) return;
    await groups.addMembers(groupId, selectedUserIds, $authStore.token!);
    const count = selectedUserIds.length;
    selectedUserIds = [];
    memberSearchQuery = '';
    groupMembers[groupId] = await groups.getMembers(groupId, $authStore.token!);
    groupMembers = groupMembers;
    toasts.success(`${count} user${count > 1 ? 's' : ''} added to group`);
  }

  async function removeMember(groupId: string, userId: string) {
    await groups.removeMember(groupId, userId, $authStore.token!);
    groupMembers[groupId] = await groups.getMembers(groupId, $authStore.token!);
    groupMembers = groupMembers;
  }

  function availableUsers(groupId: string): any[] {
    const members = groupMembers[groupId] || [];
    const memberIds = new Set(members.map(m => m.id));
    return userList.filter(u => !memberIds.has(u.id));
  }

  function filteredAvailableUsers(groupId: string): any[] {
    const available = availableUsers(groupId);
    if (!memberSearchQuery.trim()) return available;
    const q = memberSearchQuery.toLowerCase();
    return available.filter(u =>
      u.username.toLowerCase().includes(q) || (u.email && u.email.toLowerCase().includes(q))
    );
  }

  function toggleUserSelection(userId: string) {
    if (selectedUserIds.includes(userId)) {
      selectedUserIds = selectedUserIds.filter(id => id !== userId);
    } else {
      selectedUserIds = [...selectedUserIds, userId];
    }
  }

  function selectAllFilteredUsers(groupId: string) {
    const filtered = filteredAvailableUsers(groupId);
    selectedUserIds = filtered.map(u => u.id);
  }

  function clearUserSelection() {
    selectedUserIds = [];
  }

  // ─── Publishers ───
  async function addPublishers(groupId: string) {
    if (selectedPublisherIds.length === 0) return;
    await groups.addPublishers(groupId, selectedPublisherIds, $authStore.token!);
    const count = selectedPublisherIds.length;
    selectedPublisherIds = [];
    publisherSearchQuery = '';
    groupPublishers[groupId] = await groups.getPublishers(groupId, $authStore.token!);
    groupPublishers = groupPublishers;
    toasts.success(`${count} publisher${count > 1 ? 's' : ''} assigned to group`);
  }

  async function removePublisher(groupId: string, publisherId: string) {
    await groups.removePublisher(groupId, publisherId, $authStore.token!);
    groupPublishers[groupId] = await groups.getPublishers(groupId, $authStore.token!);
    groupPublishers = groupPublishers;
  }

  function availablePublishers(groupId: string): any[] {
    const assigned = groupPublishers[groupId] || [];
    const assignedIds = new Set(assigned.map(p => p.id));
    return publisherList.filter(p => !assignedIds.has(p.id));
  }

  function filteredAvailablePublishers(groupId: string): any[] {
    const available = availablePublishers(groupId);
    if (!publisherSearchQuery.trim()) return available;
    const q = publisherSearchQuery.toLowerCase();
    return available.filter(p =>
      p.name.toLowerCase().includes(q) || (p.location && p.location.toLowerCase().includes(q))
    );
  }

  function togglePublisherSelection(publisherId: string) {
    if (selectedPublisherIds.includes(publisherId)) {
      selectedPublisherIds = selectedPublisherIds.filter(id => id !== publisherId);
    } else {
      selectedPublisherIds = [...selectedPublisherIds, publisherId];
    }
  }

  function selectAllFilteredPublishers(groupId: string) {
    const filtered = filteredAvailablePublishers(groupId);
    selectedPublisherIds = filtered.map(p => p.id);
  }

  function clearPublisherSelection() {
    selectedPublisherIds = [];
  }

  // ─── Access Policies ───
  async function togglePolicy(groupId: string, publisherId: string) {
    if (policyExpanded === publisherId) {
      policyExpanded = null;
      return;
    }
    policyExpanded = publisherId;
    await loadPolicy(groupId, publisherId);
  }

  async function loadPolicy(groupId: string, publisherId: string) {
    const token = $authStore.token!;
    try {
      const policy = await groups.getPolicy(groupId, publisherId, token);
      policyData[publisherId] = {
        policy: policy.access_policy,
        rules: policy.rules || [],
      };
    } catch {
      policyData[publisherId] = { policy: 'unrestricted', rules: [] };
    }
    policyData = policyData;
  }

  async function switchPolicy(groupId: string, publisherId: string, newPolicy: string) {
    const token = $authStore.token!;
    await groups.setPolicy(groupId, publisherId, newPolicy, token);
    await loadPolicy(groupId, publisherId);
  }

  async function addRule(groupId: string, publisherId: string) {
    if (!ruleForm.target) return;
    const token = $authStore.token!;
    await groups.addRule(groupId, publisherId, {
      target: ruleForm.target,
      port: ruleForm.port || '0',
      protocol: ruleForm.protocol,
      name: ruleForm.name || undefined,
    }, token);
    ruleForm = { target: '', port: '443', protocol: 'tcp', name: '' };
    await loadPolicy(groupId, publisherId);
  }

  async function deleteRule(groupId: string, publisherId: string, ruleId: string) {
    await groups.deleteRule(groupId, publisherId, ruleId, $authStore.token!);
    await loadPolicy(groupId, publisherId);
  }

  async function loadAllPolicies(groupId: string) {
    const pubs = groupPublishers[groupId] || [];
    for (const pub of pubs) {
      await loadPolicy(groupId, pub.id);
    }
  }
</script>

<div class="page-header">
  <div class="page-header-content">
    <h1>Groups</h1>
    <p class="page-subtitle">Access groups control which users can reach which private networks. Each group connects users (members) to publishers (network agents).</p>
  </div>
  <div class="page-header-actions">
    <button class="btn-primary" on:click={() => showForm = !showForm}>
      {showForm ? 'Cancel' : '+ New Group'}
    </button>
  </div>
</div>

{#if !loading}
  <section class="mesh-domain-summary mesh-only mesh-groups-summary" aria-labelledby="mesh-groups-title">
    <div class="mesh-domain-copy">
      <span class="mesh-domain-eyebrow"><i></i> Policy relationship graph</span>
      <h2 id="mesh-groups-title">Access becomes<br /><em>a visible path.</em></h2>
      <p>Compose identities, private edges and resource policies without losing sight of who can reach what.</p>
      <div class="mesh-domain-status">
        <span><strong>{groupList.length}</strong> policy groups</span>
        <span><strong>{userList.length}</strong> identities</span>
        <span><strong>{publisherList.length}</strong> publishers</span>
      </div>
    </div>
    <div class="mesh-domain-map access-map" aria-hidden="true">
      <span class="access-map-node access-users"><strong>{userList.length}</strong><small>Users</small></span>
      <span class="access-map-arrow arrow-one">→</span>
      <span class="access-map-node access-groups"><strong>{groupList.length}</strong><small>Groups</small></span>
      <span class="access-map-arrow arrow-two">→</span>
      <span class="access-map-node access-publishers"><strong>{publisherList.length}</strong><small>Publishers</small></span>
      <span class="access-map-orbit"></span>
    </div>
  </section>
{/if}

<div class="access-flow-banner">
  <div class="flow-diagram">
    <span class="flow-node"><span class="flow-icon">👤</span> Users</span>
    <span class="flow-arrow">→</span>
    <span class="flow-node highlight"><span class="flow-icon">📁</span> Group</span>
    <span class="flow-arrow">→</span>
    <span class="flow-node"><span class="flow-icon">🔌</span> Publishers</span>
    <span class="flow-arrow">→</span>
    <span class="flow-node"><span class="flow-icon">🌐</span> CIDRs & Apps</span>
  </div>
  <details class="how-it-works">
    <summary>How does access work?</summary>
    <div class="how-steps">
      <p><strong>1.</strong> Add <strong>members</strong> — users who need access to private resources</p>
      <p><strong>2.</strong> Assign <strong>publishers</strong> — agents deployed in your networks that expose internal CIDRs</p>
      <p><strong>3.</strong> Optionally set <strong>access policies</strong> — restrict members to specific apps, ports, or protocols</p>
      <p class="how-result">Result: members can only reach the CIDRs and apps of the publishers assigned to their groups.</p>
    </div>
  </details>
</div>

<Modal title="New Group" open={showForm} on:close={() => { showForm = false; }}>
  <form on:submit|preventDefault={createGroup}>
    <label>Name <input type="text" bind:value={form.name} required placeholder="e.g., Engineering, Remote Workers" /></label>
    <label>Description <input type="text" bind:value={form.description} placeholder="What this group is for" /></label>
    <div class="modal-actions">
      <button type="button" class="btn-secondary" on:click={() => { showForm = false; }}>Cancel</button>
      <button type="submit" class="btn-primary">Create Group</button>
    </div>
  </form>
</Modal>

{#if loading}
  <p>Loading...</p>
{:else if groupList.length === 0}
  <div class="empty-state">
    <Users size={44} strokeWidth={1.2} style="color: var(--text-muted); opacity: 0.6;" />
    <h3>No groups created yet</h3>
    <p>Groups define access. Create one, add users and publishers to control who can reach what.</p>
    <button class="btn-primary" on:click={() => showForm = true}>+ New Group</button>
  </div>
{:else}
  <table class="data-table">
    <thead>
      <tr><th>Name</th><th>Description</th><th>Access</th><th>Actions</th></tr>
    </thead>
    <tbody>
      {#each groupList as group}
        <tr>
          <td>
            {#if editingGroupId === group.id}
              <input type="text" bind:value={editGroupForm.name} class="inline-edit" />
            {:else}
              <strong>{group.name}</strong>
            {/if}
          </td>
          <td>
            {#if editingGroupId === group.id}
              <input type="text" bind:value={editGroupForm.description} class="inline-edit" placeholder="Description" />
            {:else}
              {group.description || '—'}
            {/if}
          </td>
          <td>
            <button class="btn-expand" on:click={() => toggleExpand(group.id)}>
              {expandedGroup === group.id ? '▼ Hide' : '▶ Manage'}
            </button>
          </td>
          <td class="actions-cell">
            {#if editingGroupId === group.id}
              <button class="btn-primary btn-sm" on:click={saveEditGroup}>Save</button>
              <button class="btn-action" on:click={cancelEditGroup}>Cancel</button>
            {:else}
              <button class="btn-action" on:click={() => startEditGroup(group)}>Edit</button>
              <button class="btn-danger" on:click={() => deleteGroup(group.id)}>Delete</button>
            {/if}
          </td>
        </tr>
        {#if expandedGroup === group.id}
          <tr>
            <td colspan="4" class="details-row">
              <div class="tab-bar">
                <button class="tab" class:active={activeTab[group.id] === 'members'} on:click={() => setTab(group.id, 'members')}>
                  Members ({(groupMembers[group.id] || []).length})
                </button>
                <button class="tab" class:active={activeTab[group.id] === 'publishers'} on:click={() => setTab(group.id, 'publishers')}>
                  Publishers ({(groupPublishers[group.id] || []).length})
                </button>
                <button class="tab" class:active={activeTab[group.id] === 'policies'} on:click={() => setTab(group.id, 'policies')}>
                  Access Policies
                </button>
              </div>

              {#if activeTab[group.id] === 'members'}
                <div class="section">
                  <p class="tab-context-hint">Users added here can connect to all publishers assigned to this group.</p>
                  {#if availableUsers(group.id).length > 0}
                    <div class="multiselect-panel">
                      <div class="multiselect-header">
                        <input
                          type="text"
                          class="search-input"
                          placeholder="Search users..."
                          bind:value={memberSearchQuery}
                        />
                        <div class="multiselect-actions">
                          <button class="btn-link" on:click={() => selectAllFilteredUsers(group.id)}>Select all</button>
                          <button class="btn-link" on:click={clearUserSelection}>Clear</button>
                        </div>
                      </div>
                      <div class="multiselect-list">
                        {#each filteredAvailableUsers(group.id) as user}
                          <label class="multiselect-item" class:selected={selectedUserIds.includes(user.id)}>
                            <input
                              type="checkbox"
                              checked={selectedUserIds.includes(user.id)}
                              on:change={() => toggleUserSelection(user.id)}
                            />
                            <span class="multiselect-label">{user.username}</span>
                            {#if user.email}
                              <span class="multiselect-meta">{user.email}</span>
                            {/if}
                          </label>
                        {:else}
                          <em class="hint" style="padding: 0.5rem;">No users match your search.</em>
                        {/each}
                      </div>
                      <div class="multiselect-footer">
                        <span class="selection-count">{selectedUserIds.length} selected</span>
                        <button class="btn-primary" on:click={() => addMembers(group.id)} disabled={selectedUserIds.length === 0}>
                          Add {selectedUserIds.length > 0 ? selectedUserIds.length : ''} Member{selectedUserIds.length !== 1 ? 's' : ''}
                        </button>
                      </div>
                    </div>
                  {:else}
                    <em class="hint">All users are already members of this group.</em>
                  {/if}
                  <div class="badge-list">
                    {#each groupMembers[group.id] || [] as member}
                      <span class="member-badge">
                        {member.username}
                        <button on:click={() => removeMember(group.id, member.id)} title="Remove from group">&times;</button>
                      </span>
                    {:else}
                      <em class="hint">No members yet. Add users above.</em>
                    {/each}
                  </div>
                </div>
              {:else if activeTab[group.id] === 'publishers'}
                <div class="section">
                  <p class="tab-context-hint">Members of this group can reach the CIDRs exposed by these publishers. Remove a publisher to revoke access.</p>
                  {#if availablePublishers(group.id).length > 0}
                    <div class="multiselect-panel">
                      <div class="multiselect-header">
                        <input
                          type="text"
                          class="search-input"
                          placeholder="Search publishers..."
                          bind:value={publisherSearchQuery}
                        />
                        <div class="multiselect-actions">
                          <button class="btn-link" on:click={() => selectAllFilteredPublishers(group.id)}>Select all</button>
                          <button class="btn-link" on:click={clearPublisherSelection}>Clear</button>
                        </div>
                      </div>
                      <div class="multiselect-list">
                        {#each filteredAvailablePublishers(group.id) as pub}
                          <label class="multiselect-item" class:selected={selectedPublisherIds.includes(pub.id)}>
                            <input
                              type="checkbox"
                              checked={selectedPublisherIds.includes(pub.id)}
                              on:change={() => togglePublisherSelection(pub.id)}
                            />
                            <span class="multiselect-label">{pub.name}</span>
                            <span class="multiselect-meta">{pub.location || 'no location'}</span>
                          </label>
                        {:else}
                          <em class="hint" style="padding: 0.5rem;">No publishers match your search.</em>
                        {/each}
                      </div>
                      <div class="multiselect-footer">
                        <span class="selection-count">{selectedPublisherIds.length} selected</span>
                        <button class="btn-primary" on:click={() => addPublishers(group.id)} disabled={selectedPublisherIds.length === 0}>
                          Add {selectedPublisherIds.length > 0 ? selectedPublisherIds.length : ''} Publisher{selectedPublisherIds.length !== 1 ? 's' : ''}
                        </button>
                      </div>
                    </div>
                  {:else}
                    <em class="hint">All publishers are already assigned to this group.</em>
                  {/if}
                  {#if (groupPublishers[group.id] || []).length === 0}
                    <em class="hint">No publishers assigned. Members won't have access to any resources until you add publishers here.</em>
                  {:else}
                    <div class="publishers-section">
                      {#each groupPublishers[group.id] || [] as pub}
                        <div class="pub-card">
                          <div class="pub-card-header">
                            <span class="publisher-badge">
                              {pub.name}
                              {#if pub.exposed_cidrs?.length}
                                <span class="cidr-hint">({pub.exposed_cidrs.join(', ')})</span>
                              {/if}
                            </span>
                            <div class="pub-card-actions">
                              <button class="btn-action" on:click={() => togglePolicy(group.id, pub.id)}>
                                {policyExpanded === pub.id ? 'Hide Policy' : 'Access Policy'}
                              </button>
                              <button class="btn-remove" on:click={() => removePublisher(group.id, pub.id)} title="Remove access">&times;</button>
                            </div>
                          </div>

                          {#if policyExpanded === pub.id && policyData[pub.id]}
                            {@const pd = policyData[pub.id]}
                            <div class="policy-panel">
                              <div class="policy-toggle">
                                <span class="policy-label">Access mode:</span>
                                <button
                                  class="policy-btn" class:active={pd.policy === 'unrestricted'}
                                  on:click={() => switchPolicy(group.id, pub.id, 'unrestricted')}
                                >Unrestricted (full CIDR)</button>
                                <button
                                  class="policy-btn" class:active={pd.policy === 'restricted'}
                                  on:click={() => switchPolicy(group.id, pub.id, 'restricted')}
                                >Restricted (apps only)</button>
                              </div>

                              {#if pd.policy === 'restricted'}
                                <div class="rules-section">
                                  <p class="rules-hint">Only the apps below are reachable. DNS (port 53) is always allowed.</p>

                                  <!-- Existing rules -->
                                  {#if pd.rules.length > 0}
                                    <table class="rules-table">
                                      <thead><tr><th>Target</th><th>Port</th><th>Protocol</th><th>Name</th><th></th></tr></thead>
                                      <tbody>
                                        {#each pd.rules as rule}
                                          <tr>
                                            <td><code>{rule.target}</code></td>
                                            <td>{rule.port === '0' ? 'any' : rule.port}</td>
                                            <td>{rule.protocol}</td>
                                            <td>{rule.name || '—'}</td>
                                            <td><button class="btn-remove-sm" on:click={() => deleteRule(group.id, pub.id, rule.id)}>&times;</button></td>
                                          </tr>
                                        {/each}
                                      </tbody>
                                    </table>
                                  {:else}
                                    <p class="hint">No rules defined — all traffic to this publisher is blocked (except DNS).</p>
                                  {/if}

                                  <!-- Add rule form -->
                                  <form class="rule-form" on:submit|preventDefault={() => addRule(group.id, pub.id)}>
                                    <input type="text" bind:value={ruleForm.target} placeholder="Target (IP, CIDR, or FQDN)" required />
                                    <input type="text" bind:value={ruleForm.port} placeholder="Port (443, 8080-8090)" />
                                    <select bind:value={ruleForm.protocol}>
                                      <option value="tcp">TCP</option>
                                      <option value="udp">UDP</option>
                                      <option value="icmp">ICMP</option>
                                      <option value="any">Any</option>
                                    </select>
                                    <input type="text" bind:value={ruleForm.name} placeholder="Label (optional)" />
                                    <button type="submit" class="btn-primary">Add Rule</button>
                                  </form>
                                </div>
                              {:else}
                                <p class="hint">Full access to all exposed CIDRs of this publisher (default behavior).</p>
                              {/if}
                            </div>
                          {/if}
                        </div>
                      {/each}
                    </div>
                  {/if}
                </div>
              {:else if activeTab[group.id] === 'policies'}
                <div class="section policies-section">
                  <p class="tab-context-hint">Restrict which specific apps/ports members can reach on each publisher. Default is unrestricted (full CIDR access).</p>
                  {#if (groupPublishers[group.id] || []).length === 0}
                    <em class="hint">Assign publishers first, then configure access policies here.</em>
                  {:else}
                    <p class="policy-intro">Control what each publisher allows. <strong>Unrestricted</strong> = full CIDR access. <strong>Restricted</strong> = only listed apps.</p>
                    {#each groupPublishers[group.id] || [] as pub}
                      {@const pd = policyData[pub.id]}
                      <div class="policy-card">
                        <div class="policy-card-header">
                          <strong>{pub.name}</strong>
                          {#if pub.exposed_cidrs?.length}
                            <span class="cidr-hint">{pub.exposed_cidrs.join(', ')}</span>
                          {/if}
                          <div class="policy-toggle-inline">
                            {#if pd}
                              <button class="policy-btn" class:active={pd.policy === 'unrestricted'} on:click={() => switchPolicy(group.id, pub.id, 'unrestricted')}>Unrestricted</button>
                              <button class="policy-btn" class:active={pd.policy === 'restricted'} on:click={() => switchPolicy(group.id, pub.id, 'restricted')}>Restricted</button>
                            {:else}
                              <span class="text-muted">Loading...</span>
                            {/if}
                          </div>
                        </div>

                        {#if pd?.policy === 'restricted'}
                          <div class="policy-rules-inline">
                            {#if pd.rules.length > 0}
                              <table class="rules-table">
                                <thead><tr><th>Target</th><th>Port</th><th>Protocol</th><th>Name</th><th></th></tr></thead>
                                <tbody>
                                  {#each pd.rules as rule}
                                    <tr>
                                      <td><code>{rule.target}</code></td>
                                      <td>{rule.port === '0' ? 'any' : rule.port}</td>
                                      <td>{rule.protocol}</td>
                                      <td>{rule.name || '—'}</td>
                                      <td><button class="btn-remove-sm" on:click={() => deleteRule(group.id, pub.id, rule.id)}>&times;</button></td>
                                    </tr>
                                  {/each}
                                </tbody>
                              </table>
                            {:else}
                              <p class="hint">No rules — all traffic blocked (except DNS). Add apps below.</p>
                            {/if}

                            <form class="rule-form" on:submit|preventDefault={() => addRule(group.id, pub.id)}>
                              <input type="text" bind:value={ruleForm.target} placeholder="Target (IP, CIDR, or FQDN)" required />
                              <input type="text" bind:value={ruleForm.port} placeholder="Port" />
                              <select bind:value={ruleForm.protocol}>
                                <option value="tcp">TCP</option>
                                <option value="udp">UDP</option>
                                <option value="icmp">ICMP</option>
                                <option value="any">Any</option>
                              </select>
                              <input type="text" bind:value={ruleForm.name} placeholder="Label" />
                              <button type="submit" class="btn-primary">Add</button>
                            </form>
                          </div>
                        {/if}
                      </div>
                    {/each}
                  {/if}
                </div>
              {/if}
            </td>
          </tr>
        {/if}
      {/each}
    </tbody>
  </table>
{/if}

<style>
  .empty-state { background: var(--bg-surface); border: 1px solid var(--border-subtle); padding: 2rem; border-radius: var(--radius-md); text-align: center; color: var(--text-secondary); }
  .hint { color: var(--text-muted); font-size: 0.85rem; }

  .btn-expand { background: var(--bg-elevated); border: 1px solid var(--border-subtle); padding: 0.3rem 0.6rem; border-radius: var(--radius-sm); cursor: pointer; font-size: 0.8rem; color: var(--text-secondary); transition: all var(--transition); }
  .btn-expand:hover { background: var(--bg-hover); color: var(--text-primary); border-color: var(--border-default); }
  .details-row { background: var(--bg-elevated); }
  .tab-bar { display: flex; gap: 0; border-bottom: 1px solid var(--border-subtle); margin-bottom: 1rem; }
  .tab { background: none; border: none; padding: 0.5rem 1rem; cursor: pointer; font-size: 0.85rem; color: var(--text-muted); border-bottom: 2px solid transparent; font-family: inherit; transition: color var(--transition); }
  .tab:hover { color: var(--text-secondary); }
  .tab.active { color: var(--text-primary); border-bottom-color: var(--accent-primary); font-weight: 600; }
  .section { padding: 0.5rem 0; }
  .add-row { display: flex; gap: 0.5rem; margin-bottom: 0.75rem; }
  .add-row select { flex: 1; }
  .badge-list { display: flex; flex-wrap: wrap; gap: 0.5rem; }
  .member-badge { background: var(--accent-blue-dim); color: var(--accent-blue); padding: 0.3rem 0.6rem; border-radius: var(--radius-sm); font-size: 0.82rem; display: inline-flex; align-items: center; gap: 0.4rem; }
  .member-badge button { background: none; border: none; color: var(--accent-red); cursor: pointer; font-size: 1.1rem; line-height: 1; font-weight: bold; }
  .publisher-badge { background: var(--accent-orange-dim); color: var(--accent-orange); padding: 0.3rem 0.6rem; border-radius: var(--radius-sm); font-size: 0.82rem; display: inline-flex; align-items: center; gap: 0.4rem; }
  .publisher-badge button { background: none; border: none; color: var(--accent-red); cursor: pointer; font-size: 1.1rem; line-height: 1; font-weight: bold; }
  .cidr-hint { font-size: 0.72rem; color: var(--text-muted); }

  /* Publishers section with policy */
  .publishers-section { display: flex; flex-direction: column; gap: 0.75rem; }
  .pub-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); padding: 0.6rem 0.8rem; }
  .pub-card-header { display: flex; align-items: center; justify-content: space-between; }
  .pub-card-actions { display: flex; gap: 0.4rem; align-items: center; }
  .btn-remove { background: none; border: none; color: var(--accent-red); cursor: pointer; font-size: 1.2rem; font-weight: bold; padding: 0 0.3rem; }
  .btn-remove:hover { opacity: 0.7; }

  /* Policy panel */
  .policy-panel { margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px solid var(--border-subtle); }
  .policy-toggle { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.75rem; }
  .policy-label { font-size: 0.8rem; color: var(--text-secondary); font-weight: 500; }
  .policy-btn { padding: 0.3rem 0.7rem; font-size: 0.78rem; border-radius: var(--radius-sm); cursor: pointer; border: 1px solid var(--border-subtle); background: var(--bg-elevated); color: var(--text-secondary); transition: all var(--transition); }
  .policy-btn:hover { border-color: var(--border-default); color: var(--text-primary); }
  .policy-btn.active { background: var(--accent-blue-dim); color: var(--accent-blue); border-color: var(--accent-blue); font-weight: 600; }

  /* Rules */
  .rules-section { margin-top: 0.5rem; }
  .rules-hint { font-size: 0.78rem; color: var(--text-muted); margin: 0 0 0.6rem; }
  .rules-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; margin-bottom: 0.75rem; }
  .rules-table th { text-align: left; padding: 0.35rem 0.5rem; color: var(--text-muted); font-size: 0.72rem; text-transform: uppercase; border-bottom: 1px solid var(--border-subtle); }
  .rules-table td { padding: 0.35rem 0.5rem; border-bottom: 1px solid var(--border-subtle); }
  .btn-remove-sm { background: none; border: none; color: var(--accent-red); cursor: pointer; font-size: 1rem; font-weight: bold; }

  .rule-form { display: flex; gap: 0.4rem; align-items: center; flex-wrap: wrap; }
  .rule-form input, .rule-form select { padding: 0.35rem 0.5rem; font-size: 0.8rem; }
  .rule-form input[placeholder*="Target"] { flex: 1; min-width: 140px; }
  .rule-form input[placeholder*="Port"] { width: 100px; }
  .rule-form input[placeholder*="Label"] { width: 120px; }
  .rule-form select { width: 80px; }

  /* Policies tab */
  .policies-section { padding: 0.5rem 0; }
  .policy-intro { font-size: 0.82rem; color: var(--text-secondary); margin: 0 0 1rem; }
  .policy-card { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); padding: 0.75rem; margin-bottom: 0.75rem; }
  .policy-card-header { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
  .policy-card-header strong { color: var(--text-primary); font-size: 0.88rem; }
  .policy-toggle-inline { margin-left: auto; display: flex; gap: 0.3rem; }
  .policy-rules-inline { margin-top: 0.75rem; padding-top: 0.6rem; border-top: 1px solid var(--border-subtle); }
  .text-muted { color: var(--text-muted); font-size: 0.82rem; }

  /* Inline editing */
  .inline-edit { width: 100%; padding: 0.3rem 0.5rem; font-size: 0.85rem; }
  .actions-cell { display: flex; gap: 0.3rem; }
  .btn-sm { padding: 0.3rem 0.6rem; font-size: 0.78rem; }

  /* Multi-select panel */
  .multiselect-panel { background: var(--bg-surface); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); margin-bottom: 0.75rem; overflow: hidden; }
  .multiselect-header { display: flex; align-items: center; gap: 0.5rem; padding: 0.5rem 0.6rem; border-bottom: 1px solid var(--border-subtle); }
  .search-input { flex: 1; padding: 0.35rem 0.5rem; font-size: 0.82rem; background: var(--bg-elevated); border: 1px solid var(--border-subtle); border-radius: var(--radius-sm); color: var(--text-primary); }
  .search-input::placeholder { color: var(--text-muted); }
  .multiselect-actions { display: flex; gap: 0.4rem; }
  .btn-link { background: none; border: none; color: var(--accent-blue); cursor: pointer; font-size: 0.78rem; padding: 0.2rem 0.3rem; border-radius: var(--radius-sm); transition: all var(--transition); }
  .btn-link:hover { background: var(--accent-blue-dim); }
  .multiselect-list { max-height: 180px; overflow-y: auto; padding: 0.3rem 0; }
  .multiselect-item { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.6rem; cursor: pointer; transition: background var(--transition); font-size: 0.82rem; }
  .multiselect-item:hover { background: var(--bg-hover); }
  .multiselect-item.selected { background: var(--accent-blue-dim); }
  .multiselect-item input[type="checkbox"] { width: 14px; height: 14px; accent-color: var(--accent-primary); cursor: pointer; flex-shrink: 0; }
  .multiselect-label { color: var(--text-primary); font-weight: 500; }
  .multiselect-meta { color: var(--text-muted); font-size: 0.75rem; margin-left: auto; }
  .multiselect-footer { display: flex; align-items: center; justify-content: space-between; padding: 0.5rem 0.6rem; border-top: 1px solid var(--border-subtle); background: var(--bg-elevated); }
  .selection-count { font-size: 0.78rem; color: var(--text-muted); }

  /* Access flow banner */
  .access-flow-banner {
    background: var(--bg-surface);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    padding: 1rem 1.25rem;
    margin-bottom: 1.5rem;
  }

  .flow-diagram {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    justify-content: center;
  }

  .flow-node {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.4rem 0.75rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-sm);
    font-size: 0.82rem;
    font-weight: 500;
    color: var(--text-secondary);
  }

  .flow-node.highlight {
    background: var(--accent-blue-dim);
    border-color: var(--accent-blue);
    color: var(--accent-blue);
  }

  .flow-icon {
    font-size: 0.9rem;
    line-height: 1;
  }

  .flow-arrow {
    color: var(--text-muted);
    font-size: 0.85rem;
    font-weight: 500;
  }

  .how-it-works {
    margin-top: 0.75rem;
  }

  .how-it-works summary {
    font-size: 0.8rem;
    color: var(--accent-blue);
    cursor: pointer;
    font-weight: 500;
    padding: 0.25rem 0;
    user-select: none;
  }

  .how-it-works summary:hover {
    text-decoration: underline;
  }

  .how-steps {
    margin-top: 0.5rem;
    padding-left: 0.5rem;
    border-left: 2px solid var(--border-subtle);
  }

  .how-steps p {
    font-size: 0.8rem;
    color: var(--text-secondary);
    margin: 0.3rem 0;
    padding-left: 0.5rem;
  }

  .how-result {
    margin-top: 0.5rem !important;
    padding: 0.4rem 0.6rem !important;
    background: var(--accent-green-dim);
    border-radius: var(--radius-sm);
    color: var(--accent-green) !important;
    font-weight: 500;
  }

  /* Tab context hints */
  .tab-context-hint {
    font-size: 0.78rem;
    color: var(--text-muted);
    margin: 0 0 0.75rem;
    padding: 0.4rem 0.6rem;
    background: var(--bg-surface);
    border-left: 2px solid var(--accent-blue);
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  }
</style>
