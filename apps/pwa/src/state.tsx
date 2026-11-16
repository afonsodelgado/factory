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
  api,
  HttpError,
  reachOf,
  refusalOf,
  type Reach,
} from "./api/client.ts";
import { accountOf, cache, type Pending } from "./api/cache.ts";
import type {
  ActionBody,
  LocalFactory,
  Me,
  Receipt,
  Refusal,
  Snapshot,
} from "./api/types.ts";
import { closeSettled, reregister } from "./push.ts";
import { openKeys } from "./order.ts";
import { parse, type Route } from "./route.ts";

const RETRY = 15_000;
const RECONNECT = 3_000;

export type App = {
  reach: Reach | "loading";
  me?: Me;
  account?: string;
  installation?: string;
  refusal?: Refusal;
  snapshot?: Snapshot;
  savedAt?: string;
  live: boolean;
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

export function AppProvider({ children }: { children: ReactNode }) {
  const [reach, setReach] = useState<Reach | "loading">("loading");
  const [me, setMe] = useState<Me>();
  const [account, setAccount] = useState<string>();
  const [installation, setInstallation] = useState<string>();
  const [refusal, setRefusal] = useState<Refusal>();
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [savedAt, setSavedAt] = useState<string>();
  const [route, setRoute] = useState<Route>(() => parse(location));
  const [pending, setPending] = useState<Pending[]>([]);
  const events = useRef<EventSource>(undefined);
  const timer = useRef<number>(undefined);
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

  const refuse = useCallback(async (why: Refusal) => {
    events.current?.close();
    clearTimeout(timer.current);
    received.current = undefined;
    setMe(undefined);
    setAccount(undefined);
    setSnapshot(undefined);
    setSavedAt(undefined);
    setPending([]);
    setRefusal(why);
    setReach("refused");
    await cache.purge();
  }, []);

  const showSaved = useCallback(async (known: string | undefined) => {
    const saved = known ? await cache.snapshot(known) : undefined;
    if (!saved || received.current) return;
    setSnapshot(saved.snapshot);
    setSavedAt(saved.savedAt);
  }, []);

  const reconcilePending = useCallback(async (id: string) => {
    for (const op of await cache.pending(id)) {
      try {
        const receipt = await api.receipt(op.operation);
        if (receipt.status !== "unknown")
          await cache.settlePending(id, op.operation);
      } catch (error) {
        if (error instanceof HttpError && error.status === 404) {
          await cache.settlePending(id, op.operation);
        }
      }
    }
    setPending(await cache.pending(id));
  }, []);

  const connect = useCallback(async () => {
    clearTimeout(timer.current);
    events.current?.close();
    const fail = async (error: unknown) => {
      const why = refusalOf(error);
      if (why) return refuse(why);
      setReach(reachOf(error));
      await showSaved(await cache.account().catch(() => undefined));
      timer.current = window.setTimeout(connect, RETRY);
    };
    let current: Me;
    try {
      current = await api.me();
    } catch (error) {
      return fail(error);
    }
    const id = accountOf(current.node, current.caller);
    const known = await cache.account();
    if (known && known !== id) await cache.purge();
    await cache.adoptLegacy(id);
    await cache.setAccount(id);
    const browser = await cache.installation();
    setRefusal(undefined);
    setMe(current);
    setAccount(id);
    setInstallation(browser);
    await showSaved(id);
    reconcilePending(id);
    reregister(browser).catch(() => undefined);
    const stream = new EventSource(
      `/api/events?installation=${encodeURIComponent(browser)}`,
    );
    events.current = stream;
    stream.addEventListener("snapshot", (event) => {
      const next = JSON.parse((event as MessageEvent).data) as Snapshot;
      const keys = openKeys(next);
      received.current = new Date().toISOString();
      setSnapshot(next);
      setSavedAt(undefined);
      setReach("online");
      cache.saveSnapshot(id, next);
      cache.pruneDrafts(id, keys);
      closeSettled(keys);
    });
    stream.addEventListener("unavailable", () => setReach("restarting"));
    stream.onerror = () => {
      stream.close();
      api.me().then(() => {
        setReach("restarting");
        timer.current = window.setTimeout(connect, RECONNECT);
      }, fail);
    };
  }, [refuse, showSaved, reconcilePending]);

  useEffect(() => {
    connect();
    const onPop = () => setRoute(parse(location));
    const onOnline = () => connect();
    addEventListener("popstate", onPop);
    addEventListener("online", onOnline);
    const onMessage = (event: MessageEvent) => {
      if (event.data?.type === "open") navigate(event.data.path);
    };
    navigator.serviceWorker?.addEventListener("message", onMessage);
    return () => {
      events.current?.close();
      clearTimeout(timer.current);
      removeEventListener("popstate", onPop);
      removeEventListener("online", onOnline);
      navigator.serviceWorker?.removeEventListener("message", onMessage);
    };
  }, [connect, navigate]);

  const act = useCallback(
    async (factory: LocalFactory, body: ActionBody): Promise<Receipt> => {
      const id = account;
      if (!id)
        throw new Error("factory has not confirmed your Tailscale login yet.");
      const operation = crypto.randomUUID();
      await cache.addPending(id, {
        operation,
        factory: factory.key,
        item: body.item,
        at: new Date().toISOString(),
      });
      const showPending = async () => setPending(await cache.pending(id));
      await showPending();
      try {
        const receipt = await send(factory, operation, body);
        if (receipt.status !== "unknown")
          await cache.settlePending(id, operation);
        await showPending();
        return receipt;
      } catch (error) {
        const why = refusalOf(error);
        if (why) {
          await refuse(why);
          throw error;
        }
        if (reachOf(error) === "online") {
          await cache.settlePending(id, operation);
          await showPending();
          throw error;
        }
        setReach(reachOf(error));
        await showPending();
        return {
          operation,
          factory: factory.key,
          item: body.item,
          action: body.action,
          status: "unknown",
          reason:
            "The computer did not answer, so this may or may not have arrived. factory checks when it is back; nothing is sent again.",
        };
      }
    },
    [account, refuse],
  );

  const value = useMemo<App>(
    () => ({
      reach,
      me,
      account,
      installation,
      refusal,
      snapshot,
      savedAt,
      live: reach === "online" && !savedAt,
      route,
      pending,
      navigate,
      act,
      retry: connect,
    }),
    [
      reach,
      me,
      account,
      installation,
      refusal,
      snapshot,
      savedAt,
      route,
      pending,
      navigate,
      act,
      connect,
    ],
  );
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
