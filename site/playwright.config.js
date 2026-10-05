import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './tests', timeout: 30000, retries: 0, workers: 2,
  reporter: [['list']],
  use: { baseURL: process.env.SITE_TEST_URL || 'http://127.0.0.1:8765/', screenshot: 'only-on-failure', trace: 'retain-on-failure' },
  projects: process.env.CI ? [{name: 'chromium', use: {browserName: 'chromium'}}, {name: 'webkit', use: {browserName: 'webkit'}}]
    : [{name: 'chrome', use: {browserName: 'chromium', channel: 'chrome'}}],
  webServer: process.env.SITE_TEST_URL ? undefined : {
    command: 'python3 -m http.server 8765 --bind 127.0.0.1 --directory dist',
    url: 'http://127.0.0.1:8765/', reuseExistingServer: false, timeout: 10000
  }
});
