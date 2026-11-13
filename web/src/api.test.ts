import { expect, it, vi } from 'vitest';
import messages from '../../docs/contracts/acceptance/messages.json';
import { ApiError, changeResource, readSnapshot, signIn, signOut } from './api';
import type { ResourceChange, Snapshot } from './api';

const snapshot = messages.valid.find((value) => value.id === 'admin.snapshot.ready')!
  .value as unknown as Snapshot;
const login = messages.valid.find((value) => value.id === 'admin.login.response')!.value as {
  csrf_token: string;
};

it.each([['application/json'], ['Application/JSON; charset="UTF-8"']])(
  '登录接受约定的 JSON 媒体类型：%s',
  async (contentType) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(login), {
          headers: { 'Content-Type': contentType },
        }),
      ),
    );
    await expect(signIn('password', new AbortController().signal)).resolves.toBe(login.csrf_token);
  },
);

it.each([
  ['state', 201, 'application/json'],
  ['login', 201, 'application/json'],
  ['state', 200, 'text/plain'],
  ['login', 200, 'application/json; charset=iso-8859-1'],
  ['state', 200, null],
] as const)('%s 拒绝非约定响应：%i / %s', async (path, status, contentType) => {
  const response = new Response(JSON.stringify(path === 'state' ? snapshot : login), { status });
  if (contentType) response.headers.set('Content-Type', contentType);
  else response.headers.delete('Content-Type');
  if (path === 'state') response.headers.set('X-Remota-Epoch', snapshot.server.server_epoch);
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response));
  const signal = new AbortController().signal;
  await expect(
    path === 'state' ? readSnapshot(signal) : signIn('password', signal),
  ).rejects.toMatchObject({
    status: 0,
  });
});

it.each([204, 200])('退出只接受 204：%i', async (status) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status })));
  const result = signOut('csrf', new AbortController().signal);
  if (status === 204) await expect(result).resolves.toBeUndefined();
  else await expect(result).rejects.toBeInstanceOf(ApiError);
});

it.each([
  ['note', 'admin.note.response', 'PATCH'],
  ['endSession', 'admin.end.session.response', 'POST'],
  ['endClient', 'admin.end.client.response', 'POST'],
] as const)('%s 使用约定的方法、正文和应答 Schema', async (kind, fixture, method) => {
  const body = messages.valid.find((value) => value.id === fixture)!.value;
  const id = kind === 'endClient' ? snapshot.clients[0]!.id : snapshot.sessions[0]!.id;
  const change: ResourceChange =
    kind === 'note' ? { kind, id, note: '备注', expected_note_revision: 0 } : { kind, id };
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(body), {
      headers: {
        'Content-Type': 'application/json',
        'X-Remota-Epoch': snapshot.server.server_epoch,
      },
    }),
  );
  vi.stubGlobal('fetch', fetch);
  await changeResource(change, 'csrf', snapshot.server.server_epoch, new AbortController().signal);
  const [path, options] = fetch.mock.calls[0]!;
  expect(path).toBe(
    `/api/v1/admin/${kind === 'endClient' ? 'clients' : 'sessions'}/${id}${kind === 'note' ? '' : '/end'}`,
  );
  expect(options.method).toBe(method);
  expect(options.headers['X-CSRF-Token']).toBe('csrf');
  expect(JSON.parse(options.body)).toEqual(
    kind === 'note' ? { note: '备注', expected_note_revision: 0 } : {},
  );
});

it.each(['schema', 'resource', 'epoch'] as const)('修改拒绝错误的 %s 应答', async (invalid) => {
  const body = structuredClone(
    messages.valid.find((value) => value.id === 'admin.note.response')!.value,
  ) as { session: { id: string } };
  if (invalid === 'resource') body.session.id = 'ses_22222222222222222222222222222222';
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(invalid === 'schema' ? {} : body), {
        headers: {
          'Content-Type': 'application/json',
          'X-Remota-Epoch':
            invalid === 'epoch' ? 'ffffffffffffffffffffffffffffffff' : snapshot.server.server_epoch,
        },
      }),
    ),
  );
  await expect(
    changeResource(
      { kind: 'note', id: snapshot.sessions[0]!.id, note: '草稿', expected_note_revision: 0 },
      'csrf',
      snapshot.server.server_epoch,
      new AbortController().signal,
    ),
  ).rejects.toMatchObject({ status: 0 });
});
