import type { ReactNode } from "react";
import { clock, hostName, shortName } from "../format.ts";
import { paths } from "../route.ts";
import { useApp, type NodeView } from "../state.tsx";
import { Link } from "./Link.tsx";

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

function nodesChip(nodes: NodeView[]): [string, string, string, string] {
  const reachable = nodes.filter((n) => n.live).length;
  const asking = nodes.filter((n) => n.reach === "loading").length;
  const all = nodes.length;
  if (reachable === all)
    return ["●", "grn", `${all} nodes`, `Nodes: ${all} of ${all} reachable`];
  if (asking > 0 && reachable + asking === all)
    return ["◌", "mute", `${all} nodes`, `Nodes: asking ${asking}`];
  return [
    "!",
    "yel",
    `${reachable} of ${all}`,
    `Nodes: ${reachable} of ${all} reachable`,
  ];
}

function HostChip() {
  const { reach, navigate, nodes, multi } = useApp();
  const host = hostName();
  if (multi) {
    const [mark, tone, label, aria] = nodesChip(nodes);
    return (
      <button
        type="button"
        className="host"
        aria-label={aria}
        onClick={() => navigate(paths.phone())}
      >
        <span className={`dot ${tone}`} aria-hidden="true">
          {mark}
        </span>
        {label}
      </button>
    );
  }
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
  const { reach, savedAt, nodes, multi } = useApp();
  const host = hostName();
  const someAnswer = nodes.some((n) => !n.lost);
  if (multi && someAnswer)
    return (
      <>
        <HomeBanner />
        {nodes
          .filter((n) => n.lost && !(n.home && reach === "node-down"))
          .map((n) => (
            <Lost key={n.id} node={n} answering={nodes} />
          ))}
      </>
    );
  if (reach === "offline")
    return (
      <Banner tone="warn" glyph="!" title={`Can't reach ${host}.`}>
        This phone is off the tailnet, or {host} is asleep.
        {savedAt ? ` This is the list from ${clock(savedAt)}.` : ""} Nothing can
        be answered until {host} is back. Trying again every 15s.
      </Banner>
    );
  return <HomeBanner />;
}

function Lost({ node, answering }: { node: NodeView; answering: NodeView[] }) {
  const count = node.snapshot?.factories.length ?? 0;
  const others = answering.filter((n) => n.live).map((n) => n.short);
  const said = [
    count &&
      `Its ${count === 1 ? "factory shows its" : `${count} factories show their`} state from then`,
    others.length &&
      `${others.join(" and ")} ${others.length === 1 ? "works" : "work"} as usual`,
  ].filter(Boolean);
  return (
    <Link className="banner warn" href={paths.node(node.id)}>
      <span className="gl" aria-hidden="true">
        !
      </span>
      <span className="bh">
        {node.short} can't be reached
        {node.savedAt ? ` since ${clock(node.savedAt)}` : ""}.
      </span>
      <span className="x">
        {said.length ? `${said.join("; ")}. ` : ""}Trying again every 15s ›
      </span>
    </Link>
  );
}

function HomeBanner() {
  const { reach } = useApp();
  const host = hostName();
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

export function Moved(props: { home: string; installation?: string }) {
  const from = hostName();
  const to = shortName(new URL(props.home).hostname);
  const target = new URL(props.home);
  if (props.installation) target.searchParams.set("from", props.installation);
  return (
    <Closed label={`this app moved to ${to}`} glyph="↗" tone="cyn">
      <h1>This app moved to {to}</h1>
      <p>
        Your phone app lives on {to} now. {to} stays on, so the app opens,
        installs and adds nodes while {from} sleeps.
      </p>
      <ol className="steps">
        <li>Open the new address below. Tailscale already says it's you.</li>
        <li>Add it to the Home Screen again.</li>
        <li>Turn notifications on again there.</li>
      </ol>
      <a className="btn pri wide" href={target.toString()}>
        Open {target.host}
      </a>
      <p className="sm mute">
        Until then, this copy keeps getting notifications from every node. Once
        the new one has them on, each notification arrives once. Then delete
        this copy from the Home Screen.
      </p>
    </Closed>
  );
}

export function Closed(props: {
  label: string;
  children: ReactNode;
  glyph?: string;
  tone?: string;
}) {
  const host = hostName();
  const glyph = props.glyph ?? "✕";
  const tone = props.tone ?? "red";
  return (
    <div className="shell">
      <header className="tb">
        <span className="mode">factory</span>
        <span className="host" aria-label={props.label}>
          <span className={`dot ${tone}`} aria-hidden="true">
            {glyph}
          </span>
          {host}
        </span>
      </header>
      <main className="body">
        <div className="pad stack page">
          <p className={`big-g ${tone}`} aria-hidden="true">
            {glyph}
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
