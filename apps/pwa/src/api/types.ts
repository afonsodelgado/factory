export type Eligibility = { ready: boolean; queues?: boolean; reason: string };

export type Question = {
  text: string;
  options: string[];
  recommended?: string;
};

export type PermissionOption = { id: string; label: string; tells?: boolean };

export type PermissionMenu = {
  tool: string;
  title: string;
  command: string[];
  question: string;
  options: PermissionOption[];
  fingerprint: string;
};

export type ReceiptStatus =
  "queued" | "sending" | "delivered" | "refused" | "unknown" | "stale";

export type Receipt = {
  operation?: string;
  factory: string;
  item?: string;
  action: string;
  status: ReceiptStatus;
  reason?: string;
  source?: string;
  at?: string;
};

export type ItemKind =
  | "question"
  | "gate"
  | "comment"
  | "cap"
  | "failure"
  | "permission"
  | (string & {});

export type LocalItem = {
  id: string;
  key: string;
  kind: ItemKind;
  title: string;
  body?: string;
  questions?: Question[];
  stage?: string;
  since: string;
  detail?: string[];
  role?: string;
  answer?: Receipt;
  send: Eligibility;
  permission?: PermissionMenu;
};

export type LocalPage = {
  name: string;
  title: string;
  open_threads?: number;
  href?: string;
  unavailable?: string;
};

export type Resolution = {
  id: string;
  kind: string;
  since: string;
  resolved_at: string;
  source?: string;
  at?: string;
};

export type LocalFactory = {
  key: string;
  route: string;
  incarnation: string;
  id: string;
  title?: string;
  repo: string;
  repo_name: string;
  stage?: string;
  status: string;
  paused: boolean;
  needs_since?: string;
  items: LocalItem[];
  resolved?: Resolution[];
  pages: LocalPage[];
  message: Eligibility;
  can_pause: boolean;
  can_wake: boolean;
  node?: string;
};

export type Snapshot = {
  node?: string;
  version?: string;
  revision: number;
  server_time: string;
  factories: LocalFactory[];
  roster?: Roster;
};

export type Member = {
  id: string;
  name: string;
  owner: string;
  tailnet: string;
  os?: string;
  self?: boolean;
  pending: number;
};

export type Roster = {
  id: string;
  home: string;
  self: string;
  removed?: boolean;
  members: Member[];
  leaving?: Member[];
};

export type Check = {
  name: string;
  mark: string;
  detail: string;
  fix?: string;
  on?: string;
  lines?: string[];
  at: string;
};

export type Readiness = {
  node: string;
  at: string;
  checks: Check[] | null;
  problems: number;
};

export type Candidate = {
  name: string;
  short: string;
  os: string;
  state: string;
  detail: string;
  fix?: string;
  on?: string;
  last_seen?: string;
  selectable: boolean;
  hello?: { version: string; os: string };
};

export type CheckReport = {
  target: Candidate;
  checks: Check[] | null;
  refusal?: string;
  problems: number;
  at: string;
};

export type Added = { member: Member; report: CheckReport; roster: Roster };

export type PushState = {
  vapid_public_key: string;
  subscribed: boolean;
  endpoint?: string;
  last_sent?: string;
  problem?: string;
};

export type Me = {
  caller: string;
  node: string;
  version: string;
  server_time: string;
};

export type Refusal = { owner?: string; login?: string };

export type Exposure = { error: string; fixes: string[] | null };

export type Link = {
  route: string;
  item: string;
  incarnation: string;
  open: boolean;
};

export type Action =
  | "answer"
  | "approve"
  | "changes"
  | "reply"
  | "handled"
  | "continue"
  | "retry"
  | "skip"
  | "message"
  | "pause"
  | "wake"
  | "permission";

export type ActionBody = {
  action: Action;
  item?: string;
  since?: string;
  answers?: string[];
  text?: string;
  option?: string;
  fingerprint?: string;
};

export const MAX_MESSAGE = 4000;
