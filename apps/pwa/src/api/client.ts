import type {
  ActionBody,
  Added,
  BatchProgress,
  Candidate,
  CheckReport,
  LocalFactory,
  Link,
  Exposure,
  Me,
  Progress,
  PushState,
  Readiness,
  Receipt,
  Refusal,
  Roster,
  Snapshot,
  Unconfirmed,
} from "./types.ts";

const DEADLINE = 10_000;
const RETRY = 15_000;
const RECONNECT = 3_000;

export type Reach =
  "online" | "offline" | "node-down" | "restarting" | "refused" | "funnel";

export class HttpError extends Error {
  readonly status: number;
  readonly body: unknown;
  constructor(status: number, message: string, body: unknown = undefined) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

export function refusalOf(error: unknown): Refusal | undefined {
  if (!(error instanceof HttpError)) return undefined;
  if (error.status === 401) return {};
  const body = error.body as Refusal | undefined;
  if (error.status === 403 && typeof body?.owner === "string")
    return { owner: body.owner, login: body.login };
  return undefined;
}

export function exposureOf(error: unknown): Exposure | undefined {
  if (!(error instanceof HttpError) || error.status !== 403) return undefined;
  const body = error.body as (Exposure & { funnel?: boolean }) | undefined;
  if (body?.funnel !== true) return undefined;
  return { error: body.error, fixes: body.fixes };
}

export function reachOf(error: unknown): Reach {
  if (refusalOf(error)) return "refused";
  if (exposureOf(error)) return "funnel";
  if (!(error instanceof HttpError)) return "offline";
  switch (error.status) {
    case 0:
      return "offline";
    case 502:
      return "node-down";
    case 503:
      return "restarting";
  }
  return "online";
}

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
  deadline?: number,
) {
  let response: Response;
  try {
    response = await fetch(path, {
      method,
      cache: "no-store",
      credentials: "same-origin",
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: deadline ? AbortSignal.timeout(deadline) : undefined,
    });
  } catch {
    throw new HttpError(0, "Can't reach the computer.");
  }
  const text = await response.text();
  const parsed = parse(text);
  if (!response.ok) {
    const message =
      typeof parsed === "object" && parsed && "error" in parsed
        ? String(parsed.error)
        : text.trim() || response.statusText;
    throw new HttpError(response.status, message, parsed);
  }
  return parsed as T;
}

function parse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

const factoryPath = (factory: LocalFactory, what: string) =>
  `/api/factories/${encodeURIComponent(factory.key)}/${what}`;

export const nodeApi = (origin: string) => ({
  me: () => call<Me>("GET", origin + "/api/me", undefined, DEADLINE),
  preview: (factory: LocalFactory, body: ActionBody) =>
    call<{ text: string }>("POST", origin + factoryPath(factory, "preview"), {
      ...body,
      incarnation: factory.incarnation,
    }),
  receipt: (operation: string) =>
    call<Receipt>("GET", `${origin}/api/operations/${operation}`),
  link: (key: string) =>
    call<Link>("GET", `${origin}/api/links/${encodeURIComponent(key)}`),
  readiness: (repos: string[]) =>
    call<Readiness>(
      "GET",
      `${origin}/api/readiness?${new URLSearchParams(repos.map((r) => ["repo", r]))}`,
    ),
  clone: (url: string) =>
    call<{ repo: string }>("POST", origin + "/api/clone", {
      operation: crypto.randomUUID(),
      url,
    }),
  push: (installation: string) =>
    call<PushState>(
      "GET",
      `${origin}/api/push/subscription?installation=${encodeURIComponent(installation)}`,
    ),
  move: (factory: string, target: string) =>
    call<Progress>("POST", origin + "/api/transfers", {
      operation: crypto.randomUUID(),
      factory,
      target,
    }),
  progress: (operation: string) =>
    call<Progress>("GET", `${origin}/api/transfers/${operation}`),
  cancel: (operation: string) =>
    call<Progress>("POST", `${origin}/api/transfers/${operation}/cancel`, {}),
  takeover: (operation: string, incarnation: string, confirm: boolean) =>
    call<Progress>("POST", origin + "/api/transfers/takeover", {
      operation,
      factory: incarnation,
      confirm: confirm || undefined,
    }),
  moveAll: (target: string) =>
    call<BatchProgress>("POST", origin + "/api/transfers/all", {
      operation: crypto.randomUUID(),
      target,
    }),
  batch: (operation: string) =>
    call<BatchProgress>("GET", `${origin}/api/transfers/all/${operation}`),
  cancelAll: (operation: string) =>
    call<BatchProgress>(
      "POST",
      `${origin}/api/transfers/all/${operation}/cancel`,
      {},
    ),
});

