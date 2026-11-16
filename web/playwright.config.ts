import { defineConfig } from '@playwright/test';
import { origin, target } from './e2e/server';

export default defineConfig({
  testDir: './e2e',
  testMatch: '*.spec.ts',
  testIgnore: 'https.spec.ts',
  workers: 1,
  retries: 0,
  timeout: 30_000,
  globalTimeout: 180_000,
  globalSetup: './e2e/server.ts',
  use: {
    baseURL: origin,
    browserName: 'chromium',
    channel: process.env.REMOTA_BROWSER_CHANNEL,
    trace: 'off',
    viewport: { width: 1440, height: 960 },
  },
  webServer: {
    command: 'npm run dev -- --port 4173',
    url: origin,
    reuseExistingServer: false,
    env: { REMOTA_API_TARGET: target },
  },
});
