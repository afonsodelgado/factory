import type { LocalFactory, LocalItem, Snapshot } from "./api/types.ts";

export type Groups = {
  needsYou: LocalFactory[];
  repos: { repo: string; name: string; factories: LocalFactory[] }[];
  done: LocalFactory[];
};

const waitingSince = (f: LocalFactory) =>
  f.needs_since ? Date.parse(f.needs_since) : Infinity;

export function needsYou(f: LocalFactory): boolean {
  return f.status === "needs you" && f.items.length > 0;
}

export function group(factories: LocalFactory[]): Groups {
  const needing = factories
    .filter(needsYou)
    .sort((a, b) => waitingSince(a) - waitingSince(b));
  const done = factories.filter((f) => f.status === "done" && !needsYou(f));
  const rest = factories.filter((f) => !needsYou(f) && f.status !== "done");
  const repos = new Map<string, Groups["repos"][number]>();
  for (const f of rest) {
    const repo = repos.get(f.repo) ?? {
      repo: f.repo,
      name: f.repo_name,
      factories: [],
    };
    repo.factories.push(f);
    repos.set(f.repo, repo);
  }
  return {
    needsYou: needing,
    repos: [...repos.values()].sort((a, b) => a.name.localeCompare(b.name)),
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
