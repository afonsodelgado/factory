import { expect, test } from "@playwright/test";

const HERO_COMMAND = [
  "gh release download \\",
  "    --repo afonsodelgado/factory \\",
  "    --pattern 'factory_darwin_arm64' \\",
  "    --output ~/.local/bin/factory",
  "chmod +x ~/.local/bin/factory",
].join("\n");

const LINUX_ARM_COMMAND = [
  "gh release download --repo afonsodelgado/factory \\",
  "    --pattern 'factory_linux_arm64' \\",
  "    --output ~/.local/bin/factory",
  "chmod +x ~/.local/bin/factory",
].join("\n");

const SITE = new URL(process.env.SITE_URL || "http://localhost:4321").origin;

const SHARE_TITLE =
  "Manage your agents, like a factory. Stay in control when it matters.";

const DESCRIPTION =
  "Manage your agents, like a factory, and stay in control when it matters. Give the instructions and your factory takes it to a tested, ready-to-deploy feature, stopping only when it needs you.";

interface FakeClipboard {
  copied: string[];
  refuse: boolean;
}

declare global {
  interface Window {
    fakeClipboard: FakeClipboard;
  }
}

test.describe("without JavaScript", () => {
  test.use({ javaScriptEnabled: false });

  test("the production document renders without JavaScript", async ({
    page,
    request,
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
    const shownHero = await page
      .locator('[data-code="hero"]')
      .evaluate((code) => {
        const copy = code.cloneNode(true) as Element;
        copy.querySelectorAll(".pr").forEach((prompt) => prompt.remove());
        return copy.textContent;
      });
    expect(shownHero).toBe(HERO_COMMAND);

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

    const focusableFrames = () =>
      page.locator(".scroll").evaluateAll(
        (frames) =>
          frames.filter((frame) => {
            (frame as HTMLElement).focus();
            return document.activeElement === frame;
          }).length,
      );
    expect(await focusableFrames()).toBe(0);
    await page.setViewportSize({ width: 768, height: 800 });
    expect(await focusableFrames()).toBe(0);

    await expect(page).toHaveTitle("factory · manage your agents");
    await expect(page.locator('link[rel="canonical"]')).toHaveCount(0);
    const meta = await page
      .locator("meta[name], meta[property]")
      .evaluateAll((tags) =>
        Object.fromEntries(
          tags.map((tag) => [
            tag.getAttribute("name") ?? tag.getAttribute("property"),
            tag.getAttribute("content"),
          ]),
        ),
      );
    expect(meta).toMatchObject({
      description: DESCRIPTION,
      "og:type": "website",
      "og:title": SHARE_TITLE,
      "og:description": DESCRIPTION,
      "og:image": `${SITE}/og.png`,
      "og:image:width": "1200",
      "og:image:height": "630",
      "twitter:card": "summary_large_image",
      "twitter:title": SHARE_TITLE,
      "twitter:description": DESCRIPTION,
      "twitter:image": `${SITE}/og.png`,
    });
    expect(meta["og:image:alt"]).toBeTruthy();
    expect(meta["twitter:image:alt"]).toBe(meta["og:image:alt"]);

    const robots = await request.get("/robots.txt");
    expect(await robots.text()).toBe(
      `User-agent: *\nAllow: /\n\nSitemap: ${SITE}/sitemap-index.xml\n`,
    );
    const index = await (await request.get("/sitemap-index.xml")).text();
    expect(index.match(/<loc>[^<]*<\/loc>/g)).toEqual([
      `<loc>${SITE}/sitemap-0.xml</loc>`,
    ]);
    const urls = await (await request.get("/sitemap-0.xml")).text();
    expect(urls.match(/<loc>[^<]*<\/loc>/g)).toEqual([`<loc>${SITE}/</loc>`]);

    for (const file of [
      "/favicon.svg",
      "/fonts/jetbrains-mono-regular.woff2",
      "/fonts/jetbrains-mono-bold.woff2",
      "/fonts/OFL.txt",
      "/fonts/NOTICE.md",
    ]) {
      expect((await request.get(file)).status(), file).toBe(200);
    }
    const favicon = await (await request.get("/favicon.svg")).text();
    expect(favicon).not.toContain("<text");
    const og = await (await request.get("/og.png")).body();
    expect([og.readUInt32BE(16), og.readUInt32BE(20)]).toEqual([1200, 630]);
  });
});

test("interactive commands stay local and survive reduced motion", async ({
  page,
  baseURL,
}) => {
  const origin = new URL(baseURL!).origin;
  const foreign: string[] = [];
  const fonts: Record<string, number> = {};
  const problems: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).origin !== origin) foreign.push(request.url());
  });
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (path.endsWith(".woff2")) fonts[path] = response.status();
  });
  page.on("pageerror", (error) => problems.push(error.message));
  page.on("console", (message) => {
    if (["error", "warning"].includes(message.type())) {
      problems.push(message.text());
    }
  });
  await page.addInitScript(() => {
    const clipboard: FakeClipboard = { copied: [], refuse: false };
    window.fakeClipboard = clipboard;
    Object.defineProperty(navigator, "clipboard", {
      value: {
        writeText: (text: string) => {
          if (clipboard.refuse) return Promise.reject(new Error("denied"));
          clipboard.copied.push(text);
          return Promise.resolve();
        },
      },
    });
  });
  const copied = () => page.evaluate(() => window.fakeClipboard.copied);

  await page.goto("/");
  const hero = page.locator(".term-hero");
  const hub = page.locator(".hub-wide");
  const staticHero = await hero.innerHTML();
  const staticHub = await hub.innerHTML();

  await expect(page.locator('[data-code="hero"] .ghost')).toHaveCount(1);
  const heroCopy = page.getByRole("button", {
    name: "Copy the install commands",
  });
  await heroCopy.click();
  await expect.poll(copied).toEqual([HERO_COMMAND]);
  await expect(heroCopy).toHaveText("copied");
  await expect(page.locator("[data-live]")).toHaveText(
    "Copied to the clipboard.",
  );

  await page.locator(".hubframe .win").evaluate((win) => {
    const { top, height } = win.getBoundingClientRect();
    window.scrollBy(0, top + height * 0.1 - window.innerHeight);
  });
  await expect(page.locator(".steps li.on")).toHaveCount(0);
  await expect(hub).not.toContainText("yes, with a reset link");

  await page.getByRole("button", { name: "Linux, ARM64" }).click();
  await page
    .getByRole("button", { name: "Copy the download commands" })
    .click();
  await expect.poll(copied).toEqual([HERO_COMMAND, LINUX_ARM_COMMAND]);

  await page.evaluate(() => {
    window.fakeClipboard.refuse = true;
  });
  const go = page.getByRole("button", { name: "Copy the go install command" });
  await go.click();
  await expect(go).toHaveText("press ⌘C");
  await expect(page.locator("[data-live]")).toContainText("Command C");
  const selected = await page.evaluate(() => window.getSelection()?.toString());
  expect(selected).toContain(
    "go install github.com/afonsodelgado/factory/cmd/factory@latest",
  );

  await page.locator("footer").scrollIntoViewIfNeeded();
  await page.locator("#top").scrollIntoViewIfNeeded();
  await expect.poll(() => hero.innerHTML()).not.toBe(staticHero);

  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect.poll(() => hero.innerHTML()).toBe(staticHero);
  await expect.poll(() => hub.innerHTML()).toBe(staticHub);
  await expect(page.locator(".steps li.on")).toHaveAttribute("data-step", "3");
  await expect(page.locator("[data-cursor]")).toBeVisible();
  await expect(page.locator("[data-cursor]")).not.toHaveClass(/blink/);
  await expect(page.locator('.st[data-s="cur"] .key')).toHaveText("handoff");
  await expect(page.locator(".reveal:not(.in)")).toHaveCount(0);

  expect(foreign).toEqual([]);
  expect(fonts).toEqual({
    "/fonts/jetbrains-mono-regular.woff2": 200,
    "/fonts/jetbrains-mono-bold.woff2": 200,
  });
  expect(problems).toEqual([]);
});
