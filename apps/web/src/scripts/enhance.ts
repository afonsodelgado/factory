import {
  DEFAULT_PLATFORM,
  PROMPT_HTML,
  commandLines,
  commandText,
  isPlatform,
  type CommandId,
  type Platform,
} from "../lib/commands.ts";
import { IC, esc } from "../lib/frames.ts";

const TYPE_DELAY_MS = 500;
const CHAR_MS = 22;
const SPACE_MS = 30;
const NEWLINE_MS = 260;
const PROMPT_MS = 120;
const SWEEP_MS = 380;
const COPIED_MS = 1500;
const REFUSED_MS = 4000;

const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
const still = () => motion.matches;
const live = document.querySelector<HTMLElement>("[data-live]");
let platform: Platform = DEFAULT_PLATFORM;
let finishTyping = () => {};

function attempt(feature: () => void) {
  try {
    feature();
  } catch (error) {
    console.error(error);
  }
}

function announce(message: string) {
  if (live) live.textContent = message;
}

function setupPlatforms() {
  const group = document.querySelector<HTMLElement>(".plat");
  const name = document.querySelector<HTMLElement>(".plat-name");
  if (!group || !name) return;
  const buttons = Array.from(
    group.querySelectorAll<HTMLButtonElement>("button"),
  );
  for (const button of buttons) {
    button.addEventListener("click", () => {
      const chosen = button.dataset.plat;
      if (!isPlatform(chosen)) return;
      platform = chosen;
      name.textContent = chosen;
      for (const b of buttons)
        b.setAttribute("aria-pressed", String(b === button));
    });
  }
  group.hidden = false;
  document.querySelector<HTMLElement>(".nojs-only")?.setAttribute("hidden", "");
}

function selectCommand(code: Element) {
  const range = document.createRange();
  range.selectNodeContents(code);
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
}

function setupCopy() {
  const glyph = (cp: number) => String.fromCodePoint(cp);
  for (const button of document.querySelectorAll<HTMLButtonElement>(
    "button.copy",
  )) {
    const id = button.dataset.copy as CommandId;
    const code = document.querySelector(`[data-code="${id}"]`);
    const icon = button.querySelector<HTMLElement>(".ic");
    const label = button.querySelector<HTMLElement>(".copy-label");
    if (!code || !icon || !label) continue;
    let reset: ReturnType<typeof setTimeout> | undefined;

    const settle = (
      state: string,
      cp: number,
      words: string,
      said: string,
      ms: number,
    ) => {
      button.dataset.state = state;
      icon.dataset.n = glyph(cp);
      label.textContent = words;
      announce(said);
      clearTimeout(reset);
      reset = setTimeout(() => {
        button.dataset.state = "idle";
        icon.dataset.n = glyph(IC.copy);
        label.textContent = "copy";
      }, ms);
    };

    const refuse = () => {
      if (id === "hero") finishTyping();
      selectCommand(code);
      settle(
        "failed",
        IC.warn,
        "press ⌘C",
        "The browser blocked copying. The command is selected: press Command C or Control C.",
        REFUSED_MS,
      );
    };

    button.addEventListener("click", () => {
      const text = commandText(commandLines(id, platform));
      try {
        navigator.clipboard
          .writeText(text)
          .then(
            () =>
              settle(
                "copied",
                IC.pass,
                "copied",
                "Copied to the clipboard.",
                COPIED_MS,
              ),
            refuse,
          );
      } catch {
        refuse();
      }
    });
    button.hidden = false;
  }
}

function setupTyping() {
  const code = document.querySelector<HTMLElement>('[data-code="hero"]');
  const cursor = document.querySelector<HTMLElement>("[data-cursor]");
  if (!code || !cursor) return;
  const full = code.innerHTML;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let typing = false;

  finishTyping = () => {
    clearTimeout(timer);
    if (typing) code.innerHTML = full;
    typing = false;
    cursor.hidden = false;
    cursor.classList.toggle("blink", !still());
  };

  const tokens: { html: string; pause: number }[] = [];
  const walk = (node: Node, wrap: string) => {
    for (const child of Array.from(node.childNodes)) {
      if (child instanceof HTMLElement && child.classList.contains("pr")) {
        tokens.push({ html: PROMPT_HTML, pause: PROMPT_MS });
      } else if (child instanceof HTMLElement) {
        walk(child, child.className);
      } else {
        for (const ch of child.textContent ?? "") {
          const html = wrap
            ? `<span class="${wrap}">${esc(ch)}</span>`
            : esc(ch);
          const pause =
            ch === "\n" ? NEWLINE_MS : ch === " " ? SPACE_MS : CHAR_MS;
          tokens.push({ html, pause });
        }
      }
    }
  };
  walk(code, "");

  const paint = (n: number) => {
    const typed = tokens
      .slice(0, n)
      .map((t) => t.html)
      .join("");
    const rest = tokens
      .slice(n)
      .map((t) => t.html)
      .join("");
    code.innerHTML = `${typed}<span class="cursor"></span><span class="ghost">${rest}</span>`;
  };

  const type = () => {
    if (still()) return finishTyping();
    typing = true;
    cursor.hidden = true;
    let i = 0;
    paint(0);
    const step = () => {
      if (i >= tokens.length) return finishTyping();
      const token = tokens[i++]!;
      paint(i);
      timer = setTimeout(step, token.pause);
    };
    timer = setTimeout(step, TYPE_DELAY_MS);
  };

  motion.addEventListener("change", finishTyping);
  type();
}

