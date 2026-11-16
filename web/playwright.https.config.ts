import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testMatch: 'https.spec.ts',
  globalSetup: './e2e/https.ts',
  workers: 1,
  retries: 0,
  timeout: 120_000,
  globalTimeout: 360_000,
  use: { trace: 'off', ignoreHTTPSErrors: false, viewport: { width: 1440, height: 960 } },
});
