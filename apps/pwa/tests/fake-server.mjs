import { randomUUID } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const dist = path.join(here, "../dist");
const overlay = path.join(here, "../../../bundle/review/overlay.js");
const port = Number(process.argv[2] ?? 4324);
const shellRoute =
  /^\/(setup|phone|n\/[^/]+|f\/[^/]+(\/(pages|message|i\/[^/]+))?)?$/;
const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript",
  ".css": "text/css",
  ".json": "application/json",
  ".webmanifest": "application/manifest+json",
  ".png": "image/png",
  ".woff2": "font/woff2",
  ".txt": "text/plain",
  ".md": "text/plain",
};

const owner = "afonso@example.com";
let state;
const streams = new Set();
const reviewStreams = new Set();

function reset() {
  state = {
    mode: "up",
    snapshot: { revision: 1, server_time: now(), factories: [] },
    receipts: {},
    actions: [],
    previews: [],
    subscription: null,
    threads: [],
    next: 1,
    gate: "",
    grants: {},
  };
}

const now = () => new Date().toISOString();

function send(res, status, body, headers = {}) {
  res.writeHead(status, {
    "Content-Type": "application/json",
    "Cache-Control": "no-store",
    ...headers,
  });
  res.end(JSON.stringify(body));
}

function refused(req, res, url) {
  const login = req.headers["tailscale-user-login"] ?? owner;
  if (login === owner) return false;
  const page =
    req.method === "GET" &&
    !url.pathname.startsWith("/api/") &&
    !url.pathname.includes("/_api/");
  if (page) {
    res.writeHead(403, { "Content-Type": "text/html; charset=utf-8" });
    res.end(
      `<!doctype html><h1>This node answers ${owner} only</h1><p>Signed in as ${login}.</p><script>indexedDB.deleteDatabase("factory-phone")</script>`,
    );
    return true;
  }
  send(res, 403, {
    error: `this node belongs to ${owner}, not ${login}`,
    owner,
    login,
  });
  return true;
}

async function body(req) {
  let text = "";
  for await (const chunk of req) text += chunk;
  return text ? JSON.parse(text) : {};
}

function publish() {
  state.snapshot = {
    ...state.snapshot,
    revision: state.snapshot.revision + 1,
    server_time: now(),
  };
  const event = `event: snapshot\ndata: ${JSON.stringify(state.snapshot)}\n\n`;
  for (const res of streams) res.write(event);
}

function stream(req, res, set, first) {
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-store",
  });
  res.write("retry: 1000\n\n");
  if (first) res.write(first);
  set.add(res);
  req.on("close", () => set.delete(res));
}

function serveFile(res, file, cache = "no-cache") {
  if (!fs.existsSync(file) || fs.statSync(file).isDirectory()) {
    res.writeHead(404).end();
    return;
  }
  res.writeHead(200, {
    "Content-Type": types[path.extname(file)] ?? "application/octet-stream",
    "Cache-Control": cache,
  });
  fs.createReadStream(file).pipe(res);
}

function page(key, prefix = "") {
  return `<!doctype html><html><head><title>plan</title></head><body style="font:16px sans-serif;width:900px;margin:24px">
<h1>plan</h1><section id="risks"><h2>Risks</h2><ul><li id="fonts">Fonts differ between macOS and Linux.</li>
<li><a id="link" href="https://example.com/">Large invoices exceed the budget.</a></li></ul></section>
<p style="height:1200px">filler</p>
<script src="${prefix}/_review/overlay.js" data-factory-review data-mobile data-theme="dark" data-events="${prefix}/r/${key}/_api/events" data-access="${prefix}/_access" data-api="${prefix}/r/${key}/_api" data-page="${key}/plan.html" data-file="plan.html" data-label="${key}" data-back="/f/${key}/pages" data-revision="r1" defer></script>
</body></html>`;
}

