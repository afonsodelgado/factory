import type {
  ActionBody,
  LocalFactory,
  Link,
  Me,
  PushState,
  Receipt,
  Refusal,
} from "./types.ts";

export type Reach =
  "online" | "offline" | "node-down" | "restarting" | "refused";

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

export function reachOf(error: unknown): Reach {
  if (refusalOf(error)) return "refused";
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

async function call<T>(method: string, path: string, body?: unknown) {
  let response: Response;
  try {
    response = await fetch(path, {
      method,
      cache: "no-store",
      credentials: "same-origin",
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
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

export const api = {
  me: () => call<Me>("GET", "/api/me"),
  preview: (factory: LocalFactory, body: ActionBody) =>
    call<{ text: string }>("POST", factoryPath(factory, "preview"), {
      ...body,
      incarnation: factory.incarnation,
    }),
  receipt: (operation: string) =>
    call<Receipt>("GET", `/api/operations/${operation}`),
  link: (key: string) =>
    call<Link>("GET", `/api/links/${encodeURIComponent(key)}`),
  push: (installation: string) =>
    call<PushState>(
      "GET",
      `/api/push/subscription?installation=${encodeURIComponent(installation)}`,
    ),
  subscribe: (subscription: PushSubscriptionJSON, installation: string) =>
    call<PushState>("POST", "/api/push/subscription", {
      ...subscription,
      installation,
    }),
  unsubscribe: (endpoint: string) =>
    call<PushState>("DELETE", "/api/push/subscription", { endpoint }),
};

export async function act(
  factory: LocalFactory,
  operation: string,
  body: ActionBody,
): Promise<Receipt> {
  try {
    return await call<Receipt>("POST", factoryPath(factory, "actions"), {
      ...body,
      operation,
      incarnation: factory.incarnation,
    });
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
