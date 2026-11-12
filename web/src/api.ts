import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import schema from '../../docs/contracts/schemas/messages.schema.json';

export interface Session {
  id: string;
  state: 'pending' | 'active' | 'ended';
  created_at: string;
  expires_at: string;
  ended_at?: string;
  end_reason?: string;
  cleanup?: 'not_needed' | 'pending' | 'confirmed' | 'unknown';
  consent?: { mode: 'readonly' | 'full'; revision: number };
  connection: { activity: 'never_seen' | 'recent' | 'stale'; last_seen_at?: string };
  access_credential_id: string;
  note: string;
  cached_info?: { hostname: string; os: string; arch: string; agent_version: string };
}

export interface MCPClient {
  id: string;
  credential_id: string;
  client_info: { name: string; version: string };
  lifecycle: 'initializing' | 'ready' | 'ended';
  activity: 'recent' | 'idle' | 'expired';
  created_at: string;
  last_seen_at: string;
  expires_at: string;
}

export interface Credential {
  id: string;
  name: string;
  kind: 'access' | 'mcp';
  status: 'active' | 'expired' | 'revoked';
  allow_exec?: boolean;
  created_at: string;
  expires_at: string;
  effects_count: { live_sessions: number; live_clients: number; unsettled_tasks: number };
}

export interface Snapshot {
  server: {
    version: string;
    server_epoch: string;
    health: 'ready' | 'degraded';
    management_revision: number;
    public_url?: string;
  };
  server_time: string;
  csrf_token: string;
  admin_session: { expires_at: string };
  transport: {
    public_mode: 'https' | 'loopback_http' | 'insecure_http' | 'unconfigured';
    backend_mode: 'http';
  };
  summary: {
    live_sessions: number;
    cleanup_pending_sessions: number;
    ready_clients: number;
    active_access_credentials: number;
    active_mcp_credentials: number;
    expiring_credentials: number;
    expiring_sessions: number;
  };
  sessions: Session[];
  clients: MCPClient[];
  credentials: Credential[];
}

// Runtime validation reads the canonical Schema; TypeScript types are view DTOs.
const validator = new Ajv2020({ strict: false, validateFormats: true });
addFormats(validator);
validator.addSchema(schema);
const snapshotModel = validator.compile<Snapshot>({ $ref: `${schema.$id}#/$defs/AdminSnapshot` });
const loginModel = validator.compile<{ csrf_token: string; expires_at: string }>({
  $ref: `${schema.$id}#/$defs/AdminLoginResponse`,
});

export class ApiError extends Error {
  constructor(public status: number) {
    super('Admin request failed');
  }
}

async function request(
  path: string,
  signal: AbortSignal,
  expectedStatus: 200 | 204,
  body?: object,
  csrf?: string,
): Promise<Response> {
  const response = await fetch(`/api/v1/admin/${path}`, {
    method: body ? 'POST' : 'GET',
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
  });
  if (!response.ok) throw new ApiError(response.status);
  if (
    response.status !== expectedStatus ||
    (expectedStatus !== 204 &&
      !/^application\/json(?:\s*;\s*charset\s*=\s*(?:utf-8|"utf-8"))?$/i.test(
        response.headers.get('Content-Type')?.trim() ?? '',
      ))
  )
    throw new ApiError(0);
  return response;
}

export async function readSnapshot(signal: AbortSignal): Promise<Snapshot> {
  const response = await request('state', signal, 200);
  const body: unknown = await response.json();
  if (!snapshotModel(body) || body.server.server_epoch !== response.headers.get('X-Remota-Epoch'))
    throw new ApiError(0);
  return body;
}

export async function signIn(password: string, signal: AbortSignal): Promise<string> {
  const response = await request('login', signal, 200, { password });
  const body: unknown = await response.json();
  if (!loginModel(body)) throw new ApiError(0);
  return body.csrf_token;
}

export async function signOut(csrf: string, signal: AbortSignal): Promise<void> {
  await request('logout', signal, 204, {}, csrf);
}
