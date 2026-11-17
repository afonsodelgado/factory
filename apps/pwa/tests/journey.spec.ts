import { expect, test, type Page } from "@playwright/test";
import {
  controlled,
  factory,
  fake,
  gate,
  handoff,
  peer,
  question,
  roster,
} from "./helpers.ts";

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
  await server.set({ roster: roster() });
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
  await expect(
    page.getByRole("button", { name: /^This phone, connected to/ }),
  ).toBeVisible();
  await expect(page.locator(".nd")).toHaveCount(0);

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
    await fitsWidth(page, width);
  }
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 800 });
      await page.goto("/setup");
      await expect(
        page.getByRole("heading", { name: "Set up this phone" }),
      ).toBeVisible();
      await fitsWidth(page, width);
    }
  }

  const homelab = peer("homelab");
  await server.set({
    roster: roster("homelab", "build-box"),
    reports: {
      [homelab]: {
        target: { name: homelab, short: "homelab" },
        checks: [
          {
            name: "funnel",
            mark: "fail",
            detail: "Funnel is on, so anyone on the internet reaches its node",
            fix: "tailscale funnel --https=443 --set-path=/a/very/long/path/that/a/narrow/phone/must/wrap off",
            on: "homelab",
          },
        ],
        refusal: "Funnel is on, so anyone on the internet reaches its node",
        problems: 1,
      },
    },
  });
  await server.set({
    repos: { "https://example.com/afonso/cowork.git": "/dev/cowork" },
  });
  await server.node(homelab, {
    factories: [factory("rate-limits", [question()])],
    readiness: [
      {
        name: "claude",
        mark: "fail",
        detail: "claude isn't logged in",
        fix: "claude, then /login with a very long account name to wrap",
      },
      {
        name: "repos",
        mark: "warn",
        detail:
          "cowork not cloned yet; c clones it with this machine's login, or it clones when a factory first starts or moves here",
      },
    ],
  });
  await server.node(peer("build-box"), {
    mode: "offline",
    factories: [factory("perf-budget", [])],
  });
  const screens: [string, RegExp][] = [
    ["/", /can't be reached/],
    ["/phone", /app home/],
    ["/phone/nodes/node-3", /Unreachable/],
    ["/phone/nodes/node-2", /Clone cowork now, with homelab/],
    [`/phone/add/${homelab}`, /nothing was changed on homelab/],
  ];
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 800 });
      for (const [path, shows] of screens) {
        await page.goto(path);
        await expect(page.getByText(shows).first()).toBeVisible({
          timeout: 10_000,
        });
        await fitsWidth(page, width);
      }
    }
  }
});

test("with Funnel on, the installed app names each fix, shows no factory data and recovers once it is off", async ({
  page,
}) => {
  const server = fake(page.request);
  await server.factories([factory("csv-export", [question()])]);
  await page.goto("/");
  const row = page
    .getByRole("navigation", { name: "Factories" })
    .getByRole("link")
    .filter({ hasText: "csv-export" });
  await expect(row).toBeVisible();
  await controlled(page);

  const fix =
    "tailscale funnel --https=443 --set-path=/a/very/long/path/that/a/narrow/phone/must/wrap off";
  await server.set({ mode: "funnel", fixes: [fix] });
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 800 });
      await page.reload();
      await expect(
        page.getByRole("heading", { name: /Funnel is on for/ }),
      ).toBeVisible();
      await expect(page.getByText(fix)).toBeVisible();
      await expect(page.getByText("csv-export")).toHaveCount(0);
      await expect(page.getByText(/answers .* only/)).toHaveCount(0);
      await fitsWidth(page, width);
    }
  }

  await server.set({ mode: "up" });
  await page.evaluate(() => dispatchEvent(new Event("online")));
  await expect(row).toBeVisible();
});

