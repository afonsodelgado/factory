import { cache } from "./api/cache.ts";
import { api } from "./api/client.ts";
import type { PushState } from "./api/types.ts";

export type PushSupport = "ready" | "install-first" | "unsupported";

export function pushSupport(): PushSupport {
  if ("serviceWorker" in navigator && "PushManager" in window) return "ready";
  return isIOS() && !standalone() ? "install-first" : "unsupported";
}

export function isIOS(): boolean {
  return /iPhone|iPad|iPod/.test(navigator.userAgent);
}

export function standalone(): boolean {
  return (
    matchMedia("(display-mode: standalone)").matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true
  );
}

async function current(): Promise<PushSubscription | null> {
  const registration = await navigator.serviceWorker.ready;
  return registration.pushManager.getSubscription();
}

function keyBytes(base64: string): Uint8Array<ArrayBuffer> {
  const padded = (base64 + "===".slice((base64.length + 3) % 4))
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
}

function sameKey(subscription: PushSubscription, key: string): boolean {
  const current = subscription.options.applicationServerKey;
  const want = keyBytes(key);
  const have = new Uint8Array(current ?? new ArrayBuffer(0));
  return have.length === want.length && have.every((b, i) => b === want[i]);
}

export async function turnOn(
  key: string,
  installation: string,
): Promise<PushState> {
  if ((await Notification.requestPermission()) !== "granted") {
    throw new Error(
      "Notifications are blocked for factory. Allow them in the phone's settings, then try again.",
    );
  }
  const registration = await navigator.serviceWorker.ready;
  const existing = await registration.pushManager.getSubscription();
  const reusable = existing && sameKey(existing, key) ? existing : null;
  if (existing && !reusable) await existing.unsubscribe();
  const subscription =
    reusable ??
    (await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: keyBytes(key),
    }));
  const replaces = await cache.movedFrom();
  const state = await api.subscribe(
    subscription.toJSON(),
    installation,
    replaces,
  );
  if (replaces) await cache.forgetMovedFrom();
  return state;
}

export async function turnOff(endpoint: string): Promise<PushState> {
  const local = await current();
  await local?.unsubscribe();
  return api.unsubscribe(local?.endpoint ?? endpoint);
}

export async function reregister(installation: string): Promise<void> {
  if (pushSupport() !== "ready" || Notification.permission !== "granted")
    return;
  const local = await current();
  if (!local) return;
  const { vapid_public_key } = await api.push(installation);
  if (sameKey(local, vapid_public_key))
    await api.subscribe(local.toJSON(), installation);
}

export type PushHealth = "on" | "off" | "blocked" | "expired" | "rekeyed";

export async function reconcile(
  installation: string,
): Promise<{ health: PushHealth; push: PushState }> {
  const push = await api.push(installation);
  if (pushSupport() !== "ready") return { health: "off", push };
  if (Notification.permission === "denied") return { health: "blocked", push };
  if (!push.vapid_public_key) return { health: "off", push };
  const local = await current();
  if (local && !sameKey(local, push.vapid_public_key))
    return { health: "rekeyed", push };
  if (local && local.endpoint !== push.endpoint)
    return {
      health: "on",
      push: await api.subscribe(local.toJSON(), installation),
    };
  if (!local && push.subscribed) return { health: "expired", push };
  return { health: local ? "on" : "off", push };
}

export async function closeSettled(
  node: string,
  openKeys: Set<string>,
): Promise<void> {
  if (!("serviceWorker" in navigator)) return;
  const registration = await navigator.serviceWorker.getRegistration();
  const prefix = `${node}/`;
  for (const notification of (await registration?.getNotifications()) ?? []) {
    const fromNode = notification.tag.startsWith(prefix);
    const key = notification.tag.slice(prefix.length);
    if (fromNode && !openKeys.has(key)) notification.close();
  }
}
