import { defineConfig, devices } from "@playwright/test";

/**
 * Browser end-to-end tests of the production build against the real API.
 * Run through `make test-e2e` (scripts/e2e.sh), which starts the API and the
 * worker on a throwaway database, seeds it and sets ADMIN_API_URL and
 * E2E_SEED; Playwright then serves the build with `next start`.
 */
const port = Number(process.env.E2E_DASHBOARD_PORT ?? 3100);
const baseURL = `http://127.0.0.1:${port}`;
const ci = Boolean(process.env.CI);

export default defineConfig({
  testDir: "e2e",
  outputDir: "test-results",
  // One worker: the specs share one seeded database and sign-in throttling
  // is per username.
  workers: 1,
  fullyParallel: false,
  forbidOnly: ci,
  retries: ci ? 1 : 0,
  reporter: "list",
  timeout: 30_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts$/ },
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        storageState: "e2e/.auth/admin.json",
      },
      dependencies: ["setup"],
    },
  ],
  webServer: {
    command: `pnpm exec next start -H 127.0.0.1 -p ${port}`,
    url: `${baseURL}/login`,
    reuseExistingServer: false,
    timeout: 60_000,
    stdout: "ignore",
    stderr: "pipe",
  },
});