async function review(req, res, key, rest, prefix = "") {
  const notify = () => {
    for (const s of reviewStreams) s.write(`event: threads\ndata: ${key}\n\n`);
  };
  const method = req.method;
  if (rest === "_api/events") return stream(req, res, reviewStreams);
  if (rest === "plan.html") {
    res.writeHead(200, {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
      "X-Factory-Revision": "r1",
    });
    return res.end(page(key, prefix));
  }
  if (rest === "_api/status")
    return send(res, 200, { gate: state.gate, owner: "architect" });
  if (rest.startsWith("_api/threads") && method === "GET")
    return send(res, 200, state.threads);
  if (rest === "_api/threads" && method === "POST") {
    const n = await body(req);
    const found = state.threads.find(
      (t) => n.operation && t.op === n.operation,
    );
    if (found) return send(res, 200, found);
    const thread = {
      id: `t-${state.next++}`,
      page: n.page,
      anchor: n.anchor,
      intent: n.intent,
      status: "draft",
      op: n.operation,
      messages: [{ by: "you", text: n.text, at: now() }],
    };
    state.threads.push(thread);
    notify();
    return send(res, 200, thread);
  }
  const match = /^_api\/threads\/([^/]+)(\/replies)?$/.exec(rest);
  if (match) {
    const thread = state.threads.find((t) => t.id === match[1]);
    if (!thread) return send(res, 400, { error: "no thread" });
    const change = await body(req);
    if (method === "DELETE") {
      state.threads = state.threads.filter((t) => t !== thread);
    } else if (match[2]) {
      thread.messages.push({ by: "you", text: change.text, at: now() });
      thread.status = "draft";
    } else {
      if (change.text) thread.messages.at(-1).text = change.text;
      if (change.status) thread.status = change.status;
    }
    notify();
    return send(res, 200, thread);
  }
  if (rest === "_api/send" && method === "POST") {
    const drafts = state.threads.filter((t) => t.status === "draft");
    for (const t of drafts) t.status = "sent";
    state.actions.push({ action: "review-send", count: drafts.length });
    notify();
    return send(res, 200, { target: "architect", count: drafts.length });
  }
  if (rest === "_api/approve" && method === "POST") {
    const { gate } = await body(req);
    state.actions.push({ action: "review-approve", gate });
    state.gate = "";
    return send(res, 200, { target: "supervisor", gate });
  }
  send(res, 404, { error: "no such route" });
}

async function api(req, res, url) {
  const route = url.pathname;
  if (route === "/api/me") {
    return send(res, 200, {
      caller: owner,
      node: "node-1",
      version: "test",
      server_time: now(),
    });
  }
  if (route === "/api/snapshot") return send(res, 200, state.snapshot);
  if (route === "/api/events") {
    return stream(
      req,
      res,
      streams,
      `event: snapshot\ndata: ${JSON.stringify(state.snapshot)}\n\n`,
    );
  }
  if (route === "/api/push/subscription") {
    if (req.method === "POST") state.subscription = await body(req);
    if (req.method === "DELETE") state.subscription = null;
    const mine =
      state.subscription &&
      (req.method === "POST" ||
        state.subscription.installation ===
          url.searchParams.get("installation"));
    return send(res, 200, {
      vapid_public_key:
        "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U",
      subscribed: Boolean(mine),
      endpoint: mine ? state.subscription.endpoint : undefined,
    });
  }
  let match = /^\/api\/operations\/(.+)$/.exec(route);
  if (match) {
    const receipt = state.receipts[match[1]];
    return receipt
      ? send(res, 200, receipt)
      : send(res, 404, { error: "no such operation" });
  }
  match = /^\/api\/factories\/([^/]+)\/(actions|preview)$/.exec(route);
  if (match) {
    const request = await body(req);
    const key = decodeURIComponent(match[1]);
    if (match[2] === "preview") {
      state.previews.push(request);
      return send(res, 200, {
        text: `preview: ${request.action} ${request.text ?? (request.answers ?? []).join(" | ")}`,
      });
    }
    state.actions.push({ ...request, factory: key });
    const receipt = {
      operation: request.operation,
      factory: key,
      item: request.item,
      action: request.action,
      status: state.nextStatus ?? "delivered",
      reason: state.nextReason,
      source: "phone",
      at: now(),
    };
    state.receipts[request.operation] = receipt;
    return send(res, 200, receipt);
  }
  match = /^\/api\/links\/(.+)$/.exec(route);
  if (match) {
    for (const f of state.snapshot.factories) {
      if (f.items.some((i) => i.key === match[1]))
        return send(res, 200, {
          route: f.route,
          item: match[1],
          incarnation: f.incarnation,
          open: true,
        });
    }
    return send(res, 404, { error: "unknown" });
  }
  send(res, 404, { error: "no such route" });
}