export function warningOf(error: unknown): Unconfirmed | undefined {
  if (!(error instanceof HttpError) || error.status !== 428) return undefined;
  return (error.body as { warning?: Unconfirmed } | undefined)?.warning;
}

export function progressOf(error: unknown): Progress | undefined {
  if (!(error instanceof HttpError) || error.status !== 409) return undefined;
  return (error.body as { progress?: Progress } | undefined)?.progress;
}

export const api = {
  ...nodeApi(""),
  roster: () => call<Roster>("GET", "/api/nodes"),
  repos: () => call<Record<string, string> | null>("GET", "/api/repos"),
  candidates: () =>
    call<{ candidates: Candidate[] | null; at: string }>(
      "GET",
      "/api/nodes/candidates",
    ),
  check: (name: string) =>
    call<CheckReport>("POST", "/api/nodes/check", { name }),
  add: (name: string, replacePushKey: boolean) =>
    call<Added>("POST", "/api/nodes", {
      operation: crypto.randomUUID(),
      name,
      replace_push_key: replacePushKey || undefined,
    }),
  remove: (id: string) =>
    call<Roster>("POST", `/api/nodes/${encodeURIComponent(id)}/remove`, {
      operation: crypto.randomUUID(),
    }),
  subscribe: (subscription: PushSubscriptionJSON, installation: string) =>
    call<PushState>("POST", "/api/push/subscription", {
      ...subscription,
      installation,
    }),
  unsubscribe: (endpoint: string) =>
    call<PushState>("DELETE", "/api/push/subscription", { endpoint }),
};

export async function act(
  origin: string,
  factory: LocalFactory,
  operation: string,
  body: ActionBody,
): Promise<Receipt> {
  try {
    return await call<Receipt>(
      "POST",
      origin + factoryPath(factory, "actions"),
      {
        ...body,
        operation,
        incarnation: factory.incarnation,
        generation: factory.generation,
      },
    );
  } catch (error) {
    if (
      error instanceof HttpError &&
      error.status === 409 &&
      isReceipt(error.body)
    ) {
      return Object.assign(
        { factory: factory.key, action: body.action, reason: error.message },
        error.body,
      );
    }
    throw error;
  }
}

function isReceipt(body: unknown): body is Receipt {
  return typeof body === "object" && body !== null && "status" in body;
}

export type Follow = {
  up: (me: Me) => Promise<string | undefined>;
  snapshot: (next: Snapshot) => void;
  restarting: () => void;
  down: (error: unknown) => Promise<boolean>;
};

export function follow(origin: string, on: Follow) {
  let stream: EventSource | undefined;
  let timer: number | undefined;
  let closed = false;
  const fail = async (error: unknown) => {
    if (closed) return;
    if ((await on.down(error)) && !closed)
      timer = window.setTimeout(start, RETRY);
  };
  const start = async () => {
    clearTimeout(timer);
    stream?.close();
    if (closed) return;
    let me: Me;
    try {
      me = await nodeApi(origin).me();
    } catch (error) {
      return fail(error);
    }
    const installation = closed ? undefined : await on.up(me);
    if (installation === undefined || closed) return;
    const events = new EventSource(
      `${origin}/api/events?installation=${encodeURIComponent(installation)}`,
    );
    stream = events;
    events.addEventListener("snapshot", (event) =>
      on.snapshot(JSON.parse((event as MessageEvent).data) as Snapshot),
    );
    events.addEventListener("unavailable", on.restarting);
    events.onerror = () => {
      events.close();
      nodeApi(origin)
        .me()
        .then(() => {
          if (closed) return;
          on.restarting();
          timer = window.setTimeout(start, RECONNECT);
        }, fail);
    };
  };
  start();
  return {
    retry: start,
    close: () => {
      closed = true;
      clearTimeout(timer);
      stream?.close();
    },
  };
}
