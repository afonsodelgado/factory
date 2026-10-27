export type Status =
  "running" | "starting" | "needs" | "ci" | "agent" | "stalled" | "done";
export type Focus = "FACTORIES" | "PANEL";
export type Tab = "prs" | "needs";

export interface Factory {
  n: number;
  id: string;
  repo: string;
  prompt: string;
  status: Status;
  track: string;
  stage: string;
  age: string;
  prs: number;
  arts: number;
}

export interface Repo {
  name: string;
  path: string;
}

export interface DemoState {
  fleet: Factory[];
  sel: string;
  focus: Focus;
  tab: Tab;
  attn: number;
  answer: string;
  sent: boolean;
  answered: boolean;
  flash: string | null;
  spin: number;
  secs: number;
  tool: number;
  step: number;
}

export type Agent = [
  role: string,
  tool: string,
  model: string,
  effort: string,
  mine: boolean,
];

export const REPOS: Repo[] = [
  { name: "shop", path: "~/dev/shop" },
  { name: "api", path: "~/dev/api" },
];

export const FLEET: Factory[] = [
  {
    n: 1,
    id: "csv-export",
    repo: "shop",
    prompt: "Export contacts as CSV for ops.",
    status: "running",
    track: "dddcosoooo",
    stage: "implement",
    age: "24m",
    prs: 2,
    arts: 2,
  },
  {
    n: 2,
    id: "rate-limits",
    repo: "shop",
    prompt: "Rate-limit failed logins per account and per IP.",
    status: "running",
    track: "coooosoooo",
    stage: "requirements",
    age: "3m",
    prs: 0,
    arts: 0,
  },
  {
    n: 3,
    id: "dark-mode",
    repo: "shop",
    prompt: "Add a dark theme that follows the system setting.",
    status: "starting",
    track: "ooooosoooo",
    stage: "no state yet",
    age: "40s",
    prs: 0,
    arts: 0,
  },
  {
    n: 4,
    id: "search-typos",
    repo: "shop",
    prompt: "Let product search forgive one typo per word.",
    status: "stalled",
    track: "dsdddscooo",
    stage: "qa",
    age: "3h",
    prs: 3,
    arts: 1,
  },
  {
    n: 5,
    id: "webhook-retry",
    repo: "api",
    prompt: "Retry failed webhooks with backoff, then park them.",
    status: "ci",
    track: "dsddcooooo",
    stage: "review",
    age: "12m",
    prs: 2,
    arts: 1,
  },
  {
    n: 6,
    id: "invoice-pdf",
    repo: "api",
    prompt: "Render invoices as PDF with the new letterhead.",
    status: "needs",
    track: "ddcooooooo",
    stage: "plan",
    age: "41m",
    prs: 0,
    arts: 3,
  },
  {
    n: 7,
    id: "refund-audit",
    repo: "api",
    prompt: "Log every refund with who approved it.",
    status: "agent",
    track: "dsdddddcoo",
    stage: "qa_fixes",
    age: "18m",
    prs: 3,
    arts: 2,
  },
  {
    n: 0,
    id: "currency-round",
    repo: "api",
    prompt: "Round currency once, at the edge.",
    status: "done",
    track: "dsdddddddd",
    stage: "handoff",
    age: "2d",
    prs: 2,
    arts: 1,
  },
];

export const TOOLS = [
  "Edit(internal/export/csv.go)",
  "Bash(go test ./internal/export/...)",
  "Edit(web/contacts/Toolbar.tsx)",
  "Read(internal/export/csv_test.go)",
  "Bash(gh stack push --draft)",
];

export const AGENTS: Agent[] = [
  ["supervisor", "claude", "sonnet", "medium", false],
  ["analyst", "claude", "sonnet", "high", false],
  ["designer", "claude", "opus", "xhigh", false],
  ["architect", "claude", "fable", "high", false],
  ["engineer", "codex", "gpt-5.6-sol", "high", true],
  ["reviewer", "claude", "opus", "high", false],
  ["shepherd", "opencode", "big-pickle", "", false],
  ["qa", "claude", "opus", "high", false],
  ["fixer", "claude", "sonnet", "medium", false],
];

export const ANSWER = "yes, with a reset link";

