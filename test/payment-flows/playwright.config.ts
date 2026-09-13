import { defineConfig, expect } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  reporter: 'list',
  use: {
    headless: true,
    viewport: { width: 420, height: 720 },
    baseURL: process.env.BASE_URL || 'http://127.0.0.1:8080',
  },
});
