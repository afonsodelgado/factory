import { expect, test } from "@playwright/test";

test.describe("without JavaScript", () => {
  test.use({ javaScriptEnabled: false });

  test("the production document renders without JavaScript", async ({
    page,
  }) => {
    const response = await page.goto("/");
    expect(response?.ok()).toBe(true);
    await expect(page.locator("h1")).toHaveText(
      "Manage your agents, like a factory. Stay in control when it matters.",
    );
  });
});
