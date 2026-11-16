import { api } from "./api/client.ts";
import type { Me, PushState } from "./api/types.ts";

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

export async function turnOn(me: Me): Promise<PushState> {
  if ((await Notification.requestPermission()) !== "granted") {
    throw new Error(
      "Notifications are blocked for factory. Allow them in the phone's settings, then try again.",
    );
  }
  const registration = await navigator.serviceWorker.ready;
  await (await registration.pushManager.getSubscription())?.unsubscribe();
  const subscription = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: keyBytes(me.push.vapid_public_key),
  });
  return api.subscribe(subscription.toJSON());
}

export async function turnOff(): Promise<PushState> {
  await (await current())?.unsubscribe();
  return api.unsubscribe();
}

export type PushHealth = "on" | "off" | "blocked" | "expired";

export async function reconcile(me: Me): Promise<PushHealth> {
  if (pushSupport() !== "ready") return "off";
  if (Notification.permission === "denied") return "blocked";
  const local = await current();
  if (local && me.push.subscribed && local.endpoint !== me.push.endpoint) {
    await api.subscribe(local.toJSON());
    return "on";
  }
  const browserAndServerDisagree = (local !== null) !== me.push.subscribed;
  if (browserAndServerDisagree) return "expired";
  return local ? "on" : "off";
}

export async function closeSettled(openKeys: Set<string>): Promise<void> {
  if (!("serviceWorker" in navigator)) return;
  const registration = await navigator.serviceWorker.getRegistration();
  for (const notification of (await registration?.getNotifications()) ?? []) {
    if (notification.tag !== "test" && !openKeys.has(notification.tag)) {
      notification.close();
    }
  }
}
