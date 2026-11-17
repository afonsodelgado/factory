import { useCallback, useEffect, useRef, useState } from "react";
import { nodeApi } from "./api/client.ts";
import { cache, type Draft } from "./api/cache.ts";
import type {
  ActionBody,
  LocalFactory,
  PushState,
  Receipt,
} from "./api/types.ts";
import { reconcile, type PushHealth } from "./push.ts";
import { useApp, type NodeView } from "./state.tsx";

const TICK = 30_000;

export function useNow(): number {
  const { nodes, savedAt } = useApp();
  const live = nodes.some((n) => n.live);
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!live) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), TICK);
    return () => clearInterval(timer);
  }, [live]);
  return !live && savedAt ? Date.parse(savedAt) : now;
}

export function useNode(factory: LocalFactory): NodeView {
  return useApp().nodeOf(factory);
}

export function usePushHealth() {
  const { me, installation } = useApp();
  const [health, setHealth] = useState<PushHealth>();
  const [push, setPush] = useState<PushState>();
  const [round, setRound] = useState(0);
  useEffect(() => {
    if (!me || !installation) return;
    let current = true;
    reconcile(installation).then(
      (next) => {
        if (!current) return;
        setHealth(next.health);
        setPush(next.push);
      },
      () => current && setHealth("off"),
    );
    return () => {
      current = false;
    };
  }, [me, installation, round]);
  const recheck = useCallback(() => setRound((n) => n + 1), []);
  return { health, push, setPush, recheck, round };
}

export function useNodePush(round: number): Record<string, PushState> {
  const { nodes, installation } = useApp();
  const [states, setStates] = useState<Record<string, PushState>>({});
  const asked = JSON.stringify(
    nodes.filter((n) => n.live && n.id).map((n) => [n.id, n.origin]),
  );
  useEffect(() => {
    if (!installation) return;
    let current = true;
    for (const [id, origin] of JSON.parse(asked) as [string, string][]) {
      nodeApi(origin)
        .push(installation)
        .then(
          (state) => current && setStates((all) => ({ ...all, [id]: state })),
          () => undefined,
        );
    }
    return () => {
      current = false;
    };
  }, [asked, installation, round]);
  return states;
}

export function useBlocked(factory: LocalFactory): string | undefined {
  const { live, reach, short: host } = useNode(factory);
  if (live) return undefined;
  switch (reach) {
    case "offline":
      return `Actions need ${host}. Nothing is saved to send later.`;
    case "node-down":
      return `factory node isn't running on ${host}, so nothing can be sent.`;
    case "restarting":
      return `factory is restarting on ${host}. Try again in a moment.`;
  }
  return `Waiting for the latest state from ${host}…`;
}

export function usePreview(
  factory: LocalFactory,
  body: ActionBody | undefined,
): string | undefined {
  const { live, origin } = useNode(factory);
  const [text, setText] = useState<string>();
  const request = live && body ? JSON.stringify(body) : "";
  useEffect(() => {
    setText(undefined);
    if (!request) return;
    let current = true;
    const timer = setTimeout(() => {
      nodeApi(origin)
        .preview(factory, JSON.parse(request) as ActionBody)
        .then(
          (preview) => current && setText(preview.text),
          () => current && setText(undefined),
        );
    }, 300);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [factory.key, factory.incarnation, request, origin]);
  return text;
}

export function useDraft(factory: LocalFactory, itemKey: string) {
  const { scope: account } = useNode(factory);
  const [draft, setLocal] = useState<Draft>({});
  const touched = useRef(false);
  useEffect(() => {
    if (!account) return;
    let current = true;
    cache.draft(account, factory.incarnation, itemKey).then((saved) => {
      if (current && saved && !touched.current) setLocal(saved);
    });
    return () => {
      current = false;
    };
  }, [account, factory.incarnation, itemKey]);
  const setDraft = useCallback(
    (next: Draft) => {
      touched.current = true;
      setLocal(next);
      if (account) cache.saveDraft(account, factory.incarnation, itemKey, next);
    },
    [account, factory.incarnation, itemKey],
  );
  const dropDraft = useCallback(() => {
    if (account) cache.dropDraft(account, factory.incarnation, itemKey);
  }, [account, factory.incarnation, itemKey]);
  return { draft, setDraft, dropDraft };
}

export const settled = (receipt: Receipt | undefined) =>
  receipt?.status === "delivered" || receipt?.status === "queued";

export function useSend(factory: LocalFactory) {
  const { act } = useApp();
  const [receipt, setReceipt] = useState<Receipt>();
  const [sending, setSending] = useState(false);
  const send = useCallback(
    async (body: ActionBody): Promise<Receipt> => {
      setSending(true);
      try {
        const next = await act(factory, body);
        setReceipt(next);
        return next;
      } catch (error) {
        const refused: Receipt = {
          factory: factory.key,
          item: body.item,
          action: body.action,
          status: "refused",
          reason: error instanceof Error ? error.message : String(error),
        };
        setReceipt(refused);
        return refused;
      } finally {
        setSending(false);
      }
    },
    [act, factory],
  );
  return { receipt, setReceipt, sending, send };
}
