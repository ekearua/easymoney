import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  timeout: 150_000,
  expect: { timeout: 10_000 },
  reporter: 'list',
  workers: 1,
  use: {
    channel: 'msedge',
    headless: true,
    viewport: { width: 420, height: 760 },
  },
});