import type { LocalFactory, LocalItem, Snapshot } from "./api/types.ts";

export type Groups = {
  needsYou: LocalFactory[];
  repos: {
    repo: string;
    name: string;
    node?: string;
    factories: LocalFactory[];
  }[];
  done: LocalFactory[];
};

export const needsSince = (f: LocalFactory) =>
  f.needs_since ?? f.items[0]?.since;

const waitingSince = (f: LocalFactory) => {
  const since = needsSince(f);
  return since ? Date.parse(since) : Infinity;
};

const handedOff = (f: LocalFactory) =>
  f.items.some((i) => i.kind === "handoff" && i.answer?.status !== "dismissed");

export function needsYou(f: LocalFactory): boolean {
  return f.items.length > 0 && (f.status === "needs you" || handedOff(f));
}

export function group(
  factories: LocalFactory[],
  lost: (f: LocalFactory) => boolean = () => false,
): Groups {
  const waiting = (f: LocalFactory) => needsYou(f) && !lost(f);
  const finished = (f: LocalFactory) => f.status === "done" && !waiting(f);
  const needing = factories
    .filter(waiting)
    .sort((a, b) => waitingSince(a) - waitingSince(b));
  const done = factories.filter(finished);
  const rest = factories.filter((f) => !waiting(f) && !finished(f));
  const repos = new Map<string, Groups["repos"][number]>();
  for (const f of rest) {
    const key = `${f.repo}\n${f.node ?? ""}`;
    const repo = repos.get(key) ?? {
      repo: f.repo,
      name: f.repo_name,
      node: f.node,
      factories: [],
    };
    repo.factories.push(f);
    repos.set(key, repo);
  }
  return {
    needsYou: needing,
    repos: [...repos.values()].sort(
      (a, b) =>
        a.name.localeCompare(b.name) ||
        Number(!!a.node) - Number(!!b.node) ||
        (a.node ?? "").localeCompare(b.node ?? ""),
    ),
    done,
  };
}

export function next(
  snapshot: Snapshot,
  except: string,
): LocalFactory | undefined {
  return group(snapshot.factories).needsYou.find((f) => f.route !== except);
}

export function findItem(
  snapshot: Snapshot | undefined,
  route: string,
  key: string,
): { factory?: LocalFactory; item?: LocalItem } {
  const factory = snapshot?.factories.find((f) => f.route === route);
  return { factory, item: factory?.items.find((i) => i.key === key) };
}

export function openKeys(snapshot: Snapshot): Set<string> {
  return new Set(snapshot.factories.flatMap((f) => f.items.map((i) => i.key)));
}

export function age(since: string | undefined, now: number): string {
  if (!since) return "";
  const seconds = Math.max(0, Math.round((now - Date.parse(since)) / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}
