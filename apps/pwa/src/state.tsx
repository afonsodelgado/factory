import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  act as send,
  exposureOf,
  follow,
  HttpError,
  nodeApi,
  reachOf,
  refusalOf,
  type Reach,
} from "./api/client.ts";
import { accountOf, cache, ownerOf, type Pending } from "./api/cache.ts";
import type {
  ActionBody,
  Exposure,
  LocalFactory,
  Me,
  Member,
  Receipt,
  Refusal,
  Roster,
  Snapshot,
} from "./api/types.ts";
import { hostName, originOf, shortName } from "./format.ts";
import { closeSettled, reregister } from "./push.ts";
import { openKeys } from "./order.ts";
import { parse, type Route } from "./route.ts";

export type NodeView = {
  id: string;
  name: string;
  short: string;
  origin: string;
  home: boolean;
  os?: string;
  reach: Reach | "loading";
  live: boolean;
  lost: boolean;
  savedAt?: string;
  scope?: string;
  snapshot?: Snapshot;
};

type Remote = {
  reach: Reach | "loading";
  snapshot?: Snapshot;
  savedAt?: string;
  received?: string;
};

export type App = {
  reach: Reach | "loading";
  me?: Me;
  account?: string;
  installation?: string;
  refusal?: Refusal;
  exposure?: Exposure;
  snapshot?: Snapshot;
  savedAt?: string;
  live: boolean;
  roster?: Roster;
  nodes: NodeView[];
  multi: boolean;
  joined: string[];
  nodeOf: (factory: LocalFactory) => NodeView;
  route: Route;
  pending: Pending[];
  navigate: (path: string, replace?: boolean) => void;
  act: (factory: LocalFactory, body: ActionBody) => Promise<Receipt>;
  retry: () => void;
};

const Context = createContext<App | null>(null);

export function useApp(): App {
  const app = useContext(Context);
  if (!app) throw new Error("useApp needs AppProvider");
  return app;
}

const isLost = (reach: Reach | "loading") =>
  reach === "offline" ||
  reach === "node-down" ||
  reach === "refused" ||
  reach === "funnel";

const isHome = (m: Member) => originOf(m.name) === location.origin;

const peersOf = (roster: Roster | undefined) =>
  roster?.home === location.origin
    ? roster.members.filter((m) => !isHome(m))
    : [];

