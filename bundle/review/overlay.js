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
  const isApp = config.file === "app";
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
  };

  function remember(name, value) {
    try {
      if (value === undefined) return sessionStorage.getItem(sessionKey(name));
      if (value === null) sessionStorage.removeItem(sessionKey(name));
      else sessionStorage.setItem(sessionKey(name), value);
    } catch {}
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
    const response = await fetch(config.api + path, {
      method,
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
    return response.json();
  }

  const host = document.createElement("factory-review");
  host.style.cssText = "all:initial;position:fixed;inset:0;pointer-events:none;z-index:2147483647;";
  const root = host.attachShadow({ mode: "open" });
  root.innerHTML = `<style>${styles()}</style>
    <div class="highlight" hidden><span class="highlight-label"></span></div>
    <div class="pins"></div>
    <aside class="panel" hidden aria-label="Review comments"></aside>
    <form class="composer" hidden></form>
    <div class="bar" role="toolbar" aria-label="Factory review"></div>
    <div class="toast" role="status" hidden></div>`;
  const $ = (selector) => root.querySelector(selector);
  const ui = {
    highlight: $(".highlight"),
    highlightLabel: $(".highlight-label"),
    pins: $(".pins"),
    composer: $(".composer"),
    panel: $(".panel"),
    bar: $(".bar"),
    toast: $(".toast"),
  };
  document.documentElement.appendChild(host);

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
      await api("POST", "/approve", { page: config.file });
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
    if (!config.events) return;
    const events = new EventSource(config.events);
    let reloadTimer;
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
    const { open, drafts } = counts();
    const picking = state.mode === "pick";
    ui.bar.innerHTML = `
      <button class="bar-button ${picking ? "active" : ""}" data-action="pick" aria-pressed="${picking}" title="Comment on an element (C)">
        ${icon("comment")}<span>${picking ? "Click an element" : "Comment"}</span><kbd>C</kbd>
      </button>
      <button class="bar-button" data-action="panel" aria-expanded="${state.panelOpen}" title="Show comments">
        ${icon("list")}<span>${open} open</span>${drafts ? `<span class="badge">${drafts} not sent</span>` : ""}
      </button>
      ${state.pendingReload ? `<span class="bar-note">New version waiting · finish your comment</span>` : ""}
      ${drafts ? `<button class="bar-button primary-pill" data-action="send" ${state.sending ? "disabled" : ""} title="Send the comments that are not sent yet${state.status.owner ? " to the " + state.status.owner : ""}">${icon("send")}<span>${state.sending ? "Sending…" : `Send ${drafts}`}</span></button>` : ""}
      ${state.status.gate ? `<button class="bar-button approve ${state.confirmApprove ? "confirm" : ""}" data-action="approve" title="Approve this stage's gate">${icon("check")}<span>${state.confirmApprove ? "Click again to approve" : "Approve"}</span></button>` : ""}`;
    ui.bar.querySelector('[data-action="pick"]').onclick = () => (state.mode === "pick" ? stopPicking() : startPicking());
    ui.bar.querySelector('[data-action="panel"]').onclick = () => togglePanel();
    const sendButton = ui.bar.querySelector('[data-action="send"]');
    if (sendButton) sendButton.onclick = send;
    const approveButton = ui.bar.querySelector('[data-action="approve"]');
    if (approveButton) approveButton.onclick = approve;
  }

  function togglePanel(open = !state.panelOpen) {
    state.panelOpen = open;
    remember("panel", open ? "1" : "0");
    renderBar();
    renderPanel();
  }

  function startPicking() {
    closeComposer();
    state.mode = "pick";
    document.documentElement.classList.add("factory-review-picking");
    for (const type of ["mousemove", "click", "mousedown", "mouseup", "pointerdown", "pointerup"]) {
      addEventListener(type, onPickEvent, true);
    }
    renderBar();
  }

  function stopPicking() {
    state.mode = "idle";
    state.hovered = null;
    ui.highlight.hidden = true;
    document.documentElement.classList.remove("factory-review-picking");
    for (const type of ["mousemove", "click", "mousedown", "mouseup", "pointerdown", "pointerup"]) {
      removeEventListener(type, onPickEvent, true);
    }
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
    state.composer = { element, thread, intent: thread ? thread.intent : "fix" };
    const editing = thread !== null;
    const text = editing ? thread.messages[thread.messages.length - 1].text : "";
    const target = element ? describe(element) : thread && thread.anchor.selector ? "element" : "the whole page";
    ui.composer.innerHTML = `
      <header><span class="composer-title">${editing ? "Edit comment" : "New comment"}</span><span class="muted">${escapeHTML(target)}</span></header>
      <div class="intents" role="radiogroup" aria-label="Kind of comment">
        ${intents.map(([value, label]) => `<button type="button" role="radio" class="chip" data-intent="${value}" aria-checked="${value === state.composer.intent}">${label}</button>`).join("")}
      </div>
      <textarea rows="4" placeholder="What should change?" aria-label="Comment">${escapeHTML(text)}</textarea>
      <footer>
        <span class="muted">⌘↵ save · Esc cancel</span>
        ${editing ? `<button type="button" class="ghost danger" data-action="delete">Delete</button>` : ""}
        <button type="button" class="ghost" data-action="cancel">Cancel</button>
        <button type="submit" class="primary">Save</button>
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
        await api("POST", "/threads", { page: config.file, intent: composer.intent, text, anchor: anchorFor(composer.element) });
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
        <button class="ghost" data-action="page-comment">${icon("comment")} Comment on the whole page</button>
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
      ${replying ? `<form class="reply"><textarea rows="3" placeholder="Reply" aria-label="Reply"></textarea><div><button type="button" class="ghost" data-action="cancel-reply">Cancel</button><button class="primary">Reply</button></div></form>` : ""}
      <div class="card-actions">
        ${thread.status === "draft" ? `<button class="ghost" data-action="edit">Edit</button><button class="ghost danger" data-action="delete">Delete</button>` : ""}
        ${thread.status !== "draft" && !replying ? `<button class="ghost" data-action="reply">Reply</button>` : ""}
        ${thread.status === "sent" ? `<button class="ghost" data-action="resolve">Resolve</button>` : ""}
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
    return `
      :host { color-scheme: light dark; }
      * { box-sizing: border-box; }
      [hidden] { display: none !important; }
      .highlight, .pins, .composer, .panel, .bar, .toast {
        --bg: #ffffff; --raised: #f5f5f4; --fg: #1c1917; --muted: #78716c; --line: #e7e5e4;
        --accent: #4f46e5; --accent-fg: #ffffff; --danger: #dc2626; --fix: #dc2626; --change: #d97706; --question: #2563eb; --ok: #16a34a;
        --shadow: 0 8px 30px rgba(0,0,0,.14), 0 1px 3px rgba(0,0,0,.08);
        font: 13px/1.45 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
        color: var(--fg);
        -webkit-font-smoothing: antialiased;
      }
      @media (prefers-color-scheme: dark) {
        .highlight, .pins, .composer, .panel, .bar, .toast {
          --bg: #1c1917; --raised: #292524; --fg: #f5f5f4; --muted: #a8a29e; --line: #3a3532;
          --accent: #818cf8; --accent-fg: #0c0a09; --danger: #f87171; --fix: #f87171; --change: #fbbf24; --question: #60a5fa; --ok: #4ade80;
          --shadow: 0 8px 30px rgba(0,0,0,.5), 0 1px 3px rgba(0,0,0,.4);
        }
      }
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
      .approve.confirm { background: var(--ok); color: var(--accent-fg); }
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