function setupPipeline() {
  const section = document.getElementById("pipeline");
  const stages = Array.from(
    document.querySelectorAll<HTMLElement>("[data-stages] .st"),
  );
  const dots = Array.from(
    document.querySelectorAll<HTMLElement>("[data-track] i"),
  );
  if (!section || !stages.length) return null;
  const last = stages.length - 1;
  let timer: ReturnType<typeof setInterval> | undefined;

  const paint = (current: number) => {
    const state = (j: number) =>
      j < current ? "done" : j === current ? "cur" : "todo";
    const mark = { done: "●", cur: "◉", todo: "○" };
    const color = { done: "c-dk", cur: "c-grn", todo: "c-d3" };
    stages.forEach((li, j) => {
      li.dataset.s = state(j);
      const dot = li.querySelector(".dot");
      if (dot) dot.textContent = mark[state(j)];
    });
    dots.forEach((dot, j) => {
      dot.textContent = mark[state(j)];
      dot.className = color[state(j)];
    });
  };

  const finish = () => {
    clearInterval(timer);
    paint(last);
  };

  const sweep = () => {
    if (still()) return finish();
    let current = 0;
    paint(current);
    clearInterval(timer);
    timer = setInterval(() => {
      current++;
      paint(current);
      if (current >= last) clearInterval(timer);
    }, SWEEP_MS);
  };

  motion.addEventListener("change", () => {
    if (still()) finish();
  });
  return { section, sweep };
}

function setupReveals(
  pipeline: { section: HTMLElement; sweep: () => void } | null,
) {
  if (still()) return;
  const root = document.documentElement;
  const sections = Array.from(
    document.querySelectorAll<HTMLElement>(".reveal"),
  );
  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.add("in");
        observer.unobserve(entry.target);
        if (entry.target === pipeline?.section) pipeline.sweep();
      }
    },
    { threshold: 0.12 },
  );
  for (const section of sections) observer.observe(section);
  root.dataset.reveal = "";

  motion.addEventListener("change", () => {
    if (!still()) return;
    observer.disconnect();
    for (const section of sections) section.classList.add("in");
    delete root.dataset.reveal;
  });
}

const SECTION_NAMES: Record<string, string> = {
  what: "what it is",
  pipeline: "pipeline",
  agents: "agents",
  hub: "the hub",
  keys: "keys",
  install: "install",
};

function setupNav() {
  const sect = document.querySelector<HTMLElement>("[data-nav-sect]");
  const wins = document.querySelector<HTMLElement>(".wins");
  if (!sect || !wins) return;
  const links = Array.from(wins.querySelectorAll<HTMLAnchorElement>("a"));
  const spy = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        const id = entry.target.id;
        sect.textContent = id === "top" ? "~/factory" : `~/factory/${id}`;
        sect.title = SECTION_NAMES[id] ?? "";
        for (const link of links) {
          if (link.getAttribute("href") !== `#${id}`) {
            link.removeAttribute("aria-current");
            continue;
          }
          link.setAttribute("aria-current", "true");
          const overflowing = wins.scrollWidth > wins.clientWidth;
          if (overflowing)
            wins.scrollLeft = link.offsetLeft - wins.offsetLeft - 8;
        }
      }
    },
    { rootMargin: "-45% 0px -50% 0px" },
  );
  for (const id of ["top", ...links.map((l) => l.hash.slice(1))]) {
    const target = document.getElementById(id);
    if (target) spy.observe(target);
  }
}

attempt(setupPlatforms);
attempt(setupCopy);
attempt(setupTyping);
attempt(setupNav);
attempt(() => setupReveals(setupPipeline()));
