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
  note_revision: number;
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
  creation_operation_id: string;
  rotated_from_id?: string;
}

export interface CredentialIssue {
  credential: Credential;
  secret: string;
  effects: { sessions_ended: number; clients_ended: number; tasks_stop_requested: number };
  management_revision: number;
}

export type CredentialChange =
  | {
      kind: 'createCredential';
      input: {
        operation_id: string;
        kind: 'access' | 'mcp';
        name: string;
        expires_in_seconds?: number;
        allow_exec?: boolean;
      };
    }
  | {
      kind: 'rotateCredential';
      id: string;
      input: { operation_id: string; expires_in_seconds?: number };
    }
  | { kind: 'revokeCredential'; id: string };

export type AdminChange = ResourceChange | CredentialChange;

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
const errorModel = validator.compile<{ error: { code: string } }>({
  $ref: `${schema.$id}#/$defs/Error`,
});
const noteModel = validator.compile<{ session: { id: string } }>({
  $ref: `${schema.$id}#/$defs/AdminSessionResponse`,
});
const endSessionModel = validator.compile<{ session: { id: string; state: string } }>({
  $ref: `${schema.$id}#/$defs/SessionResponse`,
});
const endClientModel = validator.compile<{ client: { id: string; lifecycle: string } }>({
  $ref: `${schema.$id}#/$defs/MCPClientEndResponse`,
});
const credentialIssueModel = validator.compile<CredentialIssue>({
  $ref: `${schema.$id}#/$defs/CredentialIssue`,
});
const credentialRevokeModel = validator.compile<{ credential: Credential }>({
  $ref: `${schema.$id}#/$defs/CredentialRevokeResponse`,
});

export type ResourceChange =
  | { kind: 'note'; id: string; note: string; expected_note_revision: number }
  | { kind: 'endSession'; id: string }
  | { kind: 'endClient'; id: string };

export function isCredentialChange(change: AdminChange): change is CredentialChange {
  return (
    change.kind === 'createCredential' ||
    change.kind === 'rotateCredential' ||
    change.kind === 'revokeCredential'
  );
}

const isJSON = (response: Response) =>
  /^application\/json(?:\s*;\s*charset\s*=\s*(?:utf-8|"utf-8"))?$/i.test(
    response.headers.get('Content-Type')?.trim() ?? '',
  );

export class ApiError extends Error {
  constructor(
    public status: number,
    public code?: string,
  ) {
    super('Admin request failed');
  }
}

async function request(
  path: string,
  signal: AbortSignal,
  expectedStatus: 200 | 201 | 204,
  {
    body,
    csrf,
    method = body ? 'POST' : 'GET',
  }: { body?: object; csrf?: string; method?: 'GET' | 'POST' | 'PATCH' } = {},
): Promise<Response> {
  const response = await fetch(`/api/v1/admin/${path}`, {
    method,
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
  if (!response.ok) {
    const body: unknown = isJSON(response)
      ? await response.json().catch(() => undefined)
      : undefined;
    throw new ApiError(response.status, errorModel(body) ? body.error.code : undefined);
  }
  if (response.status !== expectedStatus || (expectedStatus !== 204 && !isJSON(response)))
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
  const response = await request('login', signal, 200, { body: { password } });
  const body: unknown = await response.json();
  if (!loginModel(body)) throw new ApiError(0);
  return body.csrf_token;
}

export async function signOut(csrf: string, signal: AbortSignal): Promise<void> {
  await request('logout', signal, 204, { body: {}, csrf });
}

export async function changeResource(
  change: ResourceChange,
  csrf: string,
  epoch: string,
  signal: AbortSignal,
): Promise<void> {
  const note = change.kind === 'note';
  const path = `${change.kind === 'endClient' ? 'clients' : 'sessions'}/${encodeURIComponent(change.id)}${note ? '' : '/end'}`;
  const response = await request(path, signal, 200, {
    method: note ? 'PATCH' : 'POST',
    body: note ? { note: change.note, expected_note_revision: change.expected_note_revision } : {},
    csrf,
  });
  const body: unknown = await response.json();
  const valid = note
    ? noteModel(body) && body.session.id === change.id
    : change.kind === 'endSession'
      ? endSessionModel(body) && body.session.id === change.id && body.session.state === 'ended'
      : endClientModel(body) && body.client.id === change.id && body.client.lifecycle === 'ended';
  if (!valid || response.headers.get('X-Remota-Epoch') !== epoch) throw new ApiError(0);
}

export async function changeCredential(
  change: CredentialChange,
  csrf: string,
  epoch: string,
  signal: AbortSignal,
): Promise<CredentialIssue | undefined> {
  const path =
    change.kind === 'createCredential'
      ? 'credentials'
      : `credentials/${encodeURIComponent(change.id)}/${change.kind === 'rotateCredential' ? 'rotate' : 'revoke'}`;
  const response = await request(path, signal, change.kind === 'revokeCredential' ? 200 : 201, {
    body: change.kind === 'revokeCredential' ? {} : change.input,
    csrf,
  });
  const body: unknown = await response.json();
  if (response.headers.get('X-Remota-Epoch') !== epoch) throw new ApiError(0);
  if (change.kind === 'revokeCredential') {
    if (
      !credentialRevokeModel(body) ||
      body.credential.id !== change.id ||
      body.credential.status !== 'revoked'
    )
      throw new ApiError(0);
    return;
  }
  if (
    !credentialIssueModel(body) ||
    body.credential.creation_operation_id !== change.input.operation_id ||
    body.credential.status !== 'active' ||
    (change.kind === 'createCredential'
      ? body.credential.kind !== change.input.kind
      : body.credential.rotated_from_id !== change.id || body.credential.id === change.id)
  )
    throw new ApiError(0);
  return body;
}
