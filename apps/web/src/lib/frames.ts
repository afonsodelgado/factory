import {
  AGENTS,
  ANSWER,
  REPOS,
  TOOLS,
  findFactory,
  type DemoState,
  type Factory,
  type Focus,
  type Status,
} from "./demo.ts";

export const IC = {
  app: 0xf275,
  repo: 0xf401,
  starting: 0xf051f,
  running: 0xf144,
  needs: 0xf009e,
  ci: 0xf252,
  agent: 0xf06a9,
  stalled: 0xf04b2,
  done: 0xf058,
  question: 0xf059,
  pr: 0xf407,
  file: 0xf0f6,
  hist: 0xf1da,
  pass: 0xf00c,
  pending: 0xf1ce,
  gh: 0xf09b,
  branch: 0xf418,
  archive: 0xf187,
  copy: 0xf0c5,
  warn: 0xf071,
  fail: 0xf00d,
  term: 0xf120,
  key: 0xf084,
} as const;

export type IconName = keyof typeof IC;

export const COLS = {
  hero: 38,
  agents: 46,
  hubWide: 106,
  hubNarrow: 56,
} as const;
export const ROWS = { hero: 22, agents: 14, hub: 28 } as const;

const STATUS: Record<Status, [icon: IconName, color: string, word: string]> = {
  running: ["running", "grn", "running"],
  starting: ["starting", "cyn", "starting"],
  needs: ["needs", "org", "needs you"],
  ci: ["ci", "mag", "on CI"],
  agent: ["agent", "mag", "on agent"],
  stalled: ["stalled", "yel", "stalled"],
  done: ["done", "dk", "done"],
};

const TRACK: Record<string, [string, string]> = {
  d: ["●", "dk"],
  s: ["–", "gt"],
  f: ["✕", "red b"],
  o: ["○", "d3"],
};

const SPIN = ["·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"];

const HINTS: Record<Focus, [string, string][]> = {
  FACTORIES: [
    ["j/k", "move"],
    ["⏎", "session"],
    ["i", "answer"],
    ["g", "next needing you"],
    ["/", "find"],
    ["1-9", "jump"],
    ["n", "new"],
    ["w", "wake"],
    ["p", "pause"],
  ],
  PANEL: [
    ["ctrl+s", "send"],
    ["ctrl+n/p", "item"],
    ["tab", "next tab"],
    ["esc", "back"],
  ],
};

const MODE_BG: Record<Focus, string> = {
  FACTORIES: "on-blu",
  PANEL: "on-mag",
};

type Part = [text: string, classes: string];
type Line = Part[] & { bg?: string };

interface Cell {
  t: string;
  c: string;
  ic?: number;
}

const cls = (c: string) => c.trim().replace(/\s+/g, " ");
const len = (s: string) => Array.from(s).length;
export const esc = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const plain = (ch: string) => {
  const cp = ch.codePointAt(0)!;
  return cp < 0x7f || (cp >= 0x2500 && cp <= 0x257f);
};

export class Grid {
  readonly w: number;
  readonly h: number;
  readonly rows: Cell[][];

  constructor(w: number, h: number) {
    this.w = w;
    this.h = h;
    this.rows = Array.from({ length: h }, () =>
      Array.from({ length: w }, () => ({ t: " ", c: "" })),
    );
  }

  inside(x: number, y: number) {
    return y >= 0 && y < this.h && x >= 0 && x < this.w;
  }

  put(x: number, y: number, text: string, c: string, max?: number) {
    let chars = Array.from(text);
    if (max !== undefined) {
      if (max <= 0) return x;
      if (chars.length > max) chars = chars.slice(0, max - 1).concat("…");
    }
    const k = cls(c);
    for (const ch of chars) {
      if (this.inside(x, y)) this.rows[y]![x] = { t: ch, c: k };
      x++;
    }
    return x;
  }

  icon(x: number, y: number, name: IconName, c: string) {
    const k = cls(c);
    if (this.inside(x, y)) this.rows[y]![x] = { t: "", ic: IC[name], c: k };
    if (this.inside(x + 1, y)) this.rows[y]![x + 1] = { t: " ", c: k };
    return x + 2;
  }

  fill(x: number, y: number, w: number, h: number, c: string) {
    for (let j = y; j < y + h; j++) {
      for (let i = x; i < x + w; i++) {
        if (this.inside(i, j)) this.rows[j]![i] = { t: " ", c: cls(c) };
      }
    }
  }