async function control(req, res, url) {
  const route = url.pathname;
  if (route === "/__test/health") return send(res, 200, { ok: true });
  if (route === "/__test/reset") {
    reset();
    return send(res, 200, {});
  }
  if (route === "/__test/state") {
    Object.assign(state, await body(req));
    if (state.snapshotFactories) {
      state.snapshot.factories = state.snapshotFactories;
      delete state.snapshotFactories;
      publish();
    }
    return send(res, 200, {});
  }
  if (route === "/__test/actions") return send(res, 200, state);
  if (route === "/__test/change") {
    const { page: changed } = await body(req);
    for (const s of reviewStreams)
      s.write(`event: change\ndata: ${changed}\n\n`);
    return send(res, 200, {});
  }
  send(res, 404, {});
}

async function sandboxed(req, res, token, rest) {
  res.setHeader("Access-Control-Allow-Origin", "null");
  res.setHeader("Access-Control-Expose-Headers", "X-Factory-Revision");
  res.setHeader(
    "Content-Security-Policy",
    "sandbox allow-scripts allow-forms allow-modals",
  );
  if (req.method === "OPTIONS") {
    res.writeHead(204, {
      "Access-Control-Allow-Methods": "GET, POST, PATCH, DELETE",
      "Access-Control-Allow-Headers": "Content-Type",
    });
    return res.end();
  }
  if (!state.grants[token])
    return send(res, 401, { error: "This review page expired." });
  if (rest === "/_access") return res.writeHead(204).end();
  if (rest === "/_review/overlay.js")
    return serveFile(res, overlay, "no-store");
  const reviewPath = /^\/r\/([^/]+)\/(.+)$/.exec(rest);
  if (reviewPath)
    return await review(req, res, reviewPath[1], reviewPath[2], `/rt/${token}`);
  send(res, 404, {});
}

reset();
http
  .createServer(async (req, res) => {
    const url = new URL(req.url, `http://${req.headers.host}`);
    try {
      if (url.pathname.startsWith("/__test/"))
        return await control(req, res, url);
      if (state.mode === "node-down")
        return send(res, 502, { error: "bad gateway" });
      if (refused(req, res, url)) return;
      if (state.mode === "funnel")
        return send(res, 403, {
          error:
            "Funnel puts this machine on the public internet, so the node answers nothing over the network until every Funnel entry is off",
          funnel: true,
          fixes: state.fixes,
        });
      if (url.pathname.startsWith("/api/")) return await api(req, res, url);
      const granted = /^\/rt\/([^/]+)(\/.*)$/.exec(url.pathname);
      if (granted) return await sandboxed(req, res, granted[1], granted[2]);
      const reviewPath = /^\/r\/([^/]+)\/(.+)$/.exec(url.pathname);
      if (reviewPath) {
        const token = randomUUID();
        state.grants[token] = true;
        res.writeHead(303, {
          Location: `/rt/${token}${url.pathname}`,
          "Cache-Control": "no-store",
        });
        return res.end();
      }
      if (shellRoute.test(url.pathname))
        return serveFile(res, path.join(dist, "index.html"));
      serveFile(
        res,
        path.join(dist, url.pathname),
        url.pathname.startsWith("/assets/")
          ? "public, max-age=31536000, immutable"
          : "no-cache",
      );
    } catch (error) {
      send(res, 500, { error: String(error) });
    }
  })
  .listen(port, "127.0.0.1");
