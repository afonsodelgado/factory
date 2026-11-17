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
  /^\/(setup|phone(\/(add(\/[^/]+)?|nodes\/[^/]+))?|n\/[^/]+\/[^/]+|f\/[^/]+(\/(pages|message|i\/[^/]+))?)?$/;
const homeHost = `127.0.0.1:${port}`;
const homeOrigin = `http://${homeHost}`;
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
const reviewStreams = new Set();

function node(id) {
  return {
    id,
    mode: "up",
    snapshot: { node: id, revision: 1, server_time: now(), factories: [] },
    receipts: {},
    actions: [],
    previews: [],
    readiness: [],
    transfers: {},
    batches: {},
    takeovers: [],
    streams: new Set(),
  };
}

function reset() {
  if (state) for (const n of [state, ...Object.values(state.peers)]) hangUp(n);
  state = {
    ...node("node-1"),
    peers: {},
    roster: null,
    candidates: [],
    reports: {},
    enrollments: [],
    subscription: null,
    threads: [],
    next: 1,
    gate: "",
    grants: {},
  };
}

function hangUp(n) {
  for (const res of n.streams) res.destroy();
  n.streams.clear();
}

function nodeAt(host) {
  if (host === homeHost) return state;
  const member = state.roster?.members.find((m) => m.name === host);
  if (!member) return undefined;
  state.peers[host] ??= node(member.id);
  return state.peers[host];
}

function nodeById(id) {
  const member = state.roster?.members.find((m) => m.id === id);
  return member && nodeAt(member.name);
}

const shortOf = (id) =>
  state.roster?.members.find((m) => m.id === id)?.name.split(".")[0] ?? id;

const moveScript = [
  { state: "moving", phase: "preflight" },
  {
    state: "checkpoint_waiting",
    phase: "checkpoint",
    waiting: "Waiting for the supervisor to finish its stage",
  },
  { state: "moving", phase: "checkpoint" },
  { state: "released", phase: "released" },
  { state: "active", phase: "active" },
];

const takeoverScript = [
  { state: "moving", phase: "requested" },
  { state: "staged", phase: "staged" },
  { state: "active", phase: "active" },
];

function startTransfer(f, operation, source, target, script, forced = false) {
  return {
    operation,
    incarnation: f.incarnation,
    factory: f.id,
    key: f.key,
    source,
    source_name: shortOf(source),
    target,
    target_name: shortOf(target),
    forced,
    script,
    step: 0,
    polls: 0,
  };
}

function progressOf(t) {
  const { script, step, polls, ...fields } = t;
  const at = script[step];
  return { ...fields, ...at, running: at.state !== "active" && !t.cancelled };
}

function relocate(t) {
  const from = nodeById(t.source);
  const to = nodeById(t.target);
  const f = from.snapshot.factories.find(
    (x) => x.incarnation === t.incarnation,
  );
  from.snapshot.factories = from.snapshot.factories.filter((x) => x !== f);
  to.snapshot.factories = [
    ...to.snapshot.factories,
    { ...f, generation: (f.generation ?? 0) + 1 },
  ];
  publish(from);
  publish(to);
}

function advance(t) {
  if (t.cancelled || t.step === t.script.length - 1) return;
  t.polls++;
  if (t.polls % 2) return;
  t.step++;
  if (t.script[t.step].state === "active") relocate(t);
}

function holderOf(incarnation) {
  return [state, ...Object.values(state.peers)].find((n) =>
    n.snapshot.factories.some((f) => f.incarnation === incarnation),
  );
}

