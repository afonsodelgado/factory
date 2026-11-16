import { devices, expect, test, type Page } from "@playwright/test";
import { fake } from "./helpers.ts";

const key = "shop-1a2b-csv-export";
const plan = `/r/${key}/plan.html`;
const overlay = (page: Page) => page.locator("factory-review");
const bar = (page: Page) => overlay(page).locator(".bar");

test.beforeEach(async ({ request }) => {
  const server = fake(request);
  await server.reset();
  await server.set({ gate: "gate-plan" });
});

test("a phone comments, sends and approves on a plan page", async ({
  page,
}) => {
  const server = fake(page.request);
  await page.goto(plan);
  await expect(overlay(page).locator(".top")).toContainText(
    `${key} / plan.html`,
  );

  await bar(page).getByRole("button", { name: "Comment" }).tap();
  await page.locator("#link").tap();
  expect(page.url()).toContain(plan);
  await expect(overlay(page).locator(".highlight-label")).toContainText("a ·");
  await bar(page).getByRole("button", { name: "↑ Parent" }).tap();
  await expect(overlay(page).locator(".highlight-label")).toContainText("li ·");
  await bar(page).getByRole("button", { name: "Comment on this" }).tap();
  await overlay(page).locator(".composer textarea").fill("Split this risk.");
  await overlay(page).getByRole("button", { name: "Save" }).tap();
  await expect(bar(page)).toContainText("Send 1");

  await bar(page).locator('[data-action="panel"]').tap();
  const panel = overlay(page).locator(".panel");
  await panel.getByRole("button", { name: "Comment on the whole page" }).tap();
  await overlay(page).locator(".composer textarea").fill("Too long.");
  await overlay(page).getByRole("button", { name: "Save" }).tap();
  await expect(bar(page)).toContainText("Send 2");
  await panel
    .locator(".card")
    .nth(1)
    .getByRole("button", { name: "Edit" })
    .tap();
  await overlay(page).locator(".composer textarea").fill("Far too long.");
  await overlay(page).getByRole("button", { name: "Save" }).tap();
  await panel
    .locator(".card")
    .nth(0)
    .getByRole("button", { name: "Delete" })
    .tap();
  await expect(bar(page)).toContainText("Send 1");

  await bar(page).getByRole("button", { name: "Send 1" }).tap();
  await expect(overlay(page).locator(".toast")).toContainText(
    "Sent 1 comment to the architect.",
  );
  await panel.getByRole("button", { name: "Reply" }).tap();
  await panel.locator(".reply textarea").fill("Shorter, please.");
  await panel.locator(".reply").getByRole("button", { name: "Reply" }).tap();
  await expect
    .poll(async () => (await server.state()).threads[0]?.messages.length)
    .toBe(2);

  await bar(page).locator('[data-action="panel"]').tap();
  await bar(page).locator('[data-action="approve"]').tap();
  await expect(bar(page)).toContainText("Tap again to approve");
  await expect(bar(page)).toContainText("still open on this page");
  await bar(page).locator('[data-action="approve"]').tap();
  await expect(overlay(page).locator(".toast")).toContainText(
    "Approval sent to the supervisor.",
  );
  const state = await server.state();
  expect(state.actions).toContainEqual({
    action: "review-approve",
    gate: "gate-plan",
  });
  expect(state.threads.map((t) => t.messages.at(-1)?.text)).toEqual([
    "Shorter, please.",
  ]);
});

test("a new page version waits until the phone's comment is saved", async ({
  page,
}) => {
  const server = fake(page.request);
  await page.goto(plan);
  await bar(page).getByRole("button", { name: "Comment" }).tap();
  await page.locator("#fonts").tap();
  await bar(page).getByRole("button", { name: "Comment on this" }).tap();
  await overlay(page).locator(".composer textarea").fill("Half a thought");
  await page.request.post("/__test/change", {
    data: { page: `${key}/plan.html` },
  });
  await expect(bar(page)).toContainText("New version waiting");
  await expect(overlay(page).locator(".composer textarea")).toHaveValue(
    "Half a thought",
  );
  const reloaded = page.waitForEvent("load");
  await overlay(page).getByRole("button", { name: "Save" }).tap();
  await reloaded;
  expect((await server.state()).threads).toHaveLength(1);
});

test("the comment panel stays open when the sandboxed page reloads", async ({
  page,
}) => {
  await page.goto(plan);
  await bar(page).locator('[data-action="panel"]').tap();
  await expect(overlay(page).locator(".panel")).toBeVisible();
  const reloaded = page.waitForEvent("load");
  await page.request.post("/__test/change", {
    data: { page: `${key}/plan.html` },
  });
  await reloaded;
  await expect(overlay(page).locator(".panel")).toBeVisible();
});

test("the desktop overlay keeps hover picking at a wide window", async ({
  browser,
}) => {
  const context = await browser.newContext({
    ...devices["Desktop Chrome"],
    baseURL: "http://127.0.0.1:4324",
  });
  const page = await context.newPage();
  await page.goto(plan);
  await expect(bar(page)).toContainText("Comment");
  await bar(page)
    .getByRole("button", { name: /Comment/ })
    .click();
  await page.locator("#fonts").click();
  await expect(overlay(page).locator(".composer textarea")).toBeFocused();
  await expect(bar(page).getByRole("button", { name: "↑ Parent" })).toHaveCount(
    0,
  );
  await context.close();
});