test("with three nodes the list groups by node, counts a lost one apart, keeps acting on another node while home is down and forgets a removed node", async ({
  page,
}) => {
  const server = fake(page.request);
  await server.set({ roster: roster("homelab", "build-box") });
  await server.factories([factory("csv-export", [question()])]);
  await server.node(peer("homelab"), {
    factories: [
      factory("rate-limits", [], {
        walkthrough: {
          state: "ready",
          at: new Date().toISOString(),
          origin: "https://homelab.tail5e2c.ts.net:8443",
          href: "/w/shop-1a2b-rate-limits",
        },
      }),
    ],
  });
  await server.node(peer("build-box"), {
    factories: [factory("perf-budget", [gate()])],
  });
  await page.goto("/");
  const list = page.getByRole("navigation", { name: "Factories" });
  await expect(
    page.getByRole("button", { name: "Nodes: 3 of 3 reachable" }),
  ).toBeVisible();
  await expect(
    list.getByRole("heading", { name: /shop ↔ homelab/ }),
  ).toBeVisible();
  await controlled(page);

  await server.node(peer("build-box"), { mode: "offline" });
  await expect(list.getByText("↮ 1 unreachable")).toBeVisible({
    timeout: 10_000,
  });
  await expect(list.getByText("◆ 1 need you")).toBeVisible();
  await expect(
    list.getByRole("heading", { name: /shop ↮ build-box · lost/ }),
  ).toBeVisible();
  await expect(
    list.getByRole("link").filter({ hasText: "perf-budget" }),
  ).toContainText("unreachable");
  await expect(page.getByText("build-box can't be reached")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Nodes: 2 of 3 reachable" }),
  ).toBeVisible();

  await server.set({ mode: "offline" });
  await page.reload();
  await expect(page.getByText("127 can't be reached")).toBeVisible({
    timeout: 10_000,
  });
  await list.getByRole("link").filter({ hasText: "rate-limits" }).click();
  await page.getByRole("link", { name: /Pages/ }).click();
  await expect(page.getByRole("link", { name: /Walkthrough/ })).toHaveAttribute(
    "href",
    `http://${peer("homelab")}/w/shop-1a2b-rate-limits`,
  );
  await page.getByRole("link", { name: /Message/ }).click();
  await page
    .getByLabel("Message to the rate-limits supervisor")
    .fill("/factory status");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Sent from this phone" }),
  ).toBeVisible();
  const reached = (await server.state()).peers[peer("homelab")].actions;
  expect(reached.map((a) => [a.action, a.text])).toEqual([
    ["message", "/factory status"],
  ]);

  await server.set({ mode: "up", roster: roster("homelab") });
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "Nodes: 2 of 2 reachable" }),
  ).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText("perf-budget")).toHaveCount(0);
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          new Promise<string[]>((resolve) => {
            const open = indexedDB.open("factory-phone");
            open.onsuccess = () => {
              const keys = open.result
                .transaction("records")
                .objectStore("records")
                .getAllKeys();
              keys.onsuccess = () => resolve(keys.result.map(String));
            };
          }),
      ),
    )
    .not.toContainEqual(expect.stringContaining("node-3/"));
});

test("a phone adds a node only through its home origin, switches its push key only when chosen, and a refused machine offers no Add", async ({
  page,
}) => {
  const server = fake(page.request);
  const studio = peer("studio");
  const homelab = peer("homelab");
  const nas = peer("nas");
  const ownKey = {
    name: "push",
    mark: "warn",
    detail: "it has its own push key, and 1 phone subscribed to it",
  };
  await server.set({
    roster: roster(),
    candidates: [
      {
        name: studio,
        short: "studio",
        os: "macOS",
        state: "node",
        detail: "factory 0.9.4",
        selectable: true,
        hello: { version: "0.9.4", os: "macOS" },
      },
      {
        name: nas,
        short: "nas",
        os: "linux",
        state: "no-node",
        detail: `no node answers at https://${nas}`,
        fix: "factory node install",
        on: "nas",
        selectable: false,
      },
    ],
    reports: {
      [studio]: {
        target: { name: studio, short: "studio" },
        checks: [
          { name: "node", mark: "ok", detail: "0.9.4 · macOS" },
          {
            name: "claude",
            mark: "fail",
            detail: "claude isn't logged in",
            fix: "claude, then /login",
            on: "studio",
          },
          ownKey,
        ],
        problems: 1,
        push_conflict: true,
      },
      [homelab]: {
        target: { name: homelab, short: "homelab" },
        checks: [ownKey],
        problems: 0,
        push_conflict: true,
      },
      [nas]: {
        target: { name: nas, short: "nas" },
        checks: [
          {
            name: "serve",
            mark: "fail",
            detail: `no node answers at https://${nas}`,
            fix: "factory node install",
            on: "nas",
          },
        ],
        refusal: `no node answers at https://${nas}`,
        problems: 1,
      },
    },
  });
  await page.goto("/phone");
  await page.getByRole("button", { name: "+ Add a node" }).click();
  const machines = page.getByRole("group", { name: "Machines" });
  await expect(machines.getByRole("button", { name: /nas/ })).toBeDisabled();
  await machines.getByRole("button", { name: /studio/ }).click();
  await page.getByRole("button", { name: "Check studio" }).click();
  await expect(page.getByText("Added either way.")).toBeVisible();
  await page.getByRole("button", { name: /^Switch studio to/ }).click();
  await page.getByRole("button", { name: "Switch key and add studio" }).click();
  await expect(page.getByRole("heading", { name: "This phone" })).toBeVisible();
  await expect(page.locator(".nodes").getByText("studio")).toBeVisible();
  await page.getByRole("button", { name: "Back" }).click();
  await expect(page.getByText("studio joined your nodes.")).toBeVisible();

  await page.goto("/phone/add");
  await page.getByLabel("Tailnet name").fill(homelab);
  await page.getByRole("button", { name: /^Check/ }).click();
  await page.getByRole("button", { name: "Add homelab" }).click();
  await expect(
    page.getByText(/homelab is added, but it keeps its own push key/),
  ).toBeVisible();
  await page.getByRole("button", { name: "Done" }).click();
  await expect(page.getByRole("heading", { name: "This phone" })).toBeVisible();

  await page.goto("/phone/add");
  await page.getByLabel("Tailnet name").fill(nas);
  await page.getByRole("button", { name: /^Check/ }).click();
  await expect(page.getByText("nothing was changed on nas")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Add/ })).toHaveCount(0);

  const enrollments = (await server.state()).enrollments;
  expect(enrollments.map((e) => [e.route, e.replacePushKey])).toEqual([
    ["/api/nodes/check", undefined],
    ["/api/nodes", true],
    ["/api/nodes/check", undefined],
    ["/api/nodes", undefined],
    ["/api/nodes/check", undefined],
  ]);
  for (const e of enrollments) {
    expect(e.host).toBe("127.0.0.1:4324");
    expect(e.origin).toBe("http://127.0.0.1:4324");
  }
});

