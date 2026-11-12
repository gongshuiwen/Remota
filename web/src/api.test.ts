import { expect, it, vi } from 'vitest';
import messages from '../../docs/contracts/acceptance/messages.json';
import { ApiError, readSnapshot, signIn, signOut } from './api';
import type { Snapshot } from './api';

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
