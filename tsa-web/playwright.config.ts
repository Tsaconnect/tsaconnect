import { defineConfig, devices } from '@playwright/test';

// The API is mocked in the browser (page.route, e2e/fixtures.ts): see the plan's
// deviation note — real sign-in codes are only ever emailed.
// A port of its own, never reused: a dev server already running from another
// checkout must not be the one under test.
const PORT = 5180;

export default defineConfig({
  testDir: 'e2e',
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? 'list' : [['list'], ['html', { open: 'never' }]],
  use: { baseURL: `http://localhost:${PORT}`, trace: 'retain-on-failure' },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `npx vite --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
