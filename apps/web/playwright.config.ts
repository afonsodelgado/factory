import { defineConfig, devices } from "@playwright/test";

const origin = "http://127.0.0.1:4322";

export default defineConfig({
  testDir: "tests",
  testMatch: "*.spec.ts",
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: "list",
  use: { baseURL: origin },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command:
      "pnpm exec astro preview --host 127.0.0.1 --port 4322 --ignore-lock",
    url: `${origin}/`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
