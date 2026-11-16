import type { ReactNode } from "react";
import { clock, hostName } from "../format.ts";
import { paths } from "../route.ts";
import { useApp } from "../state.tsx";

export function AppShell(props: {
  back?: string;
  title?: ReactNode;
  children?: ReactNode;
}) {
  const { navigate } = useApp();
  return (
    <div className="shell">
      <header className="tb">
        {props.back ? (
          <button
            type="button"
            className="back"
            aria-label="Back"
            onClick={() => navigate(props.back!)}
          >
            ‹
          </button>
        ) : (
          <span className="mode">factory</span>
        )}
        {props.title ? <h1 className="sect">{props.title}</h1> : null}
        <HostChip />
      </header>
      <ReachBanner />
      <main className="body">{props.children}</main>
    </div>
  );
}

function HostChip() {
  const { reach, navigate } = useApp();
  const host = hostName();
  const [mark, tone, state] =
    reach === "online"
      ? ["●", "grn", `connected to ${host}`]
      : reach === "node-down"
        ? ["✕", "red", `${host} is not serving this phone`]
        : reach === "offline"
          ? ["!", "yel", `${host} is offline`]
          : ["◌", "mute", `connecting to ${host}`];
  return (
    <button
      type="button"
      className="host"
      aria-label={`This phone, ${state}`}
      onClick={() => navigate(paths.phone())}
    >
      <span className={`dot ${tone}`} aria-hidden="true">
        {mark}
      </span>
      {reach === "offline" ? "offline" : host}
    </button>
  );
}

function ReachBanner() {
  const { reach, savedAt } = useApp();
  const host = hostName();
  if (reach === "offline")
    return (
      <Banner tone="warn" glyph="!" title={`Can't reach ${host}.`}>
        This phone is off the tailnet, or {host} is asleep.
        {savedAt ? ` This is the list from ${clock(savedAt)}.` : ""} Nothing can
        be answered until {host} is back. Trying again every 15s.
      </Banner>
    );
  if (reach === "node-down")
    return (
      <Banner
        tone="err"
        glyph="✕"
        title={`${host} is up, but its factory node isn't running.`}
      >
        Tailscale answered, the node didn't. Start it with{" "}
        <code>factory node run</code> on {host}. No notifications are sent while
        it is down.
      </Banner>
    );
  if (reach === "restarting")
    return (
      <Banner tone="info" glyph="◌" title={`factory is restarting on ${host}.`}>
        This takes a few seconds. The list reconnects by itself.
      </Banner>
    );
  return null;
}

export function Refused() {
  const { refusal } = useApp();
  const host = hostName();
  const owner = refusal?.owner;
  return (
    <Closed label={`${host} refuses this Tailscale account`}>
      <h1>
        {owner
          ? `${host} answers ${owner} only`
          : `${host} needs your Tailscale login`}
      </h1>
      <p>
        {refusal?.login ? (
          <>
            This phone is signed in to Tailscale as{" "}
            <b className="ink">{refusal.login}</b>.{" "}
          </>
        ) : (
          "This device reached factory without a Tailscale login, as a tagged machine does. "
        )}
        factory nodes answer their owner and no one else, so nothing from {host}{" "}
        is shown or kept here.
      </p>
      <p className="sm mute">
        {owner
          ? `Switch to ${owner} in the Tailscale app, then open factory again.`
          : "Sign in to Tailscale as the node's owner on this device, then open factory again."}
      </p>
    </Closed>
  );
}

export function Closed(props: { label: string; children: ReactNode }) {
  const host = hostName();
  return (
    <div className="shell">
      <header className="tb">
        <span className="mode">factory</span>
        <span className="host" aria-label={props.label}>
          <span className="dot red" aria-hidden="true">
            ✕
          </span>
          {host}
        </span>
      </header>
      <main className="body">
        <div className="pad stack page">
          <p className="big-g red" aria-hidden="true">
            ✕
          </p>
          {props.children}
        </div>
      </main>
    </div>
  );
}

function Banner(props: {
  tone: "warn" | "err" | "info";
  glyph: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <div className={`banner ${props.tone}`} role="status">
      <span className="gl" aria-hidden="true">
        {props.glyph}
      </span>
      <span className="bh">{props.title}</span>
      <span className="x">{props.children}</span>
    </div>
  );
}
