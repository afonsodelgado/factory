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
  type Reach,
} from "./api/client.ts";
import { cache, type Pending } from "./api/cache.ts";
import type {
  ActionBody,
  LocalFactory,
  Me,
  Receipt,
  Snapshot,
} from "./api/types.ts";
import { closeSettled } from "./push.ts";
import { openKeys } from "./order.ts";
import { parse, type Route } from "./route.ts";

const RETRY = 15_000;
const RECONNECT = 3_000;

export type App = {
  reach: Reach | "loading";
  me?: Me;
  snapshot?: Snapshot;
  savedAt?: string;
  live: boolean;
  route: Route;
  pending: Pending[];
  navigate: (path: string, replace?: boolean) => void;
  act: (factory: LocalFactory, body: ActionBody) => Promise<Receipt>;
  setMe: (me: Me) => void;
  retry: () => void;
  forget: () => Promise<void>;
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
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [savedAt, setSavedAt] = useState<string>();
  const [route, setRoute] = useState<Route>(() => parse(location));
  const [pending, setPending] = useState<Pending[]>([]);
  const device = useRef<string>(undefined);
  const events = useRef<EventSource>(undefined);
  const timer = useRef<number>(undefined);
  const received = useRef<string>(undefined);

  useEffect(() => {
    const dropped =
      reach === "offline" || reach === "serve-down" || reach === "restarting";
    if (dropped && received.current) setSavedAt((at) => at ?? received.current);
  }, [reach]);

  const navigate = useCallback((path: string, replace = false) => {
    history[replace ? "replaceState" : "pushState"](null, "", path);
    setRoute(parse(location));
    scrollTo(0, 0);
  }, []);

  const forget = useCallback(async () => {
    events.current?.close();
    await cache.purge();
    device.current = undefined;
    received.current = undefined;
    setMe(undefined);
    setSnapshot(undefined);
    setSavedAt(undefined);
    setPending([]);
    setReach("unpaired");
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
      const why = reachOf(error);
      if (why === "unpaired") return forget();
      setReach(why);
      timer.current = window.setTimeout(connect, RETRY);
    };
    let current: Me;
    try {
      current = await api.me();
    } catch (error) {
      return fail(error);
    }
    if (device.current && device.current !== current.device.id) {
      await cache.purge();
    }
    device.current = current.device.id;
    setMe(current);
    await cache.setDevice(current.device.id);
    reconcilePending(current.device.id);
    const stream = new EventSource("/api/events");
    events.current = stream;
    stream.addEventListener("snapshot", (event) => {
      const next = JSON.parse((event as MessageEvent).data) as Snapshot;
      const keys = openKeys(next);
      received.current = new Date().toISOString();
      setSnapshot(next);
      setSavedAt(undefined);
      setReach("online");
      cache.saveSnapshot(current.device.id, next);
      cache.pruneDrafts(current.device.id, keys);
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
  }, [forget, reconcilePending]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const known = await cache.device().catch(() => undefined);
      const saved = known ? await cache.snapshot(known) : undefined;
      if (!cancelled && saved) {
        device.current = known;
        setSnapshot(saved.snapshot);
        setSavedAt(saved.savedAt);
      }
      if (!cancelled) connect();
    })();
    const onPop = () => setRoute(parse(location));
    const onOnline = () => connect();
    addEventListener("popstate", onPop);
    addEventListener("online", onOnline);
    const onMessage = (event: MessageEvent) => {
      if (event.data?.type === "open") navigate(event.data.path);
    };
    navigator.serviceWorker?.addEventListener("message", onMessage);
    return () => {
      cancelled = true;
      events.current?.close();
      clearTimeout(timer.current);
      removeEventListener("popstate", onPop);
      removeEventListener("online", onOnline);
      navigator.serviceWorker?.removeEventListener("message", onMessage);
    };
  }, [connect, navigate]);

  const act = useCallback(
    async (factory: LocalFactory, body: ActionBody): Promise<Receipt> => {
      const id = device.current;
      if (!id) throw new Error("This phone is not paired.");
      const operation = crypto.randomUUID();
      await cache.addPending(id, {
        operation,
        factory: factory.key,
        item: body.item,
        at: new Date().toISOString(),
      });
      setPending(await cache.pending(id));
      try {
        const receipt = await send(factory, operation, body);
        if (receipt.status !== "unknown")
          await cache.settlePending(id, operation);
        return receipt;
      } catch (error) {
        const why = reachOf(error);
        if (why === "unpaired") {
          await forget();
          throw error;
        }
        if (why === "online") {
          await cache.settlePending(id, operation);
          throw error;
        }
        setReach(why);
        return {
          operation,
          factory: factory.key,
          item: body.item,
          action: body.action,
          status: "unknown",
          reason:
            "The computer did not answer, so this may or may not have arrived. factory checks when it is back; nothing is sent again.",
        };
      } finally {
        setPending(await cache.pending(id));
      }
    },
    [forget],
  );

  const value = useMemo<App>(
    () => ({
      reach,
      me,
      snapshot,
      savedAt,
      live: reach === "online" && !savedAt,
      route,
      pending,
      navigate,
      act,
      setMe,
      retry: connect,
      forget,
    }),
    [
      reach,
      me,
      snapshot,
      savedAt,
      route,
      pending,
      navigate,
      act,
      connect,
      forget,
    ],
  );
  return <Context.Provider value={value}>{children}</Context.Provider>;
}