test("a done factory's handoff on another node waits in Needs you, and its notification link opens it and sends Tested to that node while home is down", async ({
  page,
}) => {
  const server = fake(page.request);
  const homelab = peer("homelab");
  await server.set({ roster: roster("homelab") });
  await server.node(homelab, {
    factories: [
      factory("rate-limits", [handoff()], {
        status: "done",
        needs_since: undefined,
      }),
    ],
  });
  await page.goto("/");
  const list = page.getByRole("navigation", { name: "Factories" });
  await expect(list.getByText("◆ 1 need you")).toBeVisible();
  await expect(
    list.getByRole("link").filter({ hasText: "rate-limits" }),
  ).toContainText("handoff");
  await controlled(page);

  await server.set({ mode: "offline" });
  await page.goto("/n/node-2/k-handoff");
  await expect(
    page.getByRole("heading", { name: "ready for you to test" }),
  ).toBeVisible({ timeout: 10_000 });
  await page.getByRole("button", { name: "Tested" }).click();
  await expect(page.getByText("Delivered.")).toBeVisible();
  const reached = (await server.state()).peers[homelab].actions;
  expect(reached.map((a) => [a.action, a.item])).toEqual([
    ["handled", "handoff-rate-limits"],
  ]);
});

test("a factory moves to another node through its checkpoint and release, can't be cancelled once released, and its row moves under that node", async ({
  page,
}) => {
  const server = fake(page.request);
  await server.set({ roster: roster("homelab") });
  await server.factories([factory("csv-export", [])]);
  await server.node(peer("homelab"), { factories: [] });
  await page.goto("/f/shop-1a2b-csv-export");
  await page.getByRole("button", { name: "Move to…" }).click();
  const sheet = page.getByRole("dialog", { name: "Move csv-export" });
  await sheet.getByRole("button", { name: /homelab\s*ready/ }).click();
  await sheet.getByRole("button", { name: "Move to homelab" }).click();

  const move = page.getByRole("region", { name: "Moving csv-export" });
  await expect(
    move.getByText("Waiting for csv-export to reach a checkpoint"),
  ).toBeVisible();
  await expect(
    move.getByText("Waiting for the supervisor to finish its stage"),
  ).toBeVisible();
  await expect(
    move.getByRole("button", { name: "Cancel the move" }),
  ).toBeVisible();
  await expect(move.getByText("csv-export is stopped on 127")).toBeVisible();
  await expect(
    move.getByRole("button", { name: "Cancel the move" }),
  ).toHaveCount(0);
  await expect(move.getByText("csv-export runs on homelab now")).toBeVisible({
    timeout: 10_000,
  });
  await expect(
    move.getByRole("link", { name: /Open csv-export on homelab/ }),
  ).toBeVisible();

  await page.getByRole("button", { name: "Back" }).click();
  const list = page.getByRole("navigation", { name: "Factories" });
  const homelab = list
    .locator("section")
    .filter({ has: page.getByRole("heading", { name: /shop ↔ homelab/ }) });
  await expect(homelab.getByRole("link", { name: /csv-export/ })).toBeVisible();
  await expect(list.getByRole("link", { name: /csv-export/ })).toHaveCount(1);
});

