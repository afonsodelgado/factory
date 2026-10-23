import { expect, test } from "@playwright/test";

test.describe("without JavaScript", () => {
  test.use({ javaScriptEnabled: false });

  test("the production document renders without JavaScript", async ({
    page,
  }) => {
    const response = await page.goto("/");
    expect(response?.ok()).toBe(true);

    await expect(page.locator("h1")).toHaveCount(1);
    await expect(page.locator("h1")).toHaveText(
      "Manage your agents, like a factory. Stay in control when it matters.",
    );
    const ids = await page
      .locator("main > [id]")
      .evaluateAll((els) => els.map((el) => el.id));
    expect(ids).toEqual([
      "top",
      "what",
      "pipeline",
      "agents",
      "hub",
      "keys",
      "install",
    ]);
    await expect(page.locator("section h2")).toHaveText([
      "A factory is an id and a prompt.",
      "From a prompt to a merged stack.",
      "The right model for each job.",
      "One window. Every factory.",
      "Your hands stay on the keyboard.",
      "Install",
    ]);
    const heroCommand = await page
      .locator('[data-code="hero"]')
      .evaluate((code) => {
        const copy = code.cloneNode(true) as Element;
        copy.querySelectorAll(".pr").forEach((prompt) => prompt.remove());
        return copy.textContent;
      });
    expect(heroCommand).toBe(
      [
        "gh release download \\",
        "    --repo afonsodelgado/factory \\",
        "    --pattern 'factory_darwin_arm64' \\",
        "    --output ~/.local/bin/factory",
        "chmod +x ~/.local/bin/factory",
      ].join("\n"),
    );

    const frames = await page.locator("pre.term").evaluateAll((pres) =>
      pres.map((pre) => {
        const widths = [0];
        for (const node of Array.from(pre.childNodes)) {
          if (node instanceof Element && node.classList.contains("ic")) {
            widths[widths.length - 1]! += 1;
            continue;
          }
          const [first, ...rest] = node.textContent!.split("\n");
          widths[widths.length - 1]! += Array.from(first!).length;
          for (const line of rest) widths.push(Array.from(line).length);
        }
        return [widths.length, ...new Set(widths)];
      }),
    );
    expect(frames).toEqual([
      [22, 38],
      [14, 46],
      [28, 106],
      [28, 56],
    ]);

    await expect(page.locator("button.copy")).toHaveCount(7);
    for (const button of await page.locator("button.copy").all()) {
      await expect(button).toBeHidden();
    }
    await expect(page.locator(".plat")).toBeHidden();
    await expect(page.locator(".nojs-only")).toHaveText(
      "Other builds: factory_darwin_amd64, factory_linux_amd64, factory_linux_arm64.",
    );
    await expect(page.locator('.st[data-s="cur"] .key')).toHaveText("handoff");
    await expect(page.getByText("Design notes")).toHaveCount(0);
    await expect(page.locator("#static-toggle")).toHaveCount(0);

    await page.setViewportSize({ width: 360, height: 800 });
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBe(0);
  });
});