export function AppProvider({ children }: { children: ReactNode }) {
  const [reach, setReach] = useState<Reach | "loading">("loading");
  const [me, setMe] = useState<Me>();
  const [account, setAccount] = useState<string>();
  const [installation, setInstallation] = useState<string>();
  const [refusal, setRefusal] = useState<Refusal>();
  const [exposure, setExposure] = useState<Exposure>();
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [savedAt, setSavedAt] = useState<string>();
  const [route, setRoute] = useState<Route>(() => parse(location));
  const [pendingBy, setPendingBy] = useState<Record<string, Pending[]>>({});
  const [roster, setRoster] = useState<Roster>();
  const [remote, setRemote] = useState<Record<string, Remote>>({});
  const [joined, setJoined] = useState<string[]>([]);
  const home = useRef<ReturnType<typeof follow>>(undefined);
  const links = useRef(new Map<string, ReturnType<typeof follow>>());
  const received = useRef<string>(undefined);

  useEffect(() => {
    const dropped =
      reach === "offline" || reach === "node-down" || reach === "restarting";
    if (dropped && received.current) setSavedAt((at) => at ?? received.current);
  }, [reach]);

  const navigate = useCallback((path: string, replace = false) => {
    history[replace ? "replaceState" : "pushState"](null, "", path);
    setRoute(parse(location));
    scrollTo(0, 0);
  }, []);

  const closeLinks = useCallback(() => {
    for (const link of links.current.values()) link.close();
    links.current.clear();
    setRemote({});
  }, []);

  const refuse = useCallback(
    async (why: Refusal) => {
      home.current?.close();
      closeLinks();
      received.current = undefined;
      setMe(undefined);
      setAccount(undefined);
      setSnapshot(undefined);
      setSavedAt(undefined);
      setPendingBy({});
      setRoster(undefined);
      setJoined([]);
      setRefusal(why);
      setReach("refused");
      await cache.purge();
    },
    [closeLinks],
  );

  const adopt = useCallback(async (account: string, next: Roster) => {
    if (next.removed || !next.members?.length) return;
    setRoster(next);
    await cache.saveRoster(account, next);
    const ids = next.members.map((m) => m.id);
    const announced = await cache.announced(account);
    const fresh = next.members.filter(
      (m) => announced && !announced.includes(m.id) && !isHome(m),
    );
    if (fresh.length)
      setJoined((names) => [
        ...new Set([...names, ...fresh.map((m) => shortName(m.name))]),
      ]);
    if (ids.join() !== announced?.join()) await cache.announce(account, ids);
  }, []);

  const restore = useCallback(async (known: string | undefined) => {
    if (!known) return;
    setAccount((current) => current ?? known);
    const savedRoster = await cache.roster(known);
    if (savedRoster) setRoster((current) => current ?? savedRoster);
  }, []);

  const showSaved = useCallback(
    async (known: string | undefined) => {
      if (!known) return;
      await restore(known);
      const saved = await cache.snapshot(known);
      if (!saved || received.current) return;
      setSnapshot(saved.snapshot);
      setSavedAt(saved.savedAt);
    },
    [restore],
  );

  const reconcilePending = useCallback(
    async (scope: string, origin: string) => {
      for (const op of await cache.pending(scope)) {
        try {
          const receipt = await nodeApi(origin).receipt(op.operation);
          if (receipt.status !== "unknown")
            await cache.settlePending(scope, op.operation);
        } catch (error) {
          if (error instanceof HttpError && error.status === 404) {
            await cache.settlePending(scope, op.operation);
          }
        }
      }
      const left = await cache.pending(scope);
      setPendingBy((all) => ({ ...all, [scope]: left }));
    },
    [],
  );

  const connect = useCallback(() => {
    home.current?.close();
    let id = "";
    let self = "";
    home.current = follow("", {
      up: async (current) => {
        self = current.node;
        id = accountOf(current.node, current.caller);
        const known = await cache.account();
        if (known && known !== id) {
          closeLinks();
          setRoster(undefined);
          await cache.purge();
        }
        await cache.adoptLegacy(id);
        await cache.setAccount(id);
        const browser = await cache.installation();
        setRefusal(undefined);
        setExposure(undefined);
        setMe(current);
        setAccount(id);
        setInstallation(browser);
        await showSaved(id);
        reconcilePending(id, "");
        reregister(browser).catch(() => undefined);
        return browser;
      },
      snapshot: (next) => {
        const keys = openKeys(next);
        received.current = new Date().toISOString();
        setSnapshot(next);
        setSavedAt(undefined);
        setReach("online");
        cache.saveSnapshot(id, next);
        cache.pruneDrafts(id, keys);
        closeSettled(self, keys);
        if (next.roster) adopt(id, next.roster);
      },
      restarting: () => setReach("restarting"),
      down: async (error) => {
        const why = refusalOf(error);
        if (why) {
          await refuse(why);
          return false;
        }
        setExposure(exposureOf(error));
        setReach(reachOf(error));
        await showSaved(await cache.account().catch(() => undefined));
        return true;
      },
    });
  }, [refuse, showSaved, reconcilePending, closeLinks, adopt]);

  const homeLive = reach === "online" && !savedAt;
  const homeLiveRef = useRef(false);
  homeLiveRef.current = homeLive;

  const watch = useCallback(
    (member: Member, account: string) => {
      const id = member.id;
      const owner = ownerOf(account);
      const scope = accountOf(id, owner);
      const origin = originOf(member.name);
      const update = (change: (r: Remote) => Remote) => {
        if (!links.current.has(id)) return;
        setRemote((all) => ({
          ...all,
          [id]: change(all[id] ?? { reach: "loading" }),
        }));
      };
      cache.snapshot(scope).then((saved) => {
        if (!saved) return;
        update((r) =>
          r.snapshot
            ? r
            : { ...r, snapshot: saved.snapshot, savedAt: saved.savedAt },
        );
      });
      return follow(origin, {
        up: async () => {
          reconcilePending(scope, origin);
          return cache.installation();
        },
        snapshot: (next) => {
          const at = new Date().toISOString();
          const keys = openKeys(next);
          update(() => ({ reach: "online", snapshot: next, received: at }));
          cache.saveSnapshot(scope, next);
          cache.pruneDrafts(scope, keys);
          closeSettled(id, keys);
          if (next.roster && !homeLiveRef.current) adopt(account, next.roster);
        },
        restarting: () => update((r) => ({ ...r, reach: "restarting" })),
        down: async (error) => {
          update((r) => ({
            ...r,
            reach: reachOf(error),
            savedAt: r.savedAt ?? r.received,
          }));
          return true;
        },
      });
    },
    [reconcilePending, adopt],
  );

  useEffect(() => {
    if (!account) return;
    return closeLinks;
  }, [account, closeLinks]);

  useEffect(() => {
    if (!roster || !account) return;
    const owner = ownerOf(account);
    const wanted = peersOf(roster);
    for (const [id, link] of links.current) {
      if (wanted.some((m) => m.id === id)) continue;
      link.close();
      links.current.delete(id);
      setRemote(({ [id]: _, ...rest }) => rest);
    }
    for (const m of wanted)
      if (!links.current.has(m.id)) links.current.set(m.id, watch(m, account));
    cache.keepNodes(
      owner,
      roster.members.map((m) => m.id),
    );
  }, [roster, account, watch]);

  const retry = useCallback(() => {
    connect();
    for (const link of links.current.values()) link.retry();
  }, [connect]);

  useEffect(() => {
    cache
      .account()
      .then(restore)
      .catch(() => undefined)
      .finally(connect);
    const onPop = () => setRoute(parse(location));
    addEventListener("popstate", onPop);
    addEventListener("online", retry);
    const onMessage = (event: MessageEvent) => {
      if (event.data?.type === "open") navigate(event.data.path);
    };
    navigator.serviceWorker?.addEventListener("message", onMessage);
    return () => {
      home.current?.close();
      removeEventListener("popstate", onPop);
      removeEventListener("online", retry);
      navigator.serviceWorker?.removeEventListener("message", onMessage);
    };
  }, [connect, retry, navigate, restore]);

  const nodes = useMemo<NodeView[]>(() => {
    const homeMember = roster?.members.find(isHome);
    const homeView: NodeView = {
      id: homeMember?.id ?? me?.node ?? snapshot?.node ?? "",
      name: location.host,
      short: hostName(),
      origin: "",
      home: true,
      os: homeMember?.os,
      reach,
      live: homeLive,
      lost: isLost(reach),
      savedAt,
      scope: account,
      snapshot,
    };
    const owner = account ? ownerOf(account) : undefined;
    const others = peersOf(roster)
      .map((m): NodeView => {
        const r = remote[m.id] ?? { reach: "loading" };
        return {
          id: m.id,
          name: m.name,
          short: shortName(m.name),
          origin: originOf(m.name),
          home: false,
          os: m.os,
          reach: r.reach,
          live: r.reach === "online",
          lost: isLost(r.reach),
          savedAt: r.reach === "online" ? undefined : r.savedAt,
          scope: owner ? accountOf(m.id, owner) : undefined,
          snapshot: r.snapshot,
        };
      })
      .sort((a, b) => a.short.localeCompare(b.short));
    return [homeView, ...others];
  }, [roster, remote, me, snapshot, reach, homeLive, savedAt, account]);

  const multi = nodes.length > 1;

  const merged = useMemo<Snapshot | undefined>(() => {
    if (!multi) return snapshot;
    const known = nodes.filter((n) => n.snapshot);
    if (known.length === 0) return undefined;
    const factories = known.flatMap((n) =>
      n.home ? n.snapshot!.factories : n.snapshot!.factories.map(placed(n)),
    );
    return { ...(snapshot ?? known[0].snapshot!), factories };
  }, [multi, nodes, snapshot]);

  const nodeOf = useCallback(
    (factory: LocalFactory) =>
      nodes.find((n) => factory.node === n.id && !n.home) ?? nodes[0],
    [nodes],
  );

  const markReach = useCallback((node: NodeView, error: unknown) => {
    if (node.home) {
      setReach(reachOf(error));
      return;
    }
    setRemote((all) => ({
      ...all,
      [node.id]: {
        ...all[node.id],
        reach: reachOf(error),
        savedAt: all[node.id]?.savedAt ?? all[node.id]?.received,
      },
    }));
  }, []);

  const act = useCallback(
    async (factory: LocalFactory, body: ActionBody): Promise<Receipt> => {
      const node = nodeOf(factory);
      const scope = node.scope;
      if (!scope)
        throw new Error("factory has not confirmed your Tailscale login yet.");
      const operation = crypto.randomUUID();
      await cache.addPending(scope, {
        operation,
        factory: factory.key,
        node: factory.node,
        item: body.item,
        at: new Date().toISOString(),
      });
      const showPending = async () => {
        const left = await cache.pending(scope);
        setPendingBy((all) => ({ ...all, [scope]: left }));
      };
      await showPending();
      try {
        const receipt = await send(node.origin, factory, operation, body);
        if (receipt.status !== "unknown")
          await cache.settlePending(scope, operation);
        await showPending();
        return receipt;
      } catch (error) {
        const why = node.home ? refusalOf(error) : undefined;
        if (why) {
          await refuse(why);
          throw error;
        }
        const exposed = node.home ? exposureOf(error) : undefined;
        if (exposed || reachOf(error) === "online") {
          await cache.settlePending(scope, operation);
          await showPending();
          if (exposed) connect();
          throw error;
        }
        markReach(node, error);
        await showPending();
        return {
          operation,
          factory: factory.key,
          item: body.item,
          action: body.action,
          status: "unknown",
          reason: `${node.home ? "The computer" : node.short} did not answer, so this may or may not have arrived. factory checks when it is back; nothing is sent again.`,
        };
      }
    },
    [nodeOf, refuse, connect, markReach],
  );

  const pending = useMemo(() => Object.values(pendingBy).flat(), [pendingBy]);

  const value = useMemo<App>(
    () => ({
      reach,
      me,
      account,
      installation,
      refusal,
      exposure,
      snapshot: merged,
      savedAt,
      live: homeLive,
      roster,
      nodes,
      multi,
      joined,
      nodeOf,
      route,
      pending,
      navigate,
      act,
      retry,
    }),
    [
      reach,
      me,
      account,
      installation,
      refusal,
      exposure,
      merged,
      savedAt,
      homeLive,
      roster,
      nodes,
      multi,
      joined,
      nodeOf,
      route,
      pending,
      navigate,
      act,
      retry,
    ],
  );
  return <Context.Provider value={value}>{children}</Context.Provider>;
}

export const routeOn = (node: NodeView, route: string) =>
  node.home ? route : `${node.id}~${route}`;

const placed =
  (node: NodeView) =>
  (f: LocalFactory): LocalFactory => ({
    ...f,
    node: node.id,
    route: routeOn(node, f.route),
    pages: f.pages.map((page) =>
      page.href ? { ...page, href: node.origin + page.href } : page,
    ),
    walkthrough: f.walkthrough?.href
      ? { ...f.walkthrough, href: node.origin + f.walkthrough.href }
      : f.walkthrough,
  });
