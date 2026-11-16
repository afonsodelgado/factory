import { defineConfig, devices } from "@playwright/test";

const origin = "http://127.0.0.1:4324";

export default defineConfig({
  testDir: "tests",
  testMatch: "*.spec.ts",
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: "list",
  use: { baseURL: origin, serviceWorkers: "allow" },
  projects: [
    { name: "phone", use: { ...devices["Pixel 7"], channel: "chromium" } },
  ],
  webServer: {
    command: "node tests/fake-server.mjs 4324",
    url: `${origin}/__test/health`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
