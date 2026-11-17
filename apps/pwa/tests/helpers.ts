import type { APIRequestContext, Page } from "@playwright/test";

export type Fake = {
  reset: () => Promise<void>;
  set: (values: Record<string, unknown>) => Promise<void>;
  factories: (factories: unknown[]) => Promise<void>;
  node: (name: string, values: Record<string, unknown>) => Promise<void>;
  state: () => Promise<{
    actions: Record<string, unknown>[];
    previews: Record<string, unknown>[];
    threads: { id: string; status: string; messages: { text: string }[] }[];
    enrollments: {
      route: string;
      host: string;
      origin: string;
      replacePushKey?: boolean;
    }[];
    peers: Record<
      string,
      {
        actions: Record<string, unknown>[];
        takeovers: { operation: string; confirm?: boolean }[];
      }
    >;
  }>;
};

export const HOME = "127.0.0.1:4324";
export const peer = (short: string) => `${short}.localhost:4324`;

export function roster(...peers: string[]) {
  const member = (id: string, name: string) => ({
    id,
    name,
    owner: "afonso@example.com",
    tailnet: "localhost",
    pending: 0,
  });
  return {
    id: "roster-1",
    home: `http://${HOME}`,
    self: "node-1",
    members: [
      { ...member("node-1", HOME), self: true },
      ...peers.map((short, i) => member(`node-${i + 2}`, peer(short))),
    ],
  };
}

export function fake(request: APIRequestContext): Fake {
  const post = async (path: string, data: unknown = {}) => {
    const response = await request.post(path, { data });
    return response.json();
  };
  return {
    reset: async () => void (await post("/__test/reset")),
    set: async (values) => void (await post("/__test/state", values)),
    factories: async (factories) =>
      void (await post("/__test/state", { snapshotFactories: factories })),
    node: async (name, values) =>
      void (await post("/__test/node", { name, ...values })),
    state: async () => (await request.post("/__test/actions")).json(),
  };
}

export async function controlled(page: Page) {
  await page.evaluate(() => navigator.serviceWorker.ready);
  if (!(await page.evaluate(() => Boolean(navigator.serviceWorker.controller))))
    await page.reload();
  await page.waitForFunction(() => Boolean(navigator.serviceWorker.controller));
}

const minutesAgo = (minutes: number) =>
  new Date(Date.now() - minutes * 60_000).toISOString();

export function question(key = "k-question") {
  return {
    id: "q-csv-1",
    key,
    kind: "question",
    title: "two questions before the plan",
    since: minutesAgo(3),
    questions: [
      {
        text: "Which delimiter?",
        options: ["Comma", "Semicolon"],
        recommended: "Comma",
      },
      { text: "Include headers?", options: ["Yes", "No"] },
    ],
    send: {
      ready: true,
      reason: "Sends 1 message to csv-export, which is idle at its prompt.",
    },
  };
}

export function gate(key = "k-gate") {
  return {
    id: "gate-plan",
    key,
    kind: "gate",
    title: "plan approval",
    stage: "plan",
    since: minutesAgo(9),
    send: {
      ready: true,
      reason: "Sends 1 message to invoice-pdf, which is idle at its prompt.",
    },
  };
}

export function handoff(key = "k-handoff") {
  return {
    id: "handoff-rate-limits",
    key,
    kind: "handoff",
    title: "ready for you to test",
    since: minutesAgo(2),
    send: {
      ready: true,
      reason:
        "No supervisor runs for rate-limits, so this wakes one to close it.",
    },
  };
}

export function factory(
  id: string,
  items: unknown[],
  overrides: Record<string, unknown> = {},
) {
  return {
    key: `/dev/shop:${id}`,
    route: `shop-1a2b-${id}`,
    incarnation: `${id}-1`,
    id,
    repo: "/dev/shop",
    repo_name: "shop",
    stage: "plan",
    status: items.length ? "needs you" : "running",
    paused: false,
    needs_since: items.length ? minutesAgo(10) : undefined,
    items,
    pages: [
      {
        name: "plan.html",
        title: "Plan",
        open_threads: 2,
        href: `/r/shop-1a2b-${id}/plan.html`,
      },
      {
        name: "prototype",
        title: "Prototype",
        unavailable:
          "It runs on a dev server, so it opens only on the computer.",
      },
    ],
    message: {
      ready: true,
      reason: `Sends 1 message to ${id}, which is idle at its prompt.`,
    },
    can_pause: true,
    can_wake: false,
    ...overrides,
  };
}