async function transfers(req, res, route, n) {
  if (route === "/api/transfers/takeover") {
    const request = await body(req);
    n.takeovers.push(request);
    const old = holderOf(request.factory);
    const f = old?.snapshot.factories.find(
      (x) => x.incarnation === request.factory,
    );
    if (!f)
      return send(res, 409, {
        error: "this node has no checkpoint of it, so it cannot take it over",
      });
    if (!request.confirm) {
      const warning = `Forcing a takeover starts ${f.id} on ${shortOf(n.id)} from the checkpoint ${shortOf(old.id)} took. ${shortOf(old.id)} may keep running ${f.id} until it reconnects, and work done after that checkpoint may be lost or diverge.`;
      return send(res, 428, {
        error: warning,
        warning: {
          warning,
          factory: f.id,
          incarnation: f.incarnation,
          checkpoint: "sha256:1",
          from: shortOf(old.id),
          at: now(),
        },
      });
    }
    const t = startTransfer(
      f,
      request.operation,
      old.id,
      n.id,
      takeoverScript,
      true,
    );
    n.transfers[t.operation] = t;
    return send(res, 202, progressOf(t));
  }
  if (route === "/api/transfers/all") {
    const request = await body(req);
    const items = n.snapshot.factories.map((f) => ({
      key: f.key,
      factory: f.id,
      refusal: f.unmovable,
      transfer: f.unmovable
        ? undefined
        : startTransfer(f, randomUUID(), n.id, request.target, [
            { state: "moving", phase: "preflight" },
            moveScript.at(-1),
          ]),
    }));
    n.batches[request.operation] = {
      operation: request.operation,
      target: request.target,
      items,
    };
    return send(res, 202, batchOf(n.batches[request.operation]));
  }
  let match = /^\/api\/transfers\/all\/([^/]+)(\/cancel)?$/.exec(route);
  if (match) {
    const b = n.batches[match[1]];
    if (match[2]) b.cancelled = true;
    const next = b.items.find(
      (i) => i.transfer && progressOf(i.transfer).state !== "active",
    );
    if (next && !b.cancelled) advance(next.transfer);
    return send(res, 200, batchOf(b));
  }
  match = /^\/api\/transfers\/([^/]+)(\/cancel)?$/.exec(route);
  if (match) {
    const t = n.transfers[match[1]];
    if (!t) return send(res, 404, { error: "no such move on this node" });
    if (!match[2]) {
      advance(t);
      return send(res, 200, progressOf(t));
    }
    if (["released", "active"].includes(t.script[t.step].phase))
      return send(res, 409, {
        error: `${t.factory} is already released`,
        progress: progressOf(t),
      });
    t.cancelled = true;
    t.script = [
      ...t.script,
      { state: "cancelled", phase: t.script[t.step].phase },
    ];
    t.step = t.script.length - 1;
    return send(res, 200, progressOf(t));
  }
  const request = await body(req);
  const f = n.snapshot.factories.find(
    (x) => x.key === request.factory || x.id === request.factory,
  );
  const t = startTransfer(
    f,
    request.operation,
    n.id,
    request.target,
    moveScript,
  );
  n.transfers[t.operation] = t;
  send(res, 202, progressOf(t));
}

function batchOf(b) {
  const started = !b.cancelled;
  const items = b.items.map((i) => {
    if (i.refusal)
      return {
        key: i.key,
        factory: i.factory,
        state: "refused",
        reason: i.refusal,
      };
    const p = progressOf(i.transfer);
    const state = p.state === "active" || started ? p.state : "cancelled";
    return {
      key: i.key,
      factory: i.factory,
      operation: p.operation,
      state,
      progress: p,
    };
  });
  return {
    operation: b.operation,
    target: b.target,
    target_name: shortOf(b.target),
    running:
      started && items.some((i) => !["active", "refused"].includes(i.state)),
    cancelled: b.cancelled,
    items,
  };
}

