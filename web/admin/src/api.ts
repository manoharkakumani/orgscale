// Client for the orgscale admin APIs. The session cookie is sent by the
// browser; every request also carries the session's CSRF token.

let csrfToken = "";

export function setCSRFToken(token: string) {
  csrfToken = token;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function errorMessage(res: Response): Promise<string> {
  const text = await res.text();
  try {
    const body = JSON.parse(text);
    // /admin/api: {"error"}; Huma (/api/v1): {"title","detail","errors"}.
    const parts = [body.error, body.detail, ...(body.errors ?? []).map((e: { message?: string }) => e.message)];
    const msg = parts.filter(Boolean).join(": ");
    if (msg) return msg;
    if (body.title) return body.title;
  } catch {
    /* not JSON */
  }
  return text.trim() || `${res.status} ${res.statusText}`;
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-Orgscale-CSRF": csrfToken };
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(path, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (res.status === 401) {
    window.dispatchEvent(new Event("orgscale:signed-out"));
  }
  if (!res.ok) throw new ApiError(res.status, await errorMessage(res));
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const get = <T>(path: string) => request<T>("GET", path);
export const post = <T>(path: string, body?: unknown) => request<T>("POST", path, body ?? {});
export const put = <T>(path: string, body?: unknown) => request<T>("PUT", path, body ?? {});
export const del = <T>(path: string, body?: unknown) => request<T>("DELETE", path, body);

// Types returned by /admin/api.

export type Role = "admin" | "viewer";

export interface Me {
  signedIn: boolean;
  email: string;
  name: string;
  picture: string;
  role: Role;
  csrfToken: string;
  expiresAt: string;
  serverUrl: string;
  version: string;
}

export interface Overview {
  users: number;
  disabledUsers: number;
  devices: number;
  onlineDevices: number;
  expiredDevices: number;
  taggedDevices: number;
  assignedDevices: number;
  groups: number;
  deviceRestriction: boolean;
  ssoConfigured: boolean;
  ssoIssuer: string;
  scimEnabled: boolean;
  googleSync: boolean;
  googleSyncStatus: GoogleSyncStatus;
  entraSync: boolean;
  warnings: string[] | null;
}

export interface AdminUser {
  id: number;
  name: string;
  displayName: string;
  email: string;
  provider: string;
  picture: string;
  createdAt: string;
  disabled: boolean;
  disabledAt?: string;
  disabledBy?: string;
  groups: string[];
  devices: number;
  lastSeen?: string;
  role: "" | "admin" | "viewer";
  roleFrom: "" | "console" | "config" | "group";
}

export type PrincipalKind = "user" | "directory_group" | "policy_group" | "tag" | "device" | "everyone" | "self";

export interface Principal {
  kind: PrincipalKind;
  principal: string;
  label?: string;
}

export interface DeviceGrant {
  kind: "user" | "directory_group" | "policy_group";
  principal: string;
  label: string;
}

export interface ServerTag {
  name: string;
  description: string;
  managed: boolean;
  createdAt?: string;
  access: Principal[];
  devices: { id: number; name: string; online: boolean; os: string }[];
  tempSSHUsers: string[];
}

export interface TempAccess {
  id: number;
  grantee: string;
  kind: "user" | "directory_group";
  tag: string;
  reason: string;
  minutes: number;
  ssh: boolean;
  sshUsers: string[];
  status: "active" | "ended" | "expired";
  active: boolean;
  grantedBy: string;
  grantedAt: string;
  expiresAt?: string;
  endedBy?: string;
}

export interface AccessRule {
  id: number;
  kind: "network" | "ssh";
  sshUsers: string[];
  sshCheckPeriod: string;
  name: string;
  protocol: "" | "tcp" | "udp" | "icmp";
  ports: string;
  enabled: boolean;
  sources: Principal[];
  destinations: Principal[];
  updatedAt: string;
  updatedBy: string;
}

export interface EnrollKey {
  key: string;
  expiration: string;
  reusable: boolean;
  ephemeral: boolean;
  tag: string;
  serverUrl: string;
}

export interface AdminDevice {
  id: number;
  name: string;
  hostname: string;
  os: string;
  clientVersion: string;
  owner: string;
  ownerId?: number;
  tags: string[];
  ips: string[];
  online: boolean;
  lastSeen?: string;
  expiry?: string;
  expired: boolean;
  createdAt: string;
  announcedRoutes: string[];
  approvedRoutes: string[];
  access: DeviceGrant[];
}

export interface AdminGroup {
  id?: number;
  name: string;
  displayName: string;
  source: "oidc" | "scim" | "google" | "policy" | "console" | "entra";
  members: string[];
  memberIds: number[];
}

export interface ListSetting {
  value: string[];
  console: string[] | null;
  config: string[];
}

export interface GoogleSyncSettings {
  enabled: boolean;
  adminEmail: string;
  customer: string;
  serviceAccountJson?: string;
  intervalMinutes: number;
}

export interface EntraSyncSettings {
  enabled: boolean;
  intervalMinutes: number;
  tenantId?: string;
  clientId?: string;
  clientSecret?: string;
}

export interface EntraSyncStatus extends GoogleSyncStatus {
  unchanged: boolean;
  warning?: string;
  lastFull: string;
  requests: number;
}

export interface GoogleSyncStatus {
  lastRun: string;
  ok: boolean;
  error?: string;
  users: number;
  groups: number;
  disabled: number;
  created: number;
}

export interface Settings {
  sso: {
    issuer: string;
    clientId: string;
    redirectUrls: string[];
    allowedDomains: ListSetting;
    allowedUsers: ListSetting;
    allowedGroups: ListSetting;
  };
  roles: {
    adminUsers: ListSetting;
    adminGroups: ListSetting;
    viewerUsers: ListSetting;
    viewerGroups: ListSetting;
  };
  deviceRestriction: boolean;
  scim: { enabled: boolean; createdAt: string; baseUrl: string };
  googleSync: { settings: GoogleSyncSettings; hasServiceAccount: boolean; status: GoogleSyncStatus };
  entraSync: {
    settings: EntraSyncSettings;
    hasSecret: boolean;
    status: EntraSyncStatus;
    usesSSOApp: boolean;
    tenantId: string;
    clientId: string;
    ready: boolean;
  };
}

export interface PolicyInfo {
  policy: string;
  effective: string;
  mode: string;
  editable: boolean;
  allowsEverything: boolean;
}

export interface AuditEvent {
  id: number;
  createdAt: string;
  actor: string;
  actorKind: string;
  action: string;
  target: string;
  detail: string;
}

// Types returned by /api/v1.

export interface V1User {
  id: string;
  name: string;
  email?: string;
  displayName?: string;
}

export interface PreAuthKey {
  id: string;
  key: string;
  user: V1User | null;
  reusable: boolean;
  ephemeral: boolean;
  used: boolean;
  expiration: string | null;
  createdAt: string;
  aclTags: string[];
}

export interface ApiKey {
  id: string;
  prefix: string;
  expiration: string | null;
  createdAt: string | null;
  lastSeen: string | null;
}

export interface SSOState {
  serverUrl: string;
  redirectUrls: string[];
  configured: boolean;
  fromConfig: boolean;
  issuer: string;
  clientId: string;
  hasSecret: boolean;
  scope: string[];
}

export interface AdminSessionInfo {
  id: string;
  email: string;
  name: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  remoteAddr: string;
  userAgent: string;
  current: boolean;
}

export interface DNSRecordInfo {
  name: string;
  type: string;
  value: string;
}

export interface DNSSettings {
  magicDNS: boolean;
  overrideLocalDNS: boolean;
  nameservers: string[];
  splitDNS: Record<string, string[]>;
  searchDomains: string[];
  records: DNSRecordInfo[];
  baseDomain: string;
  source: "file" | "console";
  recordsFromFile: boolean;
}
