(() => {
  if (window.top !== window || window.__factoryReviewLoaded) return;
  const script = document.currentScript;
  const config = script && script.dataset.factoryReview !== undefined ? { ...script.dataset } : window.__factoryReviewConfig;
  if (!config) return;
  window.__factoryReviewLoaded = true;
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => start(config), { once: true });
  else start(config);
})();

function start(config) {
  const factoryKey = config.page.split("/")[0];
  const isApp = config.file === "app" || config.file === "prototype";
  const mobile = config.mobile !== undefined;
  const touch = matchMedia("(pointer: coarse)").matches;
  const viewport = window.visualViewport;
  const unreachable = "Can't reach the computer. This page is read-only until it is back. Trying again every 15s.";
  const serveDown = "The factory node is not answering, so this page is read-only and notifications have stopped. Start it with factory node run, or factory node install as a service.";
  const restarting = "factory is restarting. This page is read-only until it is back. Trying again every 15s.";
  const binding = typeof window.__factoryReview === "function" ? window.__factoryReview : null;
  const pendingCalls = new Map();
  let callSeq = 0;
  const sessionKey = (name) => "factory-review:" + name + ":" + config.page;
  const intents = [
    ["fix", "Fix"],
    ["change", "Change"],
    ["question", "Question"],
  ];

  const state = {
    threads: [],
    mode: "idle",
    hovered: null,
    composer: null,
    panelOpen: remember("panel") === "1",
    showResolved: false,
    replying: null,
    pendingReload: false,
    status: { owner: "", gate: "" },
    sending: false,
    confirmApprove: false,
    picked: null,
    offline: "",
  };

  function remember(name, value) {
    const key = sessionKey(name);
    try {
      if (value === undefined) return sessionStorage.getItem(key);
      if (value === null) sessionStorage.removeItem(key);
      else sessionStorage.setItem(key, value);
      return null;
    } catch {}
    let kept;
    try {
      kept = JSON.parse(window.name || "{}");
    } catch {
      return null;
    }
    if (kept === null || typeof kept !== "object" || Array.isArray(kept)) return null;
    if (value === undefined) return kept[key] ?? null;
    if (value === null) delete kept[key];
    else kept[key] = value;
    window.name = JSON.stringify(kept);
    return null;
  }

  window.__factoryReviewReceive = (id, status, text) => {
    const call = pendingCalls.get(id);
    if (!call) return;
    pendingCalls.delete(id);
    if (status >= 200 && status < 300) call.resolve(JSON.parse(text));
    else call.reject(new Error(String(text).trim() || "status " + status));
  };

  async function api(method, path, body) {
    if (binding) {
      return new Promise((resolve, reject) => {
        const id = ++callSeq;
        pendingCalls.set(id, { resolve, reject });
        binding(JSON.stringify({ id, api: config.api, method, path, body }));
      });
    }
    let response;
    try {
      response = await fetch(config.api + path, {
        method,
        headers: method === "GET" ? {} : { "Content-Type": "application/json" },
        body: method === "GET" ? undefined : JSON.stringify(body ?? {}),
      });
    } catch (error) {
      reachable(unreachable);
      throw error;
    }
    answered(response.status);
    if (!response.ok) throw new Error(errorText(await response.text()) || response.statusText);
    return response.json();
  }

  function errorText(text) {
    try {
      return JSON.parse(text).error || text.trim();
    } catch {
      return text.trim();
    }
  }

  function answered(status) {
    if (!mobile) return;
    if (status === 401) location.reload();
    else if (status === 502) reachable(serveDown);
    else if (status === 503) reachable(restarting);
    else reachable("");
  }

  function reachable(reason) {
    if (!mobile || state.offline === reason) return;
    state.offline = reason;
    renderBar();
    renderPanel();
  }

  async function checkAccess() {
    try {
      answered((await fetch(config.access, { cache: "no-store" })).status);
    } catch {
      reachable(unreachable);
    }
  }

  async function resync() {
    load();
    try {
      const response = await fetch(location.href, { method: "HEAD", cache: "no-store" });
      if (response.ok && config.revision && response.headers.get("X-Factory-Revision") !== config.revision) reloadPage();
    } catch {}
  }

  const host = document.createElement("factory-review");
  host.style.cssText = "all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647;";
  const root = host.attachShadow({ mode: "open" });
  root.innerHTML = `<style>${styles()}</style>
    <div class="highlight${mobile ? " mobile" : ""}" hidden><span class="highlight-label"></span></div>
    <div class="pins${mobile ? " mobile" : ""}"></div>
    <div class="chrome${mobile ? " mobile" : ""}">
      ${mobile ? `<header class="top"></header>` : ""}
      <aside class="panel" hidden aria-label="Review comments"></aside>
      <form class="composer" hidden></form>
      <div class="bar" role="toolbar" aria-label="Factory review"></div>
      <div class="toast" role="status" hidden></div>
    </div>`;
  const $ = (selector) => root.querySelector(selector);
  const ui = {
    highlight: $(".highlight"),
    highlightLabel: $(".highlight-label"),
    pins: $(".pins"),
    composer: $(".composer"),
    panel: $(".panel"),
    bar: $(".bar"),
    toast: $(".toast"),
    top: $(".top"),
    chrome: $(".chrome"),
  };
  document.documentElement.appendChild(host);
  const spacers = mobile ? [spacer("afterbegin"), spacer("beforeend")] : [];

  const cursorStyle = document.createElement("style");
  cursorStyle.textContent = "html.factory-review-picking, html.factory-review-picking * { cursor: crosshair !important; }";
  document.head.appendChild(cursorStyle);

  restoreScroll();
  connectEvents();
  load();
  renderBar();
  renderPanel();
  addEventListener("keydown", onKey, true);
  addEventListener("scroll", schedulePins, true);
  addEventListener("resize", schedulePins);
  setInterval(schedulePins, 500);
  if (mobile) {
    syncViewport();
    viewport?.addEventListener("resize", followViewport);
    viewport?.addEventListener("scroll", followViewport);
    setInterval(() => state.offline && load(), 15000);
  }

  function spacer(where) {
    const element = document.createElement("div");
    element.dataset.factoryReviewSpacer = "";
    document.body.insertAdjacentElement(where, element);
    return element;
  }

  function followViewport() {
    syncViewport();
    schedulePins();
  }

  function syncViewport() {
    if (!viewport) return;
    const scale = viewport.scale;
    ui.chrome.classList.toggle("phone", viewport.width * scale < 900);
    Object.assign(ui.chrome.style, {
      width: viewport.width * scale + "px",
      height: viewport.height * scale + "px",
      transform: `translate(${viewport.offsetLeft}px, ${viewport.offsetTop}px) scale(${1 / scale})`,
    });
    host.style.setProperty("--ui-scale", String(1 / scale));
    ui.chrome.style.setProperty("--bar-h", ui.bar.offsetHeight + "px");
    spacers[0].style.height = Math.ceil(ui.top.offsetHeight / scale) + "px";
    spacers[1].style.height = Math.ceil(ui.bar.offsetHeight / scale) + "px";
  }

  function narrow() {
    return ui.chrome.classList.contains("phone");
  }

  async function load() {
    const page = encodeURIComponent(config.file);
    try {
      const [threads, status] = await Promise.all([api("GET", "/threads?page=" + page), api("GET", "/status?page=" + page).catch(() => state.status)]);
      state.threads = threads;
      state.status = status;
    } catch (error) {
      toast("Could not load comments: " + error.message);
    }
    renderAll();
  }

  async function send() {
    if (state.sending) return;
    state.sending = true;
    renderBar();
    try {
      const result = await api("POST", "/send", { page: config.file });
      const who = result.target === "supervisor" ? "the supervisor" : "the " + result.target;
      toast(`Sent ${result.count} ${result.count === 1 ? "comment" : "comments"} to ${who}.`);
    } catch (error) {
      toast("Not sent: " + error.message);
    }
    state.sending = false;
    await load();
  }

  let confirmTimer;
  async function approve() {
    if (!state.confirmApprove) {
      state.confirmApprove = true;
      renderBar();
      clearTimeout(confirmTimer);
      confirmTimer = setTimeout(() => {
        state.confirmApprove = false;
        renderBar();
      }, 4000);
      return;
    }
    state.confirmApprove = false;
    clearTimeout(confirmTimer);
    try {
      await api("POST", "/approve", { page: config.file, gate: state.status.gate });
      toast("Approval sent to the supervisor.");
    } catch (error) {
      toast("Not approved: " + error.message);
    }
    await load();
  }

  function renderAll() {
    renderBar();
    renderPanel();
    renderPins();
  }

  function connectEvents() {
    window.__factoryReviewEvent = (name, data) => {
      if (name === "threads" && data === factoryKey) load();
    };
    window.__factoryReviewHide = (hidden) => {
      host.style.visibility = hidden ? "hidden" : "";
    };
    if (config.events) openEvents(false);
  }

  function openEvents(lost) {
    const events = new EventSource(config.events);
    let reloadTimer;
    events.onopen = () => {
      if (lost) resync();
      lost = false;
    };
    events.onerror = () => {
      lost = true;
      if (mobile) checkAccess();
      if (events.readyState !== EventSource.CLOSED) return;
      setTimeout(() => openEvents(true), state.offline ? 15000 : 3000);
    };
    events.addEventListener("change", (e) => {
      if (e.data !== config.page) return;
      clearTimeout(reloadTimer);
      reloadTimer = setTimeout(reloadPage, 150);
    });
    events.addEventListener("threads", (e) => {
      if (e.data === factoryKey) load();
    });
  }

  function busyTyping() {
    return state.composer !== null || state.replying !== null;
  }

  function reloadPage() {
    if (busyTyping()) {
      state.pendingReload = true;
      renderBar();
      return;
    }
    remember("scroll", String(scrollY));
    remember("panel", state.panelOpen ? "1" : "0");
    location.reload();
  }

  function reloadIfPending() {
    if (state.pendingReload && !busyTyping()) reloadPage();
  }

  function restoreScroll() {
    const saved = remember("scroll");
    if (saved === null) return;
    remember("scroll", null);
    const restore = () => scrollTo(0, Number(saved));
    if (document.readyState === "complete") restore();
    else addEventListener("load", restore, { once: true });
  }

  function pageThreads() {
    return state.threads.filter((t) => t.page === config.file);
  }

  function onThisURL(thread) {
    if (!isApp || !thread.anchor.url) return true;
    try {
      return new URL(thread.anchor.url).pathname === location.pathname;
    } catch {
      return true;
    }
  }

  function pathOf(thread) {
    try {
      return new URL(thread.anchor.url).pathname;
    } catch {
      return "";
    }
  }

  function numberOf(thread) {
    return pageThreads().indexOf(thread) + 1;
  }

  function visibleThreads() {
    return pageThreads().filter((t) => state.showResolved || t.status !== "resolved");
  }

  function counts() {
    const threads = pageThreads();
    return {
      open: threads.filter((t) => t.status !== "resolved").length,
      drafts: threads.filter((t) => t.status === "draft").length,
      resolved: threads.filter((t) => t.status === "resolved").length,
    };
  }

  function renderBar() {
    renderTop();
    if (state.mode === "pick" && touch) return renderPickBar();
    const { open, drafts } = counts();
    const picking = state.mode === "pick";
    const off = state.offline ? "disabled" : "";
    ui.bar.innerHTML = `
      ${notes(open, drafts)}
      <div class="buttons">
        <button class="bar-button ${picking ? "active" : ""}" data-action="pick" aria-pressed="${picking}" title="Comment on an element (C)" ${off}>
          ${icon("comment")}<span>${picking ? "Click an element" : "Comment"}</span>${touch ? "" : "<kbd>C</kbd>"}
        </button>
        <button class="bar-button fit" data-action="panel" aria-expanded="${state.panelOpen}" title="Show comments">
          ${icon("list")}<span>${open}${touch ? "" : " open"}</span>${drafts ? `<span class="badge">${drafts}${touch ? "" : " not sent"}</span>` : ""}
        </button>
        ${drafts ? `<button class="bar-button primary-pill" data-action="send" ${state.sending || state.offline ? "disabled" : ""} title="Send the comments that are not sent yet${state.status.owner ? " to the " + state.status.owner : ""}">${icon("send")}<span>${state.sending ? "Sending…" : `Send ${drafts}`}</span></button>` : ""}
        ${state.status.gate ? `<button class="bar-button approve ${state.confirmApprove ? "confirm" : ""}" data-action="approve" title="Approve this stage's gate" ${off}>${icon("check")}<span>${state.confirmApprove ? (touch ? "Tap again to approve" : "Click again to approve") : "Approve"}</span></button>` : ""}
      </div>`;
    ui.bar.querySelector('[data-action="pick"]').onclick = () => (state.mode === "pick" ? stopPicking() : startPicking());
    ui.bar.querySelector('[data-action="panel"]').onclick = () => togglePanel();
    const sendButton = ui.bar.querySelector('[data-action="send"]');
    if (sendButton) sendButton.onclick = send;
    const approveButton = ui.bar.querySelector('[data-action="approve"]');
    if (approveButton) approveButton.onclick = approve;
    if (mobile) syncViewport();
  }

  function notes(open, drafts) {
    const lines = [];
    if (state.offline) lines.push(`<p class="bar-note warn">${escapeHTML(state.offline)}</p>`);
    if (state.pendingReload) lines.push(`<p class="bar-note">New version waiting · finish your comment</p>`);
    if (state.confirmApprove && (open || drafts)) lines.push(`<p class="bar-note warn">${escapeHTML(unfinished(open, drafts))}</p>`);
    return lines.join("");
  }

  function unfinished(open, drafts) {
    const parts = [];
    if (open) parts.push(`${open} ${open === 1 ? "thread is" : "threads are"} still open on this page`);
    if (drafts) parts.push(`${drafts} ${drafts === 1 ? "comment is" : "comments are"} not sent`);
    return parts.join(", and ") + ".";
  }

  function renderPickBar() {
    const picked = state.picked ? "" : "disabled";
    ui.bar.innerHTML = `
      <p class="bar-note"><span class="key">picking</span>${state.picked ? "Tap moves the highlight." : "Tap an element."} Scroll and pinch work as usual.</p>
      <div class="buttons">
        <button class="bar-button fit" data-action="parent" ${picked}>↑ Parent</button>
        <button class="bar-button primary-pill" data-action="comment-picked" ${picked}>Comment on this</button>
        <button class="bar-button fit" data-action="stop" aria-label="Stop picking">${icon("close")}</button>
      </div>`;
    ui.bar.querySelector('[data-action="parent"]').onclick = pickParent;
    ui.bar.querySelector('[data-action="comment-picked"]').onclick = () => {
      const element = state.picked;
      stopPicking();
      openComposer({ element });
    };
    ui.bar.querySelector('[data-action="stop"]').onclick = stopPicking;
    if (mobile) syncViewport();
  }

  function pickParent() {
    const parent = state.picked && state.picked.parentElement;
    if (!parent || parent === document.documentElement) return;
    state.picked = parent;
    showHighlight(parent);
  }

  function renderTop() {
    if (!ui.top) return;
    ui.top.innerHTML = `<a class="back" href="${escapeHTML(config.back)}" aria-label="Back to ${escapeHTML(config.label)} pages">‹</a>
      <span class="sect"><span class="dim">${escapeHTML(config.label)} / </span>${escapeHTML(config.file)}</span>
      ${state.status.gate ? `<span class="where"><span class="flag">⚑</span>gate open</span>` : ""}`;
  }

  function togglePanel(open = !state.panelOpen) {
    state.panelOpen = open;
    remember("panel", open ? "1" : "0");
    renderBar();
    renderPanel();
  }

  const mouseEvents = ["mousemove", "click", "mousedown", "mouseup", "pointerdown", "pointerup"];
  const touchEvents = ["pointerdown", "pointermove", "pointerup", "pointercancel", "click"];
  const pickEvents = touch ? touchEvents : mouseEvents;
  const onPick = touch ? onTouchPick : onPickEvent;

  function startPicking() {
    closeComposer();
    state.mode = "pick";
    state.picked = null;
    document.documentElement.classList.add("factory-review-picking");
    for (const type of pickEvents) addEventListener(type, onPick, true);
    renderBar();
  }

  function stopPicking() {
    state.mode = "idle";
    state.hovered = null;
    state.picked = null;
    ui.highlight.hidden = true;
    document.documentElement.classList.remove("factory-review-picking");
    for (const type of pickEvents) removeEventListener(type, onPick, true);
    renderBar();
  }

  function ours(event) {
    return event.composedPath().includes(host);
  }

  function onPickEvent(event) {
    if (ours(event)) return;
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.type === "mousemove") {
      state.hovered = target;
      showHighlight(target);
      return;
    }
    if (event.type === "click") {
      stopPicking();
      openComposer({ element: target });
    }
  }

  const touches = new Set();
  let tap = null;

  function onTouchPick(event) {
    if (ours(event)) return;
    if (event.type === "click") {
      event.preventDefault();
      event.stopPropagation();
      return;
    }
    if (event.type === "pointerdown") {
      touches.add(event.pointerId);
      tap = touches.size === 1 ? { x: event.clientX, y: event.clientY, target: event.target } : null;
      return;
    }
    if (event.type === "pointermove") {
      if (tap && Math.hypot(event.clientX - tap.x, event.clientY - tap.y) > 10) tap = null;
      return;
    }
    touches.delete(event.pointerId);
    const tapped = event.type === "pointerup" && tap && tap.target instanceof Element ? tap.target : null;
    tap = null;
    if (!tapped || tapped.closest("[data-factory-review-spacer]")) return;
    state.picked = tapped;
    showHighlight(tapped);
    renderBar();
  }

  function showHighlight(element, label = describe(element)) {
    const rect = element.getBoundingClientRect();
    Object.assign(ui.highlight.style, {
      left: rect.left + "px",
      top: rect.top + "px",
      width: rect.width + "px",
      height: rect.height + "px",
    });
    ui.highlightLabel.textContent = label;
    ui.highlightLabel.classList.toggle("below", rect.top < 28);
    ui.highlight.hidden = false;
  }

  function describe(element) {
    const tag = element.tagName.toLowerCase();
    const component = componentsOf(element)[0];
    const size = element.getBoundingClientRect();
    return `${component ? component + " · " : ""}${tag} · ${Math.round(size.width)}×${Math.round(size.height)}`;
  }

  function onKey(event) {
    const typing = isEditable(event.target) || ours(event);
    if (event.key === "Escape") {
      if (state.composer) closeComposer();
      else if (state.mode === "pick") stopPicking();
      else if (state.panelOpen && !typing) togglePanel(false);
      else return;
      event.preventDefault();
      return;
    }
    if (typing || event.metaKey || event.ctrlKey || event.altKey) return;
    if (event.key === "c" || event.key === "C") {
      event.preventDefault();
      state.mode === "pick" ? stopPicking() : startPicking();
    }
  }

  function isEditable(target) {
    return target instanceof Element && (target.isContentEditable || /^(input|textarea|select)$/i.test(target.tagName));
  }

  function openComposer({ element = null, thread = null }) {
    state.composer = { element, thread, intent: thread ? thread.intent : "fix", operation: crypto.randomUUID?.() };
    const editing = thread !== null;
    const text = editing ? thread.messages[thread.messages.length - 1].text : "";
    const target = element ? describe(element) : thread && thread.anchor.selector ? "element" : "the whole page";
    ui.composer.innerHTML = `
      <span class="grab" aria-hidden="true"></span>
      <header><span class="composer-title">${editing ? "Edit comment" : "New comment"}</span><span class="muted">${escapeHTML(target)}</span></header>
      <div class="intents" role="radiogroup" aria-label="Kind of comment">
        ${intents.map(([value, label]) => `<button type="button" role="radio" class="chip" data-intent="${value}" aria-checked="${value === state.composer.intent}">${label}</button>`).join("")}
      </div>
      <textarea rows="4" placeholder="What should change?" aria-label="Comment">${escapeHTML(text)}</textarea>
      <footer>
        ${touch ? "" : `<span class="muted">⌘↵ save · Esc cancel</span>`}
        ${editing ? `<button type="button" class="ghost danger" data-action="delete">Delete</button>` : ""}
        <button type="button" class="ghost" data-action="cancel">Cancel</button>
        <button type="submit" class="primary" ${state.offline ? "disabled" : ""}>Save</button>
      </footer>`;
    ui.composer.hidden = false;
    for (const chip of ui.composer.querySelectorAll(".chip")) {
      chip.onclick = () => {
        state.composer.intent = chip.dataset.intent;
        for (const other of ui.composer.querySelectorAll(".chip")) other.setAttribute("aria-checked", String(other === chip));
        ui.composer.querySelector("textarea").focus();
      };
    }
    const textarea = ui.composer.querySelector("textarea");
    textarea.onkeydown = (e) => {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        saveComposer();
      }
    };
    ui.composer.onkeydown = trapFocus;
    ui.composer.onsubmit = (e) => {
      e.preventDefault();
      saveComposer();
    };
    ui.composer.querySelector('[data-action="cancel"]').onclick = closeComposer;
    const remove = ui.composer.querySelector('[data-action="delete"]');
    if (remove) remove.onclick = () => deleteThread(thread);
    placeComposer();
    if (element) showHighlight(element);
    textarea.focus();
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);
  }

  function trapFocus(event) {
    if (event.key !== "Tab") return;
    const focusable = [...ui.composer.querySelectorAll("button, textarea")];
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const active = root.activeElement;
    if (event.shiftKey && active === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function placeComposer() {
    if (mobile && narrow()) {
      ui.composer.removeAttribute("style");
      return;
    }
    const width = Math.min(340, innerWidth - 32);
    const height = ui.composer.offsetHeight || 220;
    const element = state.composer && (state.composer.element || (state.composer.thread && locate(state.composer.thread)));
    let left = innerWidth - width - 16;
    let top = innerHeight - height - 72;
    if (element) {
      const rect = element.getBoundingClientRect();
      left = clamp(rect.left, 16, innerWidth - width - 16);
      top = rect.bottom + 8 + height < innerHeight ? rect.bottom + 8 : rect.top - height - 8;
      top = clamp(top, 16, innerHeight - height - 16);
    }
    Object.assign(ui.composer.style, { left: left + "px", top: top + "px", width: width + "px" });
  }

  function closeComposer() {
    if (!state.composer) return;
    state.composer = null;
    ui.composer.hidden = true;
    ui.highlight.hidden = true;
    reloadIfPending();
  }

  async function saveComposer() {
    const composer = state.composer;
    const text = ui.composer.querySelector("textarea").value.trim();
    if (!text) {
      toast("Write the comment first.");
      return;
    }
    try {
      if (composer.thread) {
        await api("PATCH", "/threads/" + composer.thread.id, { text, intent: composer.intent });
      } else {
        await api("POST", "/threads", { page: config.file, intent: composer.intent, text, anchor: anchorFor(composer.element), operation: composer.operation });
      }
      closeComposer();
      await load();
    } catch (error) {
      toast("Not saved: " + error.message);
    }
  }

  async function deleteThread(thread) {
    try {
      await api("DELETE", "/threads/" + thread.id);
      closeComposer();
      await load();
    } catch (error) {
      toast("Not deleted: " + error.message);
    }
  }

  function anchorFor(element) {
    const anchor = { url: location.href, viewport: { x: scrollX, y: scrollY, w: innerWidth, h: innerHeight } };
    if (!element) return anchor;
    const rect = element.getBoundingClientRect();
    return Object.assign(anchor, {
      selector: selectorFor(element),
      text: textOf(element),
      html: element.outerHTML.slice(0, 4000),
      rect: { x: rect.left + scrollX, y: rect.top + scrollY, w: rect.width, h: rect.height },
      components: componentsOf(element),
    });
  }

  function textOf(element) {
    return (element.innerText || element.textContent || "").replace(/\s+/g, " ").trim().slice(0, 200);
  }

  function selectorFor(element) {
    const unique = (selector) => {
      try {
        return document.querySelectorAll(selector).length === 1;
      } catch {
        return false;
      }
    };
    if (element.id && unique("#" + CSS.escape(element.id))) return "#" + CSS.escape(element.id);
    const tag = element.tagName.toLowerCase();
    for (const attribute of ["data-testid", "data-test", "data-id", "aria-label", "name"]) {
      const value = element.getAttribute(attribute);
      const selector = value && `${tag}[${attribute}="${CSS.escape(value)}"]`;
      if (selector && unique(selector)) return selector;
    }
    const parts = [];
    let node = element;
    while (node && node.nodeType === 1 && node !== document.documentElement) {
      if (node.id && unique("#" + CSS.escape(node.id))) {
        parts.unshift("#" + CSS.escape(node.id));
        break;
      }
      const name = node.tagName.toLowerCase();
      const siblings = node.parentElement ? [...node.parentElement.children].filter((c) => c.tagName === node.tagName) : [];
      parts.unshift(siblings.length > 1 ? `${name}:nth-of-type(${siblings.indexOf(node) + 1})` : name);
      node = node.parentElement;
    }
    return parts.join(" > ");
  }

  function componentsOf(element) {
    const key = Object.keys(element).find((k) => k.startsWith("__reactFiber$") || k.startsWith("__reactInternalInstance$"));
    if (!key) return [];
    const names = [];
    for (let fiber = element[key]; fiber && names.length < 6; fiber = fiber.return) {
      const name = componentName(fiber.type);
      if (name && !names.includes(name)) names.push(name);
    }
    return names;
  }

  function componentName(type) {
    if (!type || typeof type === "string") return null;
    if (typeof type === "function") return type.displayName || type.name || null;
    if (typeof type === "object") return type.displayName || componentName(type.type) || componentName(type.render);
    return null;
  }

  function locate(thread) {
    const anchor = thread.anchor || {};
    if (!anchor.selector) return null;
    try {
      const found = document.querySelector(anchor.selector);
      if (found) return found;
    } catch {}
    if (!anchor.text) return null;
    const tag = (anchor.selector.match(/([a-z][a-z0-9-]*)(?::nth-of-type\(\d+\))?$/i) || [])[1] || "*";
    for (const candidate of document.querySelectorAll(tag)) {
      if (textOf(candidate) === anchor.text) return candidate;
    }
    return null;
  }

  let pinFrame = 0;
  function schedulePins() {
    if (pinFrame) return;
    pinFrame = requestAnimationFrame(() => {
      pinFrame = 0;
      renderPins();
      if (state.picked) showHighlight(state.picked);
      if (!state.composer) return;
      placeComposer();
      if (state.composer.element) showHighlight(state.composer.element);
    });
  }

  function renderPins() {
    ui.pins.innerHTML = "";
    for (const thread of visibleThreads()) {
      if (!onThisURL(thread)) continue;
      const element = locate(thread);
      if (!element) continue;
      const rect = element.getBoundingClientRect();
      if (rect.bottom < 0 || rect.top > innerHeight || (rect.width === 0 && rect.height === 0)) continue;
      const pin = document.createElement("button");
      pin.className = "pin " + thread.status;
      pin.textContent = String(numberOf(thread));
      pin.title = thread.messages[0].text;
      pin.setAttribute("aria-label", `Comment ${numberOf(thread)}: ${thread.messages[0].text}`);
      pin.style.left = clamp(rect.right - 12, 4, innerWidth - 28) + "px";
      pin.style.top = clamp(rect.top - 12, 4, innerHeight - 28) + "px";
      pin.onmouseenter = () => showHighlight(element, "#" + numberOf(thread));
      pin.onmouseleave = () => {
        if (!state.composer) ui.highlight.hidden = true;
      };
      pin.onclick = () => {
        togglePanel(true);
        focusCard(thread);
      };
      ui.pins.appendChild(pin);
    }
  }

  function renderPanel() {
    ui.panel.hidden = !state.panelOpen;
    if (!state.panelOpen) return;
    const { open, resolved } = counts();
    const threads = visibleThreads();
    ui.panel.innerHTML = `
      <header>
        <div><strong>Comments</strong><span class="muted"> · ${open} open</span></div>
        <button class="icon-button" data-action="close" aria-label="Close comments">${icon("close")}</button>
      </header>
      <div class="panel-actions">
        <button class="ghost" data-action="page-comment" ${state.offline ? "disabled" : ""}>${icon("comment")} Comment on the whole page</button>
        ${resolved ? `<label class="toggle"><input type="checkbox" ${state.showResolved ? "checked" : ""} data-action="resolved"> Show ${resolved} resolved</label>` : ""}
      </div>
      <ol class="cards">${threads.map(card).join("") || `<li class="empty">No comments yet. Press <kbd>C</kbd> and click anything on the page.</li>`}</ol>`;
    ui.panel.querySelector('[data-action="close"]').onclick = () => togglePanel(false);
    ui.panel.querySelector('[data-action="page-comment"]').onclick = () => openComposer({});
    const resolvedToggle = ui.panel.querySelector('[data-action="resolved"]');
    if (resolvedToggle) resolvedToggle.onchange = () => {
      state.showResolved = resolvedToggle.checked;
      renderPanel();
      renderPins();
    };
    for (const element of ui.panel.querySelectorAll(".card")) wireCard(element);
  }

  function card(thread) {
    const elsewhere = !onThisURL(thread);
    const located = elsewhere || !thread.anchor.selector || locate(thread) !== null;
    const status = { draft: "Not sent", sent: "Sent", resolved: "Resolved" }[thread.status];
    const replying = state.replying === thread.id;
    const off = state.offline ? "disabled" : "";
    return `<li class="card ${thread.status}" data-id="${thread.id}" tabindex="0">
      <div class="card-head">
        <span class="number">${numberOf(thread)}</span>
        <span class="intent ${thread.intent}">${escapeHTML(thread.intent)}</span>
        <span class="status">${status}</span>
        ${thread.anchor.selector ? "" : `<span class="muted">whole page</span>`}
      </div>
      ${isApp ? `<p class="where">${elsewhere ? `<a href="${escapeHTML(thread.anchor.url)}">${escapeHTML(pathOf(thread))}</a>` : escapeHTML(pathOf(thread))}</p>` : ""}
      ${located ? "" : `<p class="detached">The element is gone from this version of the page.</p>`}
      ${thread.messages.map((m) => `<div class="message ${m.by === "you" ? "mine" : "theirs"}"><span class="by">${escapeHTML(m.by)}</span><p>${escapeHTML(m.text)}</p></div>`).join("")}
      ${replying ? `<form class="reply"><textarea rows="3" placeholder="Reply" aria-label="Reply"></textarea><div><button type="button" class="ghost" data-action="cancel-reply">Cancel</button><button class="primary" ${off}>Reply</button></div></form>` : ""}
      <div class="card-actions">
        ${thread.status === "draft" ? `<button class="ghost" data-action="edit" ${off}>Edit</button><button class="ghost danger" data-action="delete" ${off}>Delete</button>` : ""}
        ${thread.status !== "draft" && !replying ? `<button class="ghost" data-action="reply" ${off}>Reply</button>` : ""}
        ${thread.status === "sent" ? `<button class="ghost" data-action="resolve" ${off}>Resolve</button>` : ""}
      </div>
    </li>`;
  }

  function wireCard(element) {
    const thread = state.threads.find((t) => t.id === element.dataset.id);
    if (!thread) return;
    const on = (action, handler) => {
      const button = element.querySelector(`[data-action="${action}"]`);
      if (button) button.onclick = (e) => {
        e.stopPropagation();
        handler();
      };
    };
    on("edit", () => openComposer({ thread, element: locate(thread) }));
    on("delete", () => deleteThread(thread));
    on("resolve", () => change(thread, { status: "resolved" }));
    on("reply", () => {
      state.replying = thread.id;
      renderPanel();
      const textarea = ui.panel.querySelector(`.card[data-id="${thread.id}"] textarea`);
      if (textarea) textarea.focus();
    });
    on("cancel-reply", () => {
      state.replying = null;
      renderPanel();
      reloadIfPending();
    });
    const form = element.querySelector(".reply");
    if (form) {
      const send = async () => {
        const text = form.querySelector("textarea").value.trim();
        if (!text) return;
        try {
          await api("POST", `/threads/${thread.id}/replies`, { text });
          state.replying = null;
          await load();
          reloadIfPending();
        } catch (error) {
          toast("Reply not saved: " + error.message);
        }
      };
      form.onsubmit = (e) => {
        e.preventDefault();
        send();
      };
      form.querySelector("textarea").onkeydown = (e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          send();
        }
      };
    }
    element.onclick = () => revealThread(thread);
    element.onkeydown = (e) => {
      if (e.key === "Enter" && e.target === element) revealThread(thread);
    };
  }

  async function change(thread, body) {
    try {
      await api("PATCH", "/threads/" + thread.id, body);
      await load();
    } catch (error) {
      toast("Not changed: " + error.message);
    }
  }

  function revealThread(thread) {
    const element = locate(thread);
    if (!element) return;
    element.scrollIntoView({ behavior: "smooth", block: "center" });
    setTimeout(() => {
      showHighlight(element, "#" + numberOf(thread));
      setTimeout(() => {
        if (!state.composer && state.mode !== "pick") ui.highlight.hidden = true;
      }, 1200);
    }, 350);
  }

  function focusCard(thread) {
    const element = ui.panel.querySelector(`.card[data-id="${thread.id}"]`);
    if (!element) return;
    element.scrollIntoView({ block: "nearest" });
    element.classList.add("flash");
    setTimeout(() => element.classList.remove("flash"), 900);
    element.focus();
  }

  let toastTimer;
  function toast(text) {
    ui.toast.textContent = text;
    ui.toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (ui.toast.hidden = true), 4000);
  }

  function clamp(value, low, high) {
    return Math.max(low, Math.min(high, value));
  }

  function escapeHTML(text) {
    return String(text).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  }

  function icon(name) {
    const paths = {
      comment: '<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4H6.5A2.5 2.5 0 0 1 4 13.5z"/>',
      list: '<path d="M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01"/>',
      close: '<path d="M6 6l12 12M18 6L6 18"/>',
      send: '<path d="M4 12l16-8-6 16-2.5-6.5z"/>',
      check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
    };
    return `<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name]}</svg>`;
  }

  function styles() {
    const surfaces = ".highlight, .pins, .chrome";
    const light = `--bg: #ffffff; --raised: #f5f5f4; --fg: #1c1917; --muted: #78716c; --line: #e7e5e4;
        --accent: #4f46e5; --accent-fg: #ffffff; --danger: #dc2626; --fix: #dc2626; --change: #d97706; --question: #2563eb; --ok: #16a34a; --warn: #b45309;
        --shadow: 0 8px 30px rgba(0,0,0,.14), 0 1px 3px rgba(0,0,0,.08);`;
    const stone = `--bg: #1c1917; --raised: #292524; --fg: #f5f5f4; --muted: #a8a29e; --line: #3a3532;
        --accent: #818cf8; --accent-fg: #0c0a09; --danger: #f87171; --fix: #f87171; --change: #fbbf24; --question: #60a5fa; --ok: #4ade80; --warn: #fbbf24;
        --shadow: 0 8px 30px rgba(0,0,0,.5), 0 1px 3px rgba(0,0,0,.4);`;
    const factory = `--bg: #1a1b26; --raised: #1f2335; --fg: #c0caf5; --muted: #828bb8; --line: #3b4261; --top: #16161e;
        --accent: #7aa2f7; --accent-fg: #15161e; --danger: #f7768e; --fix: #f7768e; --change: #ff9e64; --question: #7dcfff; --ok: #9ece6a; --warn: #e0af68;
        --shadow: 0 -18px 40px -20px rgba(0,0,0,.9);
        font-family: ui-monospace, SFMono-Regular, Menlo, monospace;`;
    const tokens = config.theme === "dark" ? `${surfaces} { ${factory} }` : `${surfaces} { ${light} } @media (prefers-color-scheme: dark) { ${surfaces} { ${stone} } }`;
    return `
      :host { color-scheme: ${config.theme === "dark" ? "dark" : "light dark"}; }
      * { box-sizing: border-box; }
      [hidden] { display: none !important; }
      ${surfaces} {
        font: 13px/1.45 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
        color: var(--fg);
        -webkit-font-smoothing: antialiased;
      }
      ${tokens}
      .chrome { display: contents; }
      .buttons { display: contents; }
      .grab { display: none; }
      .chrome.mobile { display: block; position: fixed; left: 0; top: 0; transform-origin: 0 0; pointer-events: none; }
      .pins.mobile .pin, .highlight.mobile .highlight-label { transform: scale(var(--ui-scale, 1)); }
      .highlight.mobile .highlight-label { transform-origin: 0 100%; }
      .top { position: fixed; left: 0; right: 0; top: 0; display: flex; align-items: stretch; height: calc(45px + env(safe-area-inset-top)); padding-top: env(safe-area-inset-top); background: var(--top); border-bottom: 1px solid var(--line); font-size: 14px; line-height: 44px; pointer-events: auto; }
      .top .back { display: inline-flex; align-items: center; justify-content: center; min-width: 44px; padding: 0 12px; background: var(--accent); color: var(--accent-fg); font-size: 20px; font-weight: 700; text-decoration: none; }
      .top .sect { display: flex; min-width: 0; padding: 0 12px; background: var(--line); color: var(--fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
      .top .dim { color: var(--muted); padding-right: 1ch; overflow: hidden; text-overflow: ellipsis; }
      .top .where { margin-left: auto; display: inline-flex; gap: 7px; padding: 0 14px; white-space: nowrap; }
      .top .flag { color: var(--change); }
      .bar-note.warn { color: var(--warn); }
      .bar-note .key { color: var(--accent); font-weight: 700; margin-right: 8px; }
      button[disabled] { opacity: .42; cursor: not-allowed; }
      .phone .bar { left: 0; right: 0; bottom: 0; max-width: none; display: grid; gap: 8px; padding: 8px 10px calc(8px + env(safe-area-inset-bottom)); border-radius: 0; border-width: 1px 0 0; background: rgba(22,22,30,.97); box-shadow: none; }
      .phone .buttons { display: flex; gap: 6px; }
      .phone .bar-button { flex: 1 1 auto; justify-content: center; min-height: 44px; padding: 0 10px; border-radius: 6px; border: 1px solid var(--line); background: var(--raised); font-size: 13px; }
      .phone .bar-button.fit { flex: 0 0 auto; }
      .phone .bar-button svg { display: none; }
      .phone .bar-button.primary-pill { background: var(--accent); border-color: var(--accent); font-weight: 700; }
      .phone .approve { background: var(--ok); border-color: var(--ok); color: var(--accent-fg); font-weight: 700; }
      .phone .bar-note { margin: 0; padding: 0; font-size: 12.5px; line-height: 1.45; color: var(--fg); }
      .phone .panel, .phone .composer { left: 0; right: 0; top: auto; width: auto; border-radius: 14px 14px 0 0; border-width: 1px 0 0; box-shadow: var(--shadow); }
      .phone .panel { bottom: var(--bar-h, 61px); max-height: calc(100% - var(--bar-h, 61px) - 56px); }
      .phone .composer { z-index: 1; bottom: 0; padding: 8px 16px calc(14px + env(safe-area-inset-bottom)); gap: 12px; max-height: calc(100% - 56px); overflow: auto; }
      .phone .grab { display: block; width: 36px; height: 5px; border-radius: 3px; background: var(--line); justify-self: center; align-self: center; }
      .phone .chip, .phone .ghost, .phone .primary, .phone .icon-button { min-height: 44px; min-width: 44px; }
      .phone .chip { padding: 0 16px; border-radius: 22px; }
      .phone textarea { font-size: 16px; }
      .phone .toast { top: calc(env(safe-area-inset-top) + 56px); bottom: auto; left: 16px; right: 16px; transform: none; max-width: none; }
      button { font: inherit; color: inherit; cursor: pointer; border: 0; background: none; }
      button:focus-visible, textarea:focus-visible, .card:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
      kbd { font: 11px ui-monospace, monospace; padding: 1px 5px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); }
      .muted { color: var(--muted); }
      .highlight { position: fixed; border: 2px solid var(--accent); background: color-mix(in srgb, var(--accent) 10%, transparent); border-radius: 4px; pointer-events: none; transition: all 60ms ease-out; }
      .highlight-label { position: absolute; left: -2px; bottom: 100%; margin-bottom: 4px; padding: 2px 6px; border-radius: 4px; background: var(--accent); color: var(--accent-fg); font-size: 11px; font-weight: 600; white-space: nowrap; }
      .highlight-label.below { bottom: auto; top: 100%; margin: 4px 0 0; }
      .pin { position: fixed; width: 24px; height: 24px; border-radius: 12px 12px 12px 3px; pointer-events: auto; background: var(--accent); color: var(--accent-fg); font-size: 11px; font-weight: 700; box-shadow: var(--shadow); transition: transform 120ms ease-out; }
      .pin:hover { transform: scale(1.12); }
      .pin.sent { background: var(--change); }
      .pin.resolved { background: var(--ok); opacity: .7; }
      .bar { position: fixed; right: 16px; bottom: 16px; display: flex; gap: 4px; align-items: center; padding: 4px; border-radius: 999px; background: var(--bg); border: 1px solid var(--line); box-shadow: var(--shadow); pointer-events: auto; max-width: calc(100vw - 32px); flex-wrap: wrap; }
      .bar-button { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border-radius: 999px; font-weight: 500; }
      .bar-button:hover { background: var(--raised); }
      .bar-button.active { background: var(--accent); color: var(--accent-fg); }
      .bar-button.active kbd { color: inherit; border-color: currentColor; }
      .bar-note { padding: 0 10px; color: var(--muted); font-size: 12px; }
      .primary-pill { background: var(--accent); color: var(--accent-fg); }
      .primary-pill:hover { background: var(--accent); filter: brightness(1.08); }
      .primary-pill[disabled] { opacity: .6; cursor: progress; }
      .approve { color: var(--ok); }
      .approve.confirm, .phone .approve.confirm { background: var(--change); border-color: var(--change); color: var(--accent-fg); }
      .badge { padding: 1px 7px; border-radius: 999px; background: color-mix(in srgb, var(--change) 18%, transparent); color: var(--change); font-size: 11px; font-weight: 600; }
      .composer { position: fixed; display: flex; flex-direction: column; gap: 10px; padding: 12px; border-radius: 12px; background: var(--bg); border: 1px solid var(--line); box-shadow: var(--shadow); pointer-events: auto; }
      .composer header { display: flex; justify-content: space-between; gap: 8px; align-items: baseline; }
      .composer header .muted { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
      .composer-title { font-weight: 600; }
      .intents { display: flex; gap: 6px; }
      .chip { padding: 4px 10px; border-radius: 999px; border: 1px solid var(--line); font-size: 12px; }
      .chip[aria-checked="true"] { background: var(--fg); color: var(--bg); border-color: var(--fg); }
      textarea { width: 100%; resize: vertical; min-height: 72px; padding: 8px 10px; border-radius: 8px; border: 1px solid var(--line); background: var(--raised); color: var(--fg); font: inherit; }
      footer, .reply div { display: flex; align-items: center; gap: 6px; justify-content: flex-end; }
      footer .muted { margin-right: auto; font-size: 11px; }
      .primary { padding: 6px 14px; border-radius: 8px; background: var(--accent); color: var(--accent-fg); font-weight: 600; }
      .primary:hover { filter: brightness(1.08); }
      .ghost { display: inline-flex; align-items: center; gap: 6px; padding: 5px 10px; border-radius: 8px; color: var(--muted); }
      .ghost:hover { background: var(--raised); color: var(--fg); }
      .danger:hover { color: var(--danger); }
      .panel { position: fixed; top: 16px; right: 16px; bottom: 68px; width: min(380px, calc(100vw - 32px)); display: flex; flex-direction: column; border-radius: 14px; background: var(--bg); border: 1px solid var(--line); box-shadow: var(--shadow); pointer-events: auto; overflow: hidden; }
      .panel header { display: flex; justify-content: space-between; align-items: center; padding: 12px 12px 12px 16px; border-bottom: 1px solid var(--line); }
      .icon-button { display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; color: var(--muted); }
      .icon-button:hover { background: var(--raised); color: var(--fg); }
      .panel-actions { display: flex; justify-content: space-between; align-items: center; gap: 8px; padding: 8px 8px 8px 6px; border-bottom: 1px solid var(--line); flex-wrap: wrap; }
      .toggle { display: inline-flex; gap: 6px; align-items: center; color: var(--muted); font-size: 12px; padding-right: 8px; }
      .cards { list-style: none; margin: 0; padding: 8px; overflow-y: auto; flex: 1; display: flex; flex-direction: column; gap: 8px; }
      .empty { padding: 32px 16px; text-align: center; color: var(--muted); }
      .card { padding: 10px 12px; border-radius: 10px; border: 1px solid var(--line); background: var(--bg); cursor: pointer; transition: border-color 120ms; }
      .card:hover { border-color: var(--muted); }
      .card.resolved { opacity: .65; }
      .card.flash { border-color: var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 25%, transparent); }
      .card-head { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
      .number { display: grid; place-items: center; min-width: 20px; height: 20px; padding: 0 5px; border-radius: 10px; background: var(--raised); font-size: 11px; font-weight: 700; }
      .intent { font-size: 11px; font-weight: 600; text-transform: capitalize; }
      .intent.fix { color: var(--fix); } .intent.change { color: var(--change); } .intent.question { color: var(--question); }
      .status { margin-left: auto; font-size: 11px; color: var(--muted); }
      .where { margin: 0 0 6px; font: 11px ui-monospace, monospace; color: var(--muted); overflow-wrap: anywhere; }
      .where a { color: var(--accent); }
      .detached { margin: 0 0 6px; padding: 6px 8px; border-radius: 6px; background: var(--raised); color: var(--muted); font-size: 12px; }
      .message { margin-top: 6px; }
      .message .by { font-size: 11px; font-weight: 600; color: var(--muted); text-transform: capitalize; }
      .message p { margin: 2px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; }
      .message.theirs { padding-left: 10px; border-left: 2px solid var(--accent); }
      .reply { margin-top: 8px; display: flex; flex-direction: column; gap: 6px; }
      .card-actions { display: flex; gap: 2px; margin: 6px -6px -4px; }
      .card-actions:empty { display: none; }
      .toast { position: fixed; left: 50%; bottom: 72px; transform: translateX(-50%); padding: 8px 14px; border-radius: 8px; background: var(--fg); color: var(--bg); box-shadow: var(--shadow); pointer-events: auto; max-width: calc(100vw - 32px); }
      @media (prefers-reduced-motion: reduce) { * { transition: none !important; } }
    `;
  }
}