  html() {
    return this.rows.map(rowHtml).join("\n");
  }
}

function rowHtml(row: Cell[]) {
  let out = "";
  let run = "";
  let runC: string | null = null;
  const flush = () => {
    if (run)
      out += runC ? `<span class="${runC}">${esc(run)}</span>` : esc(run);
    run = "";
    runC = null;
  };
  for (const cell of row) {
    const suffix = cell.c ? ` ${cell.c}` : "";
    if (cell.ic !== undefined) {
      flush();
      out += `<i class="ic${suffix}" data-n="&#x${cell.ic.toString(16)};"></i>`;
      continue;
    }
    if (!plain(cell.t)) {
      flush();
      out += `<i class="g${suffix}">${esc(cell.t)}</i>`;
      continue;
    }
    if (runC !== null && cell.c !== runC) flush();
    if (runC === null) runC = cell.c;
    run += cell.t;
  }
  flush();
  return out;
}

function segs(g: Grid, x: number, y: number, parts: Part[], w: number) {
  const end = x + w;
  for (const [t, c] of parts) {
    if (x >= end) break;
    x = g.put(x, y, t, c, end - x);
  }
  return x;
}

function box(
  g: Grid,
  x0: number,
  y0: number,
  w: number,
  h: number,
  bc: string,
) {
  g.put(x0, y0, "╭" + "─".repeat(w - 2) + "╮", bc);
  for (let y = y0 + 1; y < y0 + h - 1; y++) {
    g.put(x0, y, "│", bc);
    g.put(x0 + w - 1, y, "│", bc);
  }
  g.put(x0, y0 + h - 1, "╰" + "─".repeat(w - 2) + "╯", bc);
}

function counts(
  fleet: Factory[],
): [IconName, string, number, string, boolean][] {
  const n = (...ss: Status[]) =>
    fleet.filter((f) => ss.includes(f.status)).length;
  return [
    ["needs", "org", n("needs"), "need you", true],
    ["running", "grn", n("running"), "running", false],
    ["ci", "mag", n("ci", "agent"), "waiting", false],
    ["stalled", "yel", n("stalled"), "asleep", false],
    ["starting", "cyn", n("starting"), "starting", false],
  ];
}

function statusLine(g: Grid, y: number, st: DemoState, compact: boolean) {
  let x = g.put(0, y, " ", "blk on-blu b");
  x = g.icon(x, y, "app", "blk on-blu b");
  x = g.put(x, y, "factory ", "blk on-blu b");
  for (const [icon, color, n, word, always] of counts(st.fleet)) {
    if (!n && !always) continue;
    const c = n ? color + (always ? " b" : "") : "dk";
    x += compact ? 1 : 2;
    x = g.icon(x, y, icon, c);
    x = g.put(x, y, compact ? String(n) : `${n} ${word}`, c);
  }
  if (!compact) {
    let rx = g.w - 21;
    rx = g.icon(rx, y, "gh", "cm");
    rx = g.put(rx, y, "gh 12s ago", "cm");
    g.put(rx + 2, y, "devbox", "gt");
  }
}

function factoryRow(
  g: Grid,
  ix: number,
  y: number,
  iw: number,
  f: Factory,
  sel: boolean,
  focused: boolean,
) {
  const [icon, color, word] = STATUS[f.status];
  const bg = sel ? (focused ? " on-sel" : " on-hl") : "";
  if (sel) g.fill(ix, y, iw, 2, bg);
  g.put(ix, y, sel ? "❯" : " ", "blu b" + bg);
  g.put(ix + 1, y, String(f.n), (sel ? "hi b" : "dk") + bg);
  g.icon(ix + 3, y, icon, color + bg);
  const wx = ix + iw - 1 - len(word);
  g.put(ix + 5, y, f.id, "hi b" + bg, wx - 1 - (ix + 5));
  g.put(wx, y, word, color + " b" + bg);
  let tx = ix + 5;
  for (const k of f.track) {
    const [ch, c] =
      k === "c"
        ? ["◉", color + " b"]
        : f.status === "done" && k !== "s"
          ? TRACK.d!
          : TRACK[k]!;
    tx = g.put(tx, y + 1, ch, c + bg);
  }
  const ax = ix + iw - 1 - len(f.age);
  g.put(tx + 1, y + 1, f.stage, "dk" + bg, ax - 1 - (tx + 1));
  g.put(ax, y + 1, f.age, "cm" + bg);
}

