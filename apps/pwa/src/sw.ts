const sw = self as unknown as ServiceWorkerGlobalScope;

type Payload = {
  tag: string;
  title: string;
  body: string;
  url: string;
  revision: number;
  resolved?: boolean;
};

const PRECACHE: string[] = "__PRECACHE__" as unknown as string[];
const VERSION: string = "__VERSION__";
const SHELL = `shell-${VERSION}`;
const REVISIONS = "revisions";
const PRIVATE = ["/api/", "/r/", "/events/", "/_review/"];

function isShellRoute(path: string): boolean {
  return (
    path === "/" ||
    path === "/setup" ||
    /^\/phone(\/(add(\/[^/]+)?|nodes\/[^/]+))?$/.test(path) ||
    /^\/f\/[^/]+(\/(pages|message|i\/[^/]+))?$/.test(path) ||
    /^\/n\/[^/]+\/[^/]+$/.test(path)
  );
}

sw.addEventListener("install", (event) => {
  event.waitUntil(caches.open(SHELL).then((cache) => cache.addAll(PRECACHE)));
});

sw.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((key) => key.startsWith("shell-") && key !== SHELL)
            .map((key) => caches.delete(key)),
        ),
      )
      .then(() => sw.clients.claim()),
  );
});

sw.addEventListener("message", (event) => {
  if (event.data === "activate") sw.skipWaiting();
});

sw.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  const ours = url.origin === sw.location.origin;
  if (event.request.method !== "GET" || !ours) return;
  if (PRIVATE.some((prefix) => url.pathname.startsWith(prefix))) return;
  if (event.request.mode === "navigate") {
    if (!isShellRoute(url.pathname)) return;
    event.respondWith(fromShell("/", event.request));
    return;
  }
  if (PRECACHE.includes(url.pathname)) {
    event.respondWith(fromShell(url.pathname, event.request));
  }
});

async function fromShell(path: string, request: Request): Promise<Response> {
  const cached = await caches.match(path, { cacheName: SHELL });
  return cached ?? fetch(request);
}

sw.addEventListener("push", (event) => {
  const payload = event.data?.json() as Payload | undefined;
  if (!payload) return;
  shown = shown.then(() => show(payload));
  event.waitUntil(shown);
});

let shown = Promise.resolve();

async function show(payload: Payload): Promise<void> {
  const latest = await newest(payload);
  await sw.registration.showNotification(latest.title, {
    body: latest.body,
    tag: latest.tag,
    data: latest,
    icon: "/icons/icon-192.png",
    badge: "/icons/icon-192.png",
  });
}

async function newest(payload: Payload): Promise<Payload> {
  const cache = await caches.open(REVISIONS);
  const key = `/__revision/${encodeURIComponent(payload.tag)}`;
  const saved = await cache.match(key);
  const known = saved ? ((await saved.json()) as Payload) : undefined;
  if (known && known.revision > payload.revision) return known;
  await cache.put(key, Response.json(payload));
  return payload;
}

sw.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data as Payload | undefined;
  event.waitUntil(open(destination(data?.url)));
});

function destination(url: string | undefined): string {
  if (!url) return "/";
  const target = new URL(url, sw.location.origin);
  const safe =
    target.origin === sw.location.origin && isShellRoute(target.pathname);
  return safe ? target.pathname : "/";
}

async function open(path: string): Promise<void> {
  const windows = await sw.clients.matchAll({
    type: "window",
    includeUncontrolled: true,
  });
  const app = windows.find(
    (client) => new URL(client.url).origin === sw.location.origin,
  );
  if (app) {
    await app.focus();
    app.postMessage({ type: "open", path });
    return;
  }
  await sw.clients.openWindow(path);
}
