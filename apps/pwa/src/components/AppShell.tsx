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
      : reach === "serve-down" || reach === "unpaired"
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
  if (reach === "serve-down")
    return (
      <Banner
        tone="err"
        glyph="✕"
        title={`${host} is up, but factory serve isn't running.`}
      >
        Tailscale answered, the app server didn't. Start it with{" "}
        <code>factory phone start</code> on {host}. No notifications are sent
        while it is down.
      </Banner>
    );
  if (reach === "restarting")
    return (
      <Banner
        tone="info"
        glyph="◌"
        title="factory is moving between the hub and factory serve."
      >
        This takes a few seconds. The list reconnects by itself.
      </Banner>
    );
  return null;
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