function selector(
  g: Grid,
  x0: number,
  y0: number,
  w: number,
  h: number,
  st: DemoState,
  focused: boolean,
) {
  const bc = focused ? "blu" : "gt";
  box(g, x0, y0, w, h, bc);
  const active = st.fleet.filter((f) => f.status !== "done").length;
  let x = g.put(x0 + 1, y0, " Factories ", focused ? "blu b" : "dk");
  x = g.put(x, y0, "─", bc);
  x = g.put(x, y0, ` ${active} active `, "cm");
  x = g.put(x, y0, "·", "cm");
  g.put(x, y0, ` ${st.fleet.length - active} done `, "cm", x0 + w - 1 - x);
  const ix = x0 + 1;
  const iw = w - 2;
  const last = y0 + h - 2;
  let y = y0 + 1;
  REPOS.forEach((repo, ri) => {
    if (ri) y++;
    let hx = g.icon(ix + 1, y, "repo", "blu");
    hx = g.put(hx, y, repo.name, "blu b");
    g.put(hx, y, "  " + repo.path, "cm", ix + iw - hx);
    y++;
    for (const f of st.fleet.filter(
      (f) => f.repo === repo.name && f.status !== "done",
    )) {
      factoryRow(g, ix, y, iw, f, f.id === st.sel, focused);
      y += 2;
    }
    for (const f of st.fleet.filter(
      (f) => f.repo === repo.name && f.status === "done",
    )) {
      g.icon(ix + 3, y, "done", "dk");
      g.put(ix + 5, y, f.id, "dk", iw - 11);
      g.put(ix + iw - 5, y, "done", "dk");
      y++;
    }
  });
  if (y < last) {
    let ax = g.icon(ix + 1, last, "archive", "cm");
    ax = g.put(ax, last, "1 archived ", "cm");
    ax = g.put(ax, last, "·", "cm");
    ax = g.put(ax + 1, last, "H", "cyn");
    g.put(ax + 1, last, "show", "cm");
  }
}

function selectorNarrow(g: Grid, x0: number, y0: number, st: DemoState) {
  let y = y0;
  REPOS.forEach((repo, ri) => {
    if (ri) y++;
    g.icon(x0 + 3, y, "repo", "blu");
    y++;
    for (const f of st.fleet.filter((f) => f.repo === repo.name)) {
      const [icon, color] = STATUS[f.status];
      if (f.id === st.sel) {
        g.fill(x0, y, 5, 1, "on-sel");
        g.put(x0, y, "❯", "blu b on-sel");
        g.put(x0 + 1, y, String(f.n), "hi b on-sel");
        g.icon(x0 + 3, y, icon, color + " on-sel");
      } else {
        if (f.n) g.put(x0 + 1, y, String(f.n), "dk");
        g.icon(x0 + 3, y, icon, color);
      }
      y++;
    }
  });
}

function spinner(
  st: DemoState,
  verb: string,
  base: number,
  tokens: number,
): Line {
  const t = base + st.secs;
  const m = Math.floor(t / 60);
  const s = t % 60;
  return [
    [SPIN[st.spin % SPIN.length]!, "cla"],
    [` ${verb}…`, "cla"],
    [
      ` (${m ? m + "m " : ""}${s}s · ↓ ${tokens.toFixed(1)}k tokens · esc to interrupt)`,
      "cm",
    ],
  ];
}

function sessionLines(st: DemoState, f: Factory): Line[] {
  if (f.id === "csv-export") {
    const t = (k: number) => TOOLS[(st.tool + k) % TOOLS.length]!;
    return [
      [
        ["⏺", "grn"],
        [" ", ""],
        ["factory-engineer", "hi b"],
        ["(Implement plan.md as a 3-layer stack)", ""],
      ],
      [
        ["  ⎿  ", "cm"],
        [t(0), ""],
      ],
      [
        ["     ", ""],
        [t(1), ""],
      ],
      [
        ["     ", ""],
        [t(2), ""],
      ],
      [
        ["     ", ""],
        [`+${21 + st.tool} more tool uses (ctrl+o to expand)`, "cm"],
      ],
      [],
      spinner(st, "Percolating", 24 * 60 + 10, 48.2 + st.tool * 0.7),
      [],
    ];
  }
  if (f.id === "rate-limits" && st.answered) {
    const sent: Line = Object.assign(
      [
        [">", "dk on-hl"],
        [` q-rl-1 answers: 1. recommended  2. ${ANSWER}`, "on-hl"],
      ] satisfies Part[],
      { bg: "on-hl" },
    );
    return [
      [["  2. Should the user get an email when an account locks?", ""]],
      [["     Recommended: yes, with a link to reset the password.", "dk"]],
      [],
      sent,
      [],
      [
        ["⏺", "hi"],
        [" Both answered. Writing requirements.md.", ""],
      ],
      [],
      spinner(st, "Cogitating", 3, 1.1),
    ];
  }
  if (f.id === "rate-limits") {
    const lines: Line[] = [
      [
        ["⏺", "hi"],
        [" Two questions before I write requirements.md.", ""],
      ],
      [],
      [["  1. How many failed logins before the limit kicks in?", ""]],
      [["     Recommended: 5 per 15 minutes, per account.", "dk"]],
      [["  2. Should the user get an email when an account locks?", ""]],
      [["     Recommended: yes, with a link to reset the password.", "dk"]],
      [],
      [["  Answer here or from the factory panel. Start with q-rl-1.", "cm"]],
    ];
    if (f.status === "running")
      return [lines[0]!, [], spinner(st, "Thinking", 160, 9.4)];
    return lines;
  }
  return [
    [
      ["⏺", "hi"],
      [" " + f.prompt, ""],
    ],
  ];
}

