import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api/client.ts";
import { cache, type Draft } from "./api/cache.ts";
import type { ActionBody, LocalFactory, Me, Receipt } from "./api/types.ts";
import { reconcile, type PushHealth } from "./push.ts";
import { hostName } from "./format.ts";
import { useApp } from "./state.tsx";

const TICK = 30_000;

export function useNow(): number {
  const { live, savedAt } = useApp();
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!live) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), TICK);
    return () => clearInterval(timer);
  }, [live]);
  return savedAt ? Date.parse(savedAt) : now;
}

export function usePushHealth(me: Me | undefined) {
  const [health, setHealth] = useState<PushHealth>();
  const [round, setRound] = useState(0);
  useEffect(() => {
    if (!me) return;
    let current = true;
    reconcile(me).then(
      (next) => current && setHealth(next),
      () => current && setHealth("off"),
    );
    return () => {
      current = false;
    };
  }, [me, round]);
  const recheck = useCallback(() => setRound((n) => n + 1), []);
  return { health, recheck };
}

export function useBlocked(): string | undefined {
  const { reach, live } = useApp();
  if (live) return undefined;
  const host = hostName();
  switch (reach) {
    case "offline":
      return `Actions need ${host}. Nothing is saved to send later.`;
    case "serve-down":
      return `factory serve isn't running on ${host}, so nothing can be sent.`;
    case "restarting":
      return `factory is restarting on ${host}. Try again in a moment.`;
  }
  return `Waiting for the latest state from ${host}…`;
}

export function usePreview(
  factory: LocalFactory,
  body: ActionBody | undefined,
): string | undefined {
  const { live } = useApp();
  const [text, setText] = useState<string>();
  const request = live && body ? JSON.stringify(body) : "";
  useEffect(() => {
    setText(undefined);
    if (!request) return;
    let current = true;
    const timer = setTimeout(() => {
      api.preview(factory, JSON.parse(request) as ActionBody).then(
        (preview) => current && setText(preview.text),
        () => current && setText(undefined),
      );
    }, 300);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [factory.key, factory.incarnation, request]);
  return text;
}

export function useDraft(factory: LocalFactory, itemKey: string) {
  const { me } = useApp();
  const device = me?.device.id;
  const [draft, setLocal] = useState<Draft>({});
  const touched = useRef(false);
  useEffect(() => {
    if (!device) return;
    let current = true;
    cache.draft(device, factory.incarnation, itemKey).then((saved) => {
      if (current && saved && !touched.current) setLocal(saved);
    });
    return () => {
      current = false;
    };
  }, [device, factory.incarnation, itemKey]);
  const setDraft = useCallback(
    (next: Draft) => {
      touched.current = true;
      setLocal(next);
      if (device) cache.saveDraft(device, factory.incarnation, itemKey, next);
    },
    [device, factory.incarnation, itemKey],
  );
  const dropDraft = useCallback(() => {
    if (device) cache.dropDraft(device, factory.incarnation, itemKey);
  }, [device, factory.incarnation, itemKey]);
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
