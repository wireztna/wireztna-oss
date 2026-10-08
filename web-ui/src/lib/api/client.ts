/**
 * WireZTNA API Client — Typed HTTP client for the control plane API.
 */

// Browser-facing API calls always use the current origin. In development,
// Vite proxies /api to the local control plane; production nginx does the same.
const API_URL = '';

interface RequestOptions {
  method?: string;
  body?: unknown;
  token?: string;
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, token } = options;

  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };

  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const response = await fetch(`${API_URL}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });

  if (!response.ok) {
    const error = await response.json().catch(() => ({ detail: 'Unknown error' }));
    throw new Error(error.detail || `HTTP ${response.status}`);
  }

  if (response.status === 204) return null as T;
  return response.json();
}

// ─── Auth ───
export const auth = {
  login: (email: string, password: string) =>
    request<{ access_token: string; mfa_required?: boolean; password_change_required?: boolean }>('/api/v1/auth/login', {
      method: 'POST',
      body: { email, password },
    }),
  otpRequest: (email: string) =>
    request<{ message: string; email_hint: string | null; expires_in: number }>('/api/v1/auth/otp/request', {
      method: 'POST',
      body: { email },
    }),
  otpVerify: (email: string, code: string) =>
    request<{ access_token: string; mfa_required?: boolean; password_change_required?: boolean }>('/api/v1/auth/otp/verify', {
      method: 'POST',
      body: { email, code },
    }),
  getOIDCConfig: () =>
    request<{
      enabled: boolean;
      provider_url?: string;
      login_url?: string;
      email_enabled?: boolean;
      otp_enabled?: boolean;
      setup_required?: boolean;
    }>('/api/v1/auth/oidc/config'),
  setup: (email: string, password: string, username?: string) =>
    request<{ access_token: string; token_type?: string }>('/api/v1/auth/setup', {
      method: 'POST',
      body: username ? { email, username, password } : { email, password },
    }),
};

// ─── Publishers ───
export const publishers = {
  list: (token: string, orgId?: string) => {
    const query = orgId ? `?org_id=${orgId}` : '';
    return request<any[]>(`/api/v1/publishers${query}`, { token });
  },
  handshakes: (token: string) => request<Record<string, number | null>>('/api/v1/publishers/handshakes', { token }),
  create: (data: any, token: string) => request<any>('/api/v1/publishers', { method: 'POST', body: data, token }),
  get: (id: string, token: string) => request<any>(`/api/v1/publishers/${id}`, { token }),
  update: (id: string, data: any, token: string) => request<any>(`/api/v1/publishers/${id}`, { method: 'PUT', body: data, token }),
  delete: (id: string, token: string) => request<null>(`/api/v1/publishers/${id}`, { method: 'DELETE', token }),
  createToken: (data: any, token: string) => request<any>('/api/v1/publishers/enrollment-tokens', { method: 'POST', body: data, token }),
  reset: (id: string, token: string) => request<null>(`/api/v1/publishers/${id}/reset`, { method: 'POST', token }),
  disable: (id: string, token: string) => request<null>(`/api/v1/publishers/${id}/disable`, { method: 'POST', token }),
  enable: (id: string, token: string) => request<null>(`/api/v1/publishers/${id}/enable`, { method: 'POST', token }),
};

// ─── Users ───
export const users = {
  list: (token: string, orgId?: string) => {
    const query = orgId ? `?org_id=${orgId}` : '';
    return request<any[]>(`/api/v1/users${query}`, { token });
  },
  create: (data: any, token: string, orgId?: string) => {
    const query = orgId ? `?org_id=${orgId}` : '';
    return request<any>(`/api/v1/users${query}`, { method: 'POST', body: data, token });
  },
  get: (id: string, token: string) => request<any>(`/api/v1/users/${id}`, { token }),
  update: (id: string, data: any, token: string) => request<any>(`/api/v1/users/${id}`, { method: 'PUT', body: data, token }),
  delete: (id: string, token: string) => request<null>(`/api/v1/users/${id}`, { method: 'DELETE', token }),
  disable: (id: string, token: string) => request<null>(`/api/v1/users/${id}/disable`, { method: 'POST', token }),
  enable: (id: string, token: string) => request<null>(`/api/v1/users/${id}/enable`, { method: 'POST', token }),
  unenroll: (id: string, token: string) => request<null>(`/api/v1/users/${id}/unenroll`, { method: 'POST', token }),
  sendOnboarding: (id: string, token: string) => request<{ message: string }>(`/api/v1/users/${id}/send-onboarding`, { method: 'POST', token }),
  resetTokens: (id: string, token: string) => request<{ message: string }>(`/api/v1/users/${id}/reset-tokens`, { method: 'POST', token }),
  createEnrollmentToken: (userId: string, data: { expires_in_hours?: number; note?: string }, token: string) =>
    request<EnrollmentTokenResponse>(`/api/v1/users/${userId}/enrollment-token`, { method: 'POST', body: data, token }),
  listEnrollmentTokens: (userId: string, token: string) =>
    request<EnrollmentTokenResponse[]>(`/api/v1/users/${userId}/enrollment-tokens`, { token }),
  revokeEnrollmentToken: (userId: string, tokenId: string, token: string) =>
    request<null>(`/api/v1/users/${userId}/enrollment-tokens/${tokenId}`, { method: 'DELETE', token }),
};

export interface EnrollmentTokenResponse {
  id: string;
  token: string;
  user_id: string;
  username: string;
  expires_at: string;
  used_at: string | null;
  used_from_ip: string | null;
  revoked: boolean;
  created_by: string | null;
  created_at: string;
  note: string | null;
  enroll_url: string;
  device_name: string | null;
  device_platform: string | null;
}

// ─── Groups ───
export const groups = {
  list: (token: string, orgId?: string) => {
    const query = orgId ? `?org_id=${orgId}` : '';
    return request<any[]>(`/api/v1/groups${query}`, { token });
  },
  create: (data: any, token: string, orgId?: string) => {
    const query = orgId ? `?org_id=${orgId}` : '';
    return request<any>(`/api/v1/groups${query}`, { method: 'POST', body: data, token });
  },
  get: (id: string, token: string) => request<any>(`/api/v1/groups/${id}`, { token }),
  update: (id: string, data: any, token: string) => request<any>(`/api/v1/groups/${id}`, { method: 'PUT', body: data, token }),
  delete: (id: string, token: string) => request<null>(`/api/v1/groups/${id}`, { method: 'DELETE', token }),
  getMembers: (groupId: string, token: string) => request<any[]>(`/api/v1/groups/${groupId}/members`, { token }),
  addMembers: (groupId: string, userIds: string[], token: string) =>
    request<null>(`/api/v1/groups/${groupId}/members`, { method: 'POST', body: { user_ids: userIds }, token }),
  removeMember: (groupId: string, userId: string, token: string) =>
    request<null>(`/api/v1/groups/${groupId}/members/${userId}`, { method: 'DELETE', token }),
  getPublishers: (groupId: string, token: string) => request<any[]>(`/api/v1/groups/${groupId}/publishers`, { token }),
  addPublishers: (groupId: string, publisherIds: string[], token: string) =>
    request<null>(`/api/v1/groups/${groupId}/publishers`, { method: 'POST', body: { publisher_ids: publisherIds }, token }),
  removePublisher: (groupId: string, publisherId: string, token: string) =>
    request<null>(`/api/v1/groups/${groupId}/publishers/${publisherId}`, { method: 'DELETE', token }),
  // Access policies
  getPolicy: (groupId: string, publisherId: string, token: string) =>
    request<any>(`/api/v1/groups/${groupId}/publishers/${publisherId}/policy`, { token }),
  setPolicy: (groupId: string, publisherId: string, policy: string, token: string) =>
    request<any>(`/api/v1/groups/${groupId}/publishers/${publisherId}/policy`, { method: 'PUT', body: { access_policy: policy }, token }),
  getRules: (groupId: string, publisherId: string, token: string) =>
    request<any[]>(`/api/v1/groups/${groupId}/publishers/${publisherId}/rules`, { token }),
  addRule: (groupId: string, publisherId: string, rule: { target: string; port: string; protocol: string; name?: string }, token: string) =>
    request<any>(`/api/v1/groups/${groupId}/publishers/${publisherId}/rules`, { method: 'POST', body: rule, token }),
  deleteRule: (groupId: string, publisherId: string, ruleId: string, token: string) =>
    request<null>(`/api/v1/groups/${groupId}/publishers/${publisherId}/rules/${ruleId}`, { method: 'DELETE', token }),
};

// ─── Clients ───
export interface ClientDebugPublisher {
  publisher_id: string;
  publisher_name: string;
  status: string;
  exposed_cidrs: string[];
  endpoint: string | null;
  last_heartbeat: string | null;
  via_groups: string[];
}

export interface ClientDebugSession {
  session_id: string;
  is_active: boolean;
  created_at: string;
  expires_at: string;
  ttl_remaining_seconds: number;
  client_ip: string | null;
  selected_group_id: string | null;
  selected_group_name: string | null;
}

export interface ClientDebugResponse {
  user_id: string;
  username: string;
  overlay_ip: string | null;
  status: string;
  public_key_set: boolean;
  is_connected: boolean;
  endpoint_ip: string | null;
  endpoint_port: number | null;
  last_handshake_at: string | null;
  handshake_age_seconds: number | null;
  rx_bytes: number;
  tx_bytes: number;
  last_reported_at: string | null;
  active_session: ClientDebugSession | null;
  total_sessions: number;
  accessible_publishers: ClientDebugPublisher[];
  total_allowed_cidrs: string[];
  issues: string[];
}

export interface ClientFlowEntry {
  protocol: string;
  dst_ip: string;
  dst_port: number;
  src_port: number;
  state: string;
  bytes_in: number;
  bytes_out: number;
  service: string | null;
  publisher_name: string | null;
  reachable: boolean | null;
}

export interface ClientFlowsResponse {
  overlay_ip: string | null;
  total_flows: number;
  flows: ClientFlowEntry[];
  stale: boolean;
}

export const clients = {
  connected: (token: string) => request<any[]>('/api/v1/clients/connected', { token }),
  history: (userId: string, token: string) => request<any[]>(`/api/v1/clients/${userId}/history`, { token }),
  debug: (userId: string, token: string) => request<ClientDebugResponse>(`/api/v1/clients/${userId}/debug`, { token }),
  flows: (userId: string, token: string) => request<ClientFlowsResponse>(`/api/v1/clients/${userId}/flows`, { token }),
  getConfig: async (userId: string, token: string): Promise<Blob> => {
    const response = await fetch(`${API_URL}/api/v1/clients/${userId}/config`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.blob();
  },
  getQr: async (userId: string, token: string): Promise<Blob> => {
    const response = await fetch(`${API_URL}/api/v1/clients/${userId}/qr`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.blob();
  },
};

// ─── Audit ───
export const audit = {
  logs: (token: string, params?: { user_id?: string; action?: string; limit?: number }) => {
    const query = new URLSearchParams();
    if (params?.user_id) query.set('user_id', params.user_id);
    if (params?.action) query.set('action', params.action);
    if (params?.limit) query.set('limit', params.limit.toString());
    return request<any[]>(`/api/v1/audit/logs?${query}`, { token });
  },
};

// ─── Sessions ───
export const sessions = {
  renew: (token: string) => request<any>('/api/v1/sessions/renew', { method: 'POST', token }),
};

// ─── User Portal (self-service) ───
export interface PortalPublisher {
  name: string;
  status: string;
  exposed_cidrs: string[];
  via_groups: string[];
}

export interface PortalSession {
  session_id: string;
  expires_at: string;
  ttl_remaining_seconds: number;
  selected_group_name: string | null;
}

export interface PortalFlowEntry {
  protocol: string;
  dst_ip: string;
  dst_port: number;
  state: string;
  bytes_in: number;
  bytes_out: number;
  service: string | null;
  publisher_name: string | null;
  reachable: boolean | null;
}

export interface PortalStatusResponse {
  username: string;
  overlay_ip: string | null;
  status: string;
  is_connected: boolean;
  endpoint_ip: string | null;
  last_handshake_at: string | null;
  handshake_age_seconds: number | null;
  rx_bytes: number;
  tx_bytes: number;
  active_session: PortalSession | null;
  accessible_publishers: PortalPublisher[];
  allowed_cidrs: string[];
  issues: string[];
}

export interface PortalFlowsResponse {
  overlay_ip: string | null;
  total_flows: number;
  flows: PortalFlowEntry[];
}

export const me = {
  status: (token: string) => request<PortalStatusResponse>('/api/v1/me', { token }),
  flows: (token: string) => request<PortalFlowsResponse>('/api/v1/me/flows', { token }),
};

// ─── Downloads ───
export interface DownloadFile {
  filename: string;
  platform: string;
  arch: string;
  type: 'installer' | 'package' | 'desktop' | 'cli';
  description: string;
  size_bytes: number;
  sha256: string;
  download_url: string;
  sigstore_bundle_url?: string;
}

export const downloads = {
  list: () => request<{ version: string; files: DownloadFile[] }>('/api/v1/downloads'),
  getUrl: (filename: string) => `/api/v1/downloads/${encodeURIComponent(filename)}`,
};

// ─── Diagnostics ───
export interface DiagnosticSummary {
  status: string;
  timestamp: string;
  publishers_total: number;
  publishers_online: number;
  publishers_offline: number;
  publishers_pending: number;
  clients_total: number;
  clients_connected: number;
  clients_disconnected: number;
  sessions_active: number;
  sessions_expired_recently: number;
  issues: string[];
}

export interface BrokerDiagnosticResult {
  broker_reachable: boolean;
  broker_diagnostic: any | null;
  control_plane_health: DiagnosticSummary | null;
  error: string | null;
}

export const diagnostics = {
  quick: () => request<{ status: string; publishers_online: number; publishers_total: number; clients_connected: number; clients_total: number; issues_count: number }>('/api/v1/diagnostics/quick'),
  health: (token: string, includeHealthy = false) =>
    request<any>(`/api/v1/diagnostics/health?include_healthy=${includeHealthy}`, { token }),
  run: (token: string, opts?: { quick?: boolean; publisher?: string; client?: string }) => {
    const params = new URLSearchParams();
    if (opts?.quick) params.set('quick', 'true');
    if (opts?.publisher) params.set('publisher', opts.publisher);
    if (opts?.client) params.set('client', opts.client);
    const query = params.toString() ? `?${params}` : '';
    return request<BrokerDiagnosticResult>(`/api/v1/diagnostics/run${query}`, { method: 'POST', token });
  },
  diagnosePublisher: (publisherId: string, token: string) =>
    request<any>(`/api/v1/diagnostics/publisher/${publisherId}`, { token }),
};

// ─── Portal (self-service /me) ───
export const portal = {
  profile: (token: string) => request<any>('/api/v1/me/profile', { token }),
  status: (token: string) => request<any>('/api/v1/me', { token }),
  updateEmail: (email: string, currentPassword: string, token: string) =>
    request<any>('/api/v1/me/email', { method: 'PUT', body: { email, current_password: currentPassword }, token }),
  changePassword: (currentPassword: string, newPassword: string, token: string) =>
    request<any>('/api/v1/me/password', { method: 'PUT', body: { current_password: currentPassword, new_password: newPassword }, token }),
  listTokens: (token: string) => request<any[]>('/api/v1/me/tokens', { token }),
  generateToken: (token: string) => request<any>('/api/v1/me/tokens', { method: 'POST', token }),
  access: (token: string) => request<any>('/api/v1/me/access', { token }),
  flows: (token: string) => request<any>('/api/v1/me/flows', { token }),
  // MFA (TOTP)
  mfaStatus: (token: string) => request<{ enabled: boolean }>('/api/v1/me/mfa/status', { token }),
  mfaSetup: (token: string) => request<{ secret: string; otpauth_uri: string; qr_data: string }>('/api/v1/me/mfa/setup', { method: 'POST', token }),
  mfaVerify: (code: string, token: string) => request<{ enabled: boolean }>('/api/v1/me/mfa/verify', { method: 'POST', body: { code }, token }),
  mfaDisable: (code: string, token: string) => request<{ enabled: boolean }>('/api/v1/me/mfa/disable', { method: 'POST', body: { code }, token }),
};

// ─── Announcements ───
export const announcements = {
  active: (token: string) => request<any[]>('/api/v1/announcements/active', { token }),
  list: (token: string) => request<any[]>('/api/v1/announcements/', { token }),
  create: (data: { title: string; message: string; type?: string }, token: string) =>
    request<any>('/api/v1/announcements/', { method: 'POST', body: data, token }),
  update: (id: string, data: any, token: string) =>
    request<any>(`/api/v1/announcements/${id}`, { method: 'PUT', body: data, token }),
  delete: (id: string, token: string) =>
    request<null>(`/api/v1/announcements/${id}`, { method: 'DELETE', token }),
};

// ─── MFA (login step 2) ───
export const mfa = {
  verify: (mfaToken: string, code: string) =>
    request<{ access_token: string; password_change_required?: boolean }>('/api/v1/auth/mfa/verify', { method: 'POST', body: { mfa_token: mfaToken, code } }),
};

// ─── Organizations ───
export interface OrgResponse {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  created_at: string;
  user_count: number;
  group_count: number;
  publisher_count: number;
}

export const organizations = {
  list: (token: string) => request<OrgResponse[]>('/api/v1/organizations', { token }),
  create: (data: { name: string; slug?: string; description?: string }, token: string) =>
    request<OrgResponse>('/api/v1/organizations', { method: 'POST', body: data, token }),
  update: (id: string, data: { name?: string; description?: string }, token: string) =>
    request<OrgResponse>(`/api/v1/organizations/${id}`, { method: 'PUT', body: data, token }),
  delete: (id: string, token: string) =>
    request<null>(`/api/v1/organizations/${id}`, { method: 'DELETE', token }),
};

// ─── Access Passes (delegated access) ───
export const accessPasses = {
  list: (token: string) => request<any[]>('/api/v1/access-passes', { token }),
  get: (passId: string, token: string) => request<any>(`/api/v1/access-passes/${passId}`, { token }),
  create: (data: { label: string; scope: { publishers: string[]; cidrs: string[]; ports: number[]; apps?: string[] }; ttl_seconds: number; metadata?: any }, token: string) =>
    request<any>('/api/v1/access-passes', { method: 'POST', body: data, token }),
  revoke: (passId: string, token: string) =>
    request<any>(`/api/v1/access-passes/${passId}`, { method: 'DELETE', token }),
  // Admin
  listByUser: (userId: string, token: string) => request<any[]>(`/api/v1/access-passes/admin/by-user/${userId}`, { token }),
  adminRevoke: (passId: string, token: string) => request<any>(`/api/v1/access-passes/admin/${passId}`, { method: 'DELETE', token }),
};
