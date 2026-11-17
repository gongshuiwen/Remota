import { expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import type { Snapshot } from '../src/api';

export async function login(page: Page, password: string) {
  await page.getByLabel('管理员口令').fill(password);
  await page.getByRole('button', { name: '登录管理台', exact: true }).click();
  await expect(page.getByRole('heading', { name: '设备会话', exact: true })).toBeVisible();
}

export async function openCredential(page: Page, name: string, kind: 'access' | 'mcp' = 'access') {
  await page.getByRole('button', { name: /^凭据/ }).click();
  await page.getByRole('button', { name: '签发凭据', exact: true }).click();
  const form = page.getByRole('form', { name: '签发凭据' });
  await form.getByLabel('凭据类型').selectOption(kind);
  await form.getByLabel('凭据名称').fill(name);
  return form;
}

export async function receiveSecret(page: Page, checkCopy = false) {
  const dialog = page.getByRole('dialog', { name: '一次性秘密' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByLabel('凭据秘密')).toHaveAttribute('type', 'password');
  const secret = await dialog.getByLabel('凭据秘密').inputValue();
  const id = (await dialog.locator('code').textContent())!;
  if (checkCopy) {
    await page.evaluate(() =>
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: { writeText: () => Promise.reject(new Error('denied')) },
      }),
    );
    await dialog.getByRole('button', { name: '复制秘密', exact: true }).click();
    await expect(dialog.getByRole('status')).toContainText('复制失败');
    await dialog.getByRole('button', { name: '显示秘密', exact: true }).click();
    await expect(dialog.getByLabel('凭据秘密')).toHaveAttribute('type', 'text');
  }
  if (checkCopy) await dialog.getByRole('button', { name: '我已保存，关闭', exact: true }).click();
  else await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  expect((await page.content()).includes(secret)).toBe(false);
  await page.getByRole('button', { name: '关闭', exact: true }).click();
  return { id, secret };
}

export async function snapshot(page: Page): Promise<Snapshot> {
  return page.evaluate(async () => {
    const response = await fetch('/api/v1/admin/state', { cache: 'no-store' });
    if (response.status !== 200) throw new Error('Cannot read browser-test snapshot');
    return response.json();
  });
}