function session(
  g: Grid,
  x0: number,
  y0: number,
  w: number,
  h: number,
  st: DemoState,
  f: Factory,
) {
  const room = h - 4;
  sessionLines(st, f)
    .slice(0, room)
    .forEach((parts, i) => {
      if (parts.bg) g.fill(x0, y0 + i, w, 1, parts.bg);
      segs(g, x0, y0 + i, parts, w);
    });
  const py = y0 + h - 4;
  g.put(x0, py, "─".repeat(w), "d3");
  g.put(x0, py + 1, "❯", "hi");
  g.put(x0, py + 2, "─".repeat(w), "d3");
  segs(
    g,
    x0,
    py + 3,
    [
      ["  ", ""],
      ["⏵⏵", "mag"],
      [" auto mode on", "mag"],
      [" (shift+tab to cycle)", "cm"],
    ],
    w,
  );
}

interface PanelTab {
  key: string;
  icon: IconName;
  text: string;
}

function tabStrip(f: Factory, st: DemoState, compact: boolean): PanelTab[] {
  const tabs: [string, IconName, string, number | null][] = [
    ["needs", "needs", "Attention", st.attn],
    ["prs", "pr", "PRs", f.prs],
    ["arts", "file", "Artifacts", f.arts],
    ["hist", "hist", "Activity", null],
  ];
  return tabs.map(([key, icon, name, n]) => ({
    key,
    icon,
    text: compact
      ? n === null
        ? ""
        : String(n)
      : name + (n === null ? "" : ` ${n}`),
  }));
}