function snapshotOf(n) {
  return state.roster ? { ...n.snapshot, roster: state.roster } : n.snapshot;
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
  const machine = req.headers.host.split(".")[0];
  const page =
    req.method === "GET" &&
    !url.pathname.startsWith("/api/") &&
    !url.pathname.includes("/_api/");
  if (page) {
    res.writeHead(403, { "Content-Type": "text/html; charset=utf-8" });
    res.end(
      `<!doctype html><h1>${machine} answers ${owner} only</h1><p>This phone is signed in to Tailscale as ${login}.</p><script>indexedDB.deleteDatabase("factory-phone")</script>`,
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

function publish(n = state) {
  n.snapshot = {
    ...n.snapshot,
    revision: n.snapshot.revision + 1,
    server_time: now(),
  };
  const event = `event: snapshot\ndata: ${JSON.stringify(snapshotOf(n))}\n\n`;
  for (const res of n.streams) res.write(event);
}

function publishAll() {
  for (const n of [state, ...Object.values(state.peers)]) publish(n);
}

function cors(req, res, url) {
  const origin = req.headers.origin;
  if (!origin || url.pathname.startsWith("/rt/")) return false;
  res.setHeader("Vary", "Origin");
  const allowed =
    origin === `http://${req.headers.host}` || origin === homeOrigin;
  const preflight =
    req.method === "OPTIONS" && req.headers["access-control-request-method"];
  if (allowed) res.setHeader("Access-Control-Allow-Origin", origin);
  if (preflight && !allowed) {
    res.writeHead(403).end();
    return true;
  }
  if (preflight) {
    res.writeHead(204, {
      "Access-Control-Allow-Methods": "GET, POST",
      "Access-Control-Allow-Headers": "Content-Type",
    });
    res.end();
    return true;
  }
  if (allowed || !url.pathname.startsWith("/api/")) return false;
  send(res, 403, {
    error:
      "this node answers browser reads only from its own origin and the phone app's home",
  });
  return true;
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

function reportFor(name) {
  return (
    state.reports[name] ?? {
      target: { name, short: name.split(".")[0] },
      checks: [],
      refusal: `no machine called ${name} is on this tailnet`,
      problems: 0,
      at: now(),
    }
  );
}

async function enroll(req, res, route) {
  const request = await body(req);
  state.enrollments.push({
    route,
    host: req.headers.host,
    origin: req.headers.origin,
    replacePushKey: request.replace_push_key,
  });
  if (req.headers.origin !== homeOrigin)
    return send(res, 403, {
      error: `changing the node list from a browser works only from the phone app at ${homeOrigin}`,
    });
  if (route === "/api/nodes/check")
    return send(res, 200, reportFor(request.name));
  const removing = /^\/api\/nodes\/([^/]+)\/remove$/.exec(route);
  if (removing && removing[1] === state.id)
    return send(res, 409, {
      error:
        "this is the node you are talking to; run factory node uninstall on it instead",
    });
  if (removing) {
    state.roster.members = state.roster.members.filter(
      (m) => m.id !== removing[1],
    );
    publishAll();
    return send(res, 200, state.roster);
  }
  const report = reportFor(request.name);
  if (report.refusal) return send(res, 409, { error: report.refusal, report });
  const member = {
    id: `node-${state.roster.members.length + 1}`,
    name: report.target.name,
    owner,
    tailnet: "localhost",
    pending: 0,
  };
  state.roster.members.push(member);
  publishAll();
  const keepsItsOwn = report.push_conflict && !request.replace_push_key;
  const push = {
    fingerprint: keepsItsOwn ? "SHA256:theirs" : "SHA256:shared",
    phones: 1,
    shared: !keepsItsOwn,
    conflict: keepsItsOwn
      ? `${report.target.short} is added, but it keeps its own push key, so it can't notify your phone: one key serves every node`
      : undefined,
  };
  send(res, 200, { member, report, roster: state.roster, push });
}

async function api(req, res, url, n) {
  const route = url.pathname;
  const home = n === state;
  if (route === "/api/me") {
    return send(res, 200, {
      caller: owner,
      node: n.id,
      version: "test",
      server_time: now(),
    });
  }
  if (route === "/api/snapshot") return send(res, 200, snapshotOf(n));
  if (route === "/api/events") {
    return stream(
      req,
      res,
      n.streams,
      `event: snapshot\ndata: ${JSON.stringify(snapshotOf(n))}\n\n`,
    );
  }
  if (route === "/api/readiness")
    return send(res, 200, {
      node: n.id,
      at: now(),
      checks: n.readiness,
      problems: 0,
    });
  if (home && route === "/api/nodes" && req.method === "GET")
    return send(res, 200, state.roster);
  if (home && route === "/api/nodes/candidates")
    return send(res, 200, { candidates: state.candidates, at: now() });
  if (home && route === "/api/repos") return send(res, 200, state.repos ?? {});
  if (home && route.startsWith("/api/nodes")) return enroll(req, res, route);
  if (route.startsWith("/api/transfers")) return transfers(req, res, route, n);
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
      fingerprint: "SHA256:shared",
      health: "ready",
      subscribed: Boolean(mine),
      endpoint: mine ? state.subscription.endpoint : undefined,
    });
  }
  let match = /^\/api\/operations\/(.+)$/.exec(route);
  if (match) {
    const receipt = n.receipts[match[1]];
    return receipt
      ? send(res, 200, receipt)
      : send(res, 404, { error: "no such operation" });
  }
  match = /^\/api\/factories\/([^/]+)\/(actions|preview)$/.exec(route);
  if (match) {
    const request = await body(req);
    const key = decodeURIComponent(match[1]);
    if (match[2] === "preview") {
      n.previews.push(request);
      return send(res, 200, {
        text: `preview: ${request.action} ${request.text ?? (request.answers ?? []).join(" | ")}`,
      });
    }
    n.actions.push({ ...request, factory: key });
    const receipt = {
      operation: request.operation,
      factory: key,
      item: request.item,
      action: request.action,
      status: n.nextStatus ?? "delivered",
      reason: n.nextReason,
      source: "phone",
      at: now(),
    };
    n.receipts[request.operation] = receipt;
    return send(res, 200, receipt);
  }
  match = /^\/api\/links\/(.+)$/.exec(route);
  if (match) {
    for (const f of n.snapshot.factories) {
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
    const { roster, ...rest } = await body(req);
    Object.assign(state, rest);
    if (state.mode !== "up") hangUp(state);
    if (roster !== undefined) {
      state.roster = roster;
      publishAll();
    }
    if (state.snapshotFactories) {
      state.snapshot.factories = state.snapshotFactories;
      delete state.snapshotFactories;
      publish();
    }
    return send(res, 200, {});
  }
  if (route === "/__test/node") {
    const { name, factories, ...rest } = await body(req);
    const n = nodeAt(name);
    Object.assign(n, rest);
    if (n.mode !== "up") hangUp(n);
    if (factories) {
      n.snapshot.factories = factories;
      publish(n);
    }
    return send(res, 200, {});
  }
  if (route === "/__test/reply") {
    const { text } = await body(req);
    for (const receipt of Object.values(state.receipts))
      if (receipt.action === "message")
        Object.assign(receipt, {
          status: "delivered",
          reply: { at: now(), text },
        });
    publish();
    return send(res, 200, {});
  }
  if (route === "/__test/actions")
    return send(res, 200, {
      ...state,
      streams: undefined,
      peers: Object.fromEntries(
        Object.entries(state.peers).map(([host, n]) => [
          host,
          { ...n, streams: undefined },
        ]),
      ),
    });
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
      const n = nodeAt(req.headers.host);
      if (!n || n.mode === "offline") return req.socket.destroy();
      if (n.mode === "node-down")
        return send(res, 502, { error: "bad gateway" });
      if (refused(req, res, url)) return;
      if (cors(req, res, url)) return;
      if (n !== state)
        return url.pathname.startsWith("/api/")
          ? await api(req, res, url, n)
          : send(res, 404, { error: "no such route" });
      if (state.mode === "funnel")
        return send(res, 403, {
          error:
            "Funnel puts this machine on the public internet, so the node answers nothing over the network until every Funnel entry is off",
          funnel: true,
          fixes: state.fixes,
        });
      if (url.pathname.startsWith("/api/"))
        return await api(req, res, url, state);
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
