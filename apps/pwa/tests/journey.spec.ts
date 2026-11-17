import { expect, test } from "@playwright/test";
import { controlled, factory, fake, gate, question } from "./helpers.ts";

test.beforeEach(async ({ request }) => {
  await fake(request).reset();
});

test("the owner answers, approves and messages, goes read-only offline, then another Tailscale account sees only the refusal", async ({
  page,
  context,
}) => {
  const server = fake(page.request);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await server.factories([
    factory("csv-export", [question()]),
    factory("invoice-pdf", [gate()], {
      needs_since: new Date(Date.now() - 30 * 60_000).toISOString(),
    }),
    factory("search-typos", []),
  ]);

  await page.goto("/");

  const rows = page
    .getByRole("navigation", { name: "Factories" })
    .getByRole("link")
    .filter({ hasText: /invoice-pdf|csv-export|search-typos/ });
  await expect(rows.first()).toContainText("invoice-pdf");
  await expect(rows.nth(1)).toContainText("csv-export");

  await rows.nth(1).click();
  await page.getByRole("link", { name: /two questions/ }).click();
  await page.getByRole("button", { name: "Comma recommended" }).click();
  await page.getByRole("button", { name: "No", exact: true }).click();
  await expect(page.getByText("preview: answer Comma | No")).toBeVisible();
  await page.getByRole("button", { name: "Send answers" }).click();
  await expect(page.getByText("Delivered.")).toBeVisible();
  await page.getByRole("link", { name: "Next: invoice-pdf needs you" }).click();

  await expect(
    page.getByRole("heading", { name: "plan approval" }),
  ).toBeVisible();
  await page.getByLabel("Notes sent with the approval").fill("ship it");
  await page.getByRole("button", { name: "Approve plan" }).click();
  await expect(page.getByText("Open threads")).toBeVisible();
  await page.getByRole("button", { name: "Tap again to approve" }).click();
  await expect(page.getByText("Delivered.")).toBeVisible();

  await page.goto("/f/shop-1a2b-search-typos/message");
  await page
    .getByLabel("Message to the search-typos supervisor")
    .fill("/factory status");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Sent from this phone" }),
  ).toBeVisible();

  const actions = (await server.state()).actions;
  expect(actions.map((a) => [a.action, a.answers ?? a.text])).toEqual([
    ["answer", ["Comma", "No"]],
    ["approve", "ship it"],
    ["message", "/factory status"],
  ]);

  await context.setOffline(true);
  await page.goto("/").catch(() => undefined);
  await page.reload().catch(() => undefined);
  await expect(page.getByText(/Can.t reach/)).toBeVisible({ timeout: 20_000 });
  await expect(rows.filter({ hasText: "csv-export" })).toBeVisible();
  await context.setOffline(false);

  await controlled(page);
  await context.setExtraHTTPHeaders({
    "Tailscale-User-Login": "sam@example.com",
  });
  await page.reload();
  await expect(
    page.getByRole("heading", { name: /answers afonso@example\.com only/ }),
  ).toBeVisible();
  await expect(
    page.getByText(/sam@example\.com.*shown or kept here/),
  ).toBeVisible();
  await expect(page.getByText("csv-export")).toHaveCount(0);
  await expect
    .poll(() =>
      page.evaluate(async () =>
        (await indexedDB.databases()).map((d) => d.name),
      ),
    )
    .not.toContain("factory-phone");
  expect(errors).toEqual([]);
});

test("the shell has no sideways scroll at phone and desktop widths", async ({
  page,
}) => {
  const server = fake(page.request);
  await server.factories([factory("csv-export", [question()])]);
  for (const width of [320, 390, 900, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/f/shop-1a2b-csv-export");
    await expect(
      page.getByRole("navigation", { name: "csv-export" }),
    ).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
  }
});