function panel(
  g: Grid,
  x0: number,
  y0: number,
  w: number,
  h: number,
  st: DemoState,
  f: Factory,
  focused: boolean,
  narrow: boolean,
) {
  const bc = focused ? "blu" : "gt";
  box(g, x0, y0, w, h, bc);
  let tabs = tabStrip(f, st, false);
  const width = (ts: PanelTab[]) =>
    ts.reduce((a, t) => a + 3 + len(t.text) + 1, 0) + ts.length - 1;
  const label = ` ${f.id} `;
  if (2 + width(tabs) + 1 + len(label) + 1 > w) tabs = tabStrip(f, st, true);
  const showLabel = 2 + width(tabs) + 1 + len(label) + 1 <= w;
  let x = x0 + 2;
  tabs.forEach((t, i) => {
    if (i) x = g.put(x, y0, "─", bc);
    const c =
      t.key === st.tab ? (focused ? "blk on-blu b" : "hi on-sel b") : "dk";
    x = g.put(x, y0, " ", c);
    x = g.icon(x, y0, t.icon, c);
    x = g.put(x, y0, t.text + " ", c);
  });
  if (showLabel) g.put(x0 + w - 1 - len(label), y0, label, "cm");
  const cx = x0 + 2;
  const cy = y0 + 1;
  const cw = w - 4;
  const ch = h - 2;
  if (st.tab === "prs") return prs(g, cx, cy, cw, ch);
  if (!st.attn && !st.sent) {
    segs(g, cx, cy + 1, [[`Nothing needs you in ${f.id}.`, "dk"]], cw);
    segs(
      g,
      cx,
      cy + 2,
      [
        ["g", "cyn b"],
        [" jumps to the next factory that does.", "cm"],
      ],
      cw,
    );
    return;
  }
  let fx = cx;
  let fw = cw;
  if (!narrow) {
    g.fill(cx, cy, 18, 2, "on-sel");
    let lx = g.put(cx, cy, "❯ ", "blu b on-sel");
    lx = g.icon(lx, cy, "question", "org on-sel");
    g.put(lx, cy, "question", "org b on-sel");
    g.put(cx + 16, cy, "3m", "cm on-sel");
    g.put(cx + 4, cy + 1, "q-rl-1", "hi on-sel");
    for (let y = cy; y < cy + ch; y++) g.put(cx + 19, y, "│", "gt");
    fx = cx + 21;
    fw = cw - 21;
  }
  let qx = g.icon(fx, cy, "question", "org");
  qx = g.put(qx, cy, "question", "org b");
  segs(
    g,
    qx,
    cy,
    [
      ["  q-rl-1", "hi b"],
      ["  from factory-analyst", "cm"],
    ],
    fx + fw - qx,
  );
  segs(
    g,
    fx,
    cy + 2,
    [
      ["1", "dk"],
      ["  Failed logins before the limit?", "hi"],
    ],
    fw,
  );
  segs(
    g,
    fx,
    cy + 3,
    [
      ["   ", ""],
      ["✓", "grn"],
      [" 5 per 15 minutes", ""],
      ["  recommended", "cm"],
    ],
    fw,
  );
  segs(
    g,
    fx,
    cy + 4,
    [
      ["2", "dk"],
      ["  Email the user when it locks?", "hi"],
    ],
    fw,
  );
  g.fill(fx + 3, cy + 5, fw - 3, 1, "on-hl");
  let ax = g.put(fx + 3, cy + 5, "❯ ", "blu b on-hl");
  ax = g.put(ax, cy + 5, st.answer, "hi on-hl", fx + fw - ax - 1);
  if (focused && !st.sent) g.put(ax, cy + 5, " ", "on-cur");
  if (st.sent) {
    const sx = g.icon(fx, cy + 7, "pass", "grn");
    segs(g, sx, cy + 7, [[`Sent to ${f.id}.`, "grn"]], fx + fw - sx);
  } else {
    segs(g, fx, cy + 7, [[`Lands in ${f.id}, idle at its prompt.`, "dk"]], fw);
  }
  const flashing = st.flash === "ctrl+s";
  segs(
    g,
    fx,
    cy + ch - 1,
    [
      ["ctrl+s", flashing ? "blk on-cyn b" : "cyn b"],
      [" send   ", ""],
      ["ctrl+n", "cyn b"],
      [" next   ", ""],
      ["esc", "cyn b"],
      [" back", ""],
    ],
    fw,
  );
}

function prs(g: Grid, cx: number, cy: number, cw: number, ch: number) {
  segs(g, cx, cy, [["Stack in merge order, bottom first", "dk"]], cw);
  const right = "gh checked 12s ago";
  if (cw > 36 + len(right) + 2) {
    const rx = g.icon(cx + cw - len(right) - 2, cy, "branch", "cm");
    g.put(rx, cy, right, "cm");
  }
  const stateX = cx + cw - 17;
  const ciX = cx + cw - 10;
  const titleW = stateX - (cx + 12) - 1;
  g.put(cx, cy + 2, "  #  PR", "cm");
  g.put(cx + 12, cy + 2, "title", "cm");
  g.put(stateX, cy + 2, "state", "cm");
  g.put(ciX, cy + 2, "CI", "cm");
  const rows: [string, string, string, IconName, string, string][] = [
    [
      "1",
      "#212",
      "Stream CSV rows instead of buffering",
      "pass",
      "grn",
      "pass",
    ],
    [
      "2",
      "#213",
      "Add Export to the Contacts toolbar",
      "pending",
      "yel",
      "pending",
    ],
  ];
  rows.forEach(([n, pr, title, icon, color, word], i) => {
    const y = cy + 3 + i;
    const bg = i === 0 ? " on-hl" : "";
    if (bg) g.fill(cx, y, cw, 1, bg);
    g.put(cx, y, i === 0 ? "❯" : " ", "blu b" + bg);
    g.put(cx + 2, y, n, "dk" + bg);
    g.put(cx + 5, y, pr, "hi b" + bg);
    g.put(cx + 12, y, title, bg, titleW);
    g.put(stateX, y, "draft", "dk" + bg);
    const ix = g.icon(ciX, y, icon, color + bg);
    g.put(ix, y, word, color + bg, cx + cw - ix);
  });
  segs(
    g,
    cx,
    cy + 6,
    [["Layer 3 (limit exports per account) is not pushed yet.", "cm"]],
    cw,
  );
  segs(
    g,
    cx,
    cy + ch - 1,
    [
      ["o", "cyn b"],
      [" open in browser   ", ""],
      ["⏎", "cyn b"],
      [" show checks   ", ""],
      ["r", "cyn b"],
      [" refresh now", ""],
    ],
    cw,
  );
}

