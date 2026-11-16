import { expect, test } from "@playwright/test";
import { controlled, factory, fake, question } from "./helpers.ts";

test.beforeEach(async ({ request }) => {
  const server = fake(request);
  await server.reset();
});

test("the installed shell opens offline and never caches private routes", async ({
  page,
  context,
}) => {
  const server = fake(page.request);
  await server.pair();
  await server.factories([factory("csv-export", [question()])]);
  await page.goto("/");
  await controlled(page);
  await page.goto("/r/shop-1a2b-csv-export/plan.html");
  await page.goto("/");

  const cached = await page.evaluate(async () => {
    const urls: string[] = [];
    for (const name of await caches.keys()) {
      for (const request of await (await caches.open(name)).keys()) {
        urls.push(new URL(request.url).pathname);
      }
    }
    return urls;
  });
  expect(cached).toContain("/");
  expect(
    cached.filter((path) => /^\/(api|r|events|_review)\//.test(path)),
  ).toEqual([]);

  await context.setOffline(true);
  await page.reload();
  await expect(page.locator("#root")).not.toBeEmpty();
  const api = await page.evaluate(() =>
    fetch("/api/me").then(
      () => "answered",
      () => "failed",
    ),
  );
  expect(api).toBe("failed");
  await context.setOffline(false);
});

test("an older push never replaces the resolved notification", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["notifications"]);
  await page.goto("/");
  await controlled(page);
  const cdp = await context.newCDPSession(page);
  const registration = new Promise<string>((resolve) =>
    cdp.on("ServiceWorker.workerRegistrationUpdated", ({ registrations }) => {
      const found = registrations.find((r) => !r.isDeleted);
      if (found) resolve(found.registrationId);
    }),
  );
  await cdp.send("ServiceWorker.enable");
  const registrationId = await registration;
  const origin = new URL(page.url()).origin;
  const push = (payload: object) =>
    cdp.send("ServiceWorker.deliverPushMessage", {
      origin,
      registrationId,
      data: JSON.stringify(payload),
    });
  const base = { tag: "k1", title: "csv-export · question", url: "/n/k1" };
  await push({
    ...base,
    body: "Resolved elsewhere.",
    revision: 2,
    resolved: true,
  });
  await push({ ...base, body: "Needs you. Tap to open it.", revision: 1 });

  await expect
    .poll(() =>
      page.evaluate(async () =>
        (await navigator.serviceWorker.ready)
          .getNotifications({ tag: "k1" })
          .then((shown) => shown.map((n) => n.body)),
      ),
    )
    .toEqual(["Resolved elsewhere."]);
});