test("taking over a lost node's factory shows the server's warning and confirms only after the explicit tap", async ({
  page,
}) => {
  const server = fake(page.request);
  const homelab = peer("homelab");
  await server.set({ roster: roster("homelab", "build-box") });
  await server.node(homelab, { factories: [] });
  await server.node(peer("build-box"), {
    factories: [factory("perf-budget", [gate()])],
  });
  await page.goto("/");
  const list = page.getByRole("navigation", { name: "Factories" });
  const row = list.getByRole("link").filter({ hasText: "perf-budget" });
  await expect(row).toBeVisible();
  await controlled(page);
  await server.node(peer("build-box"), { mode: "offline" });
  await expect(row).toContainText("unreachable", { timeout: 10_000 });
  await row.click();

  await page.getByRole("button", { name: "Take over…" }).click();
  const sheet = page.getByRole("dialog", { name: "Take perf-budget over" });
  await expect(sheet.getByRole("button", { name: /build-box/ })).toHaveCount(0);
  await sheet.getByRole("button", { name: /homelab\s*ready/ }).click();
  await sheet.getByRole("button", { name: "Take over on homelab" }).click();
  const warning = page.getByRole("dialog", {
    name: "Take perf-budget over on homelab?",
  });
  await expect(
    warning.getByText(
      "build-box may keep running perf-budget until it reconnects, and work done after that checkpoint may be lost or diverge.",
      { exact: false },
    ),
  ).toBeVisible();
  const asked = (await server.state()).peers[homelab].takeovers;
  expect(asked.map((t) => t.confirm ?? false)).toEqual([false]);

  await warning.getByRole("button", { name: "Take over anyway" }).click();
  await expect(
    page
      .getByRole("region", { name: "Moving perf-budget" })
      .getByText("perf-budget runs on homelab now"),
  ).toBeVisible({ timeout: 10_000 });
  const confirmed = (await server.state()).peers[homelab].takeovers;
  expect(confirmed.map((t) => [t.operation, t.confirm ?? false])).toEqual([
    [asked[0].operation, false],
    [asked[0].operation, true],
  ]);
});

test("move all reports which factories moved and which stayed, and why", async ({
  page,
}) => {
  const server = fake(page.request);
  await server.set({ roster: roster("homelab") });
  await server.factories([
    factory("csv-export", []),
    factory("invoice-pdf", [], {
      unmovable: "the answer to gate-plan is unknown and must settle first",
    }),
  ]);
  await server.node(peer("homelab"), { factories: [] });
  await page.goto("/phone/nodes/node-2");
  await page
    .getByRole("button", { name: "Move all from 127 to homelab…" })
    .click();
  await page
    .getByRole("dialog", { name: "Move all to homelab?" })
    .getByRole("button", { name: "Move all" })
    .click();
  const outcomes = page.getByRole("list", { name: "Moving to homelab" });
  await expect(
    outcomes
      .getByRole("listitem")
      .filter({ hasText: "invoice-pdf" })
      .getByText(
        "not moved: the answer to gate-plan is unknown and must settle first",
      ),
  ).toBeVisible();
  await expect(
    outcomes
      .getByRole("listitem")
      .filter({ hasText: "csv-export" })
      .getByText("moved to homelab", { exact: true }),
  ).toBeVisible({ timeout: 10_000 });
  await expect(
    page.getByRole("button", { name: /Move all from 127 to homelab/ }),
  ).toBeVisible();
});

async function fitsWidth(page: Page, width: number) {
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(width);
}

test("pending removal stays visible until the offline node acknowledges it", async ({
  page,
}) => {
  const server = fake(page.request);
  const members = roster("homelab");
  const leaving = members.members[1];
  await server.set({ roster: { ...roster(), leaving: [leaving] } });
  await page.goto("/phone");
  await expect(
    page.getByText("homelab: removal pending", { exact: false }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText("homelab: removal pending", { exact: false }),
  ).toBeVisible();
  await server.set({ roster: roster() });
  await expect(
    page.getByText("homelab: removal pending", { exact: false }),
  ).toHaveCount(0);
});