function footer(
  g: Grid,
  y: number,
  mode: Focus,
  ctx: string,
  hints: [string, string][],
  flash: string | null,
) {
  g.fill(0, y, g.w, 1, "on-sl");
  let x = g.put(0, y, ` ${mode} `, "blk b " + MODE_BG[mode]);
  if (ctx) x = g.put(x, y, ` ${ctx} `, "hi on-gt");
  x++;
  const limit = g.w - 8;
  for (const [k, d] of hints) {
    if (x + 1 + len(k) + 1 + len(d) > limit) break;
    x = g.put(x + 1, y, k, k === flash ? "blk on-cyn b" : "cyn on-sl b");
    x = g.put(x + 1, y, d, "fg on-sl") + 1;
  }
  g.put(g.w - 7, y, "?", "cyn on-sl b");
  g.put(g.w - 5, y, "keys ", "dk on-sl");
}

export function renderHub(st: DemoState, w: number, h: number = ROWS.hub) {
  const narrow = w < 80;
  const g = new Grid(w, h);
  const f = findFactory(st, st.sel);
  statusLine(g, 0, st, narrow);
  const rx = narrow ? 6 : 35;
  const rw = w - rx;
  if (narrow) selectorNarrow(g, 0, 2, st);
  else selector(g, 0, 1, 34, h - 2, st, st.focus === "FACTORIES");
  segs(
    g,
    rx,
    1,
    [
      [" ", ""],
      ["✻", "cm"],
      [" ", ""],
      [f.id, "dk b"],
      ["  ", ""],
      [f.prompt, "cm"],
      ["  ", ""],
      [`~/dev/${f.repo}/.claude/worktrees/${f.id}`, "cm"],
    ],
    rw,
  );
  const sessH = narrow ? 11 : 12;
  session(g, rx + 1, 2, rw - 1, sessH, st, f);
  panel(
    g,
    rx,
    2 + sessH,
    rw,
    h - 3 - sessH,
    st,
    f,
    st.focus === "PANEL",
    narrow,
  );
  const ctx = st.focus === "PANEL" ? `${f.id} · q-rl-1` : f.id;
  footer(g, h - 1, st.focus, ctx, HINTS[st.focus], st.flash);
  return g.html();
}

export function renderHero(st: DemoState) {
  const g = new Grid(COLS.hero, ROWS.hero);
  statusLine(g, 0, st, true);
  selector(g, 0, 1, COLS.hero, ROWS.hero - 2, st, true);
  footer(
    g,
    ROWS.hero - 1,
    "FACTORIES",
    "",
    [
      ["j/k", "move"],
      ["g", "next"],
    ],
    null,
  );
  return g.html();
}

export function renderAgents() {
  const w = COLS.agents;
  const h = ROWS.agents;
  const g = new Grid(w, h);
  const cols = [2, 14, 24, 37];
  box(g, 0, 0, w, h, "blu");
  let x = g.put(1, 0, " Agents ", "hi b");
  x = g.put(x, 0, "·", "cm");
  g.put(x, 0, " csv-export ", "cm");
  ["role", "tool", "model", "effort"].forEach((label, i) =>
    g.put(cols[i]!, 1, label, "cm"),
  );
  AGENTS.forEach(([role, tool, model, effort, mine], i) => {
    const y = 2 + i;
    const v = mine ? "hi b" : "dk";
    g.put(cols[0]!, y, role, mine ? "hi b" : "fg");
    g.put(cols[1]!, y, tool, v);
    g.put(cols[2]!, y, model, v);
    g.put(cols[3]!, y, effort || "–", effort ? v : "gt");
  });
  segs(
    g,
    2,
    12,
    [
      ["t", "cyn b"],
      [" tool   ", ""],
      ["m", "cyn b"],
      [" model   ", ""],
      ["e", "cyn b"],
      [" effort", ""],
    ],
    w - 4,
  );
  const label = " bold: this factory only ";
  g.put(w - 1 - len(label), h - 1, label, "cm");
  return g.html();
}