export function findFactory(st: DemoState, id: string): Factory {
  const found = st.fleet.find((f) => f.id === id);
  if (!found) throw new Error(`unknown factory ${id}`);
  return found;
}

function set(st: DemoState, id: string, patch: Partial<Factory>) {
  Object.assign(findFactory(st, id), patch);
}

export function baseState(): DemoState {
  return {
    fleet: FLEET.map((f) => ({ ...f })),
    sel: "csv-export",
    focus: "FACTORIES",
    tab: "prs",
    attn: 0,
    answer: "",
    sent: false,
    answered: false,
    flash: null,
    spin: 4,
    secs: 0,
    tool: 0,
    step: 0,
  };
}

export interface HubPhase {
  ms: number;
  step: number;
  typing: boolean;
  apply: (st: DemoState) => void;
}

export const TYPING_MS_PER_CHAR = 75;

export const HUB_PHASES: HubPhase[] = [
  { ms: 3200, step: 0, typing: false, apply: () => {} },
  {
    ms: 1900,
    step: 1,
    typing: false,
    apply: (s) => {
      set(s, "rate-limits", { status: "needs" });
      set(s, "webhook-retry", {
        status: "running",
        track: "dsdddcoooo",
        stage: "coderabbit",
      });
    },
  },
  {
    ms: 900,
    step: 2,
    typing: false,
    apply: (s) => {
      s.flash = "g";
    },
  },
  {
    ms: 1100,
    step: 2,
    typing: false,
    apply: (s) => {
      s.flash = null;
      s.sel = "rate-limits";
      s.focus = "PANEL";
      s.tab = "needs";
      s.attn = 1;
    },
  },
  {
    ms: 0,
    step: 3,
    typing: true,
    apply: (s) => {
      s.answer = ANSWER;
    },
  },
  {
    ms: 1300,
    step: 3,
    typing: false,
    apply: (s) => {
      s.flash = "ctrl+s";
    },
  },
  {
    ms: 2200,
    step: 4,
    typing: false,
    apply: (s) => {
      s.flash = null;
      s.sent = true;
      s.answered = true;
      s.attn = 0;
      set(s, "rate-limits", { status: "running" });
    },
  },
  {
    ms: 3400,
    step: 4,
    typing: false,
    apply: (s) => {
      s.sent = false;
      s.focus = "FACTORIES";
      set(s, "dark-mode", {
        status: "running",
        track: "dcooosoooo",
        stage: "design",
        age: "1m",
      });
    },
  },
];

export const HUB_STATIC = 5;

export function hubStateAt(index: number): DemoState {
  const s = baseState();
  for (let i = 0; i <= index; i++) HUB_PHASES[i]!.apply(s);
  s.step = HUB_PHASES[index]!.step;
  if (index === HUB_STATIC) s.flash = null;
  return s;
}

export const HERO_STEP_MS = 1700;
export const HERO_LAST_STEP_MS = 2600;

const HERO_STEPS: ((st: DemoState) => void)[] = [
  () => {},
  (s) =>
    set(s, "dark-mode", {
      status: "running",
      track: "dcooosoooo",
      stage: "design",
      age: "1m",
    }),
  (s) =>
    set(s, "webhook-retry", {
      status: "running",
      track: "dsdddcoooo",
      stage: "coderabbit",
    }),
  (s) => set(s, "rate-limits", { status: "needs" }),
  (s) =>
    set(s, "csv-export", { track: "ddddcsoooo", stage: "review", age: "1m" }),
  (s) => {
    set(s, "invoice-pdf", {
      status: "running",
      track: "dddcoooooo",
      stage: "implement",
      age: "0s",
    });
    set(s, "refund-audit", {
      status: "running",
      track: "dsddddddco",
      stage: "test_gaps",
    });
  },
  (s) => {
    set(s, "webhook-retry", { status: "ci", track: "dsddddcooo", stage: "qa" });
    set(s, "search-typos", { status: "running" });
  },
];

export const HERO_STATES = HERO_STEPS.length;

export function heroStateAt(index: number): DemoState {
  const s = baseState();
  for (let i = 0; i <= index; i++) HERO_STEPS[i]!(s);
  return s;
}
