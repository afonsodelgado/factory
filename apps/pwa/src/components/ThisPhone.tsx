import { useState } from "react";
import type { PushState } from "../api/types.ts";
import { clock, day, hostName } from "../format.ts";
import { useNodePush, usePushHealth } from "../hooks.ts";
import { pushSupport, turnOff, turnOn, type PushHealth } from "../push.ts";
import { paths } from "../route.ts";
import { useApp, type NodeView } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";
import { Link } from "./Link.tsx";
import { factoryCount } from "./Nodes.tsx";
import { Check } from "./Setup.tsx";

export function ThisPhone() {
  const { me, installation, reach, retry, nodes, multi, navigate } = useApp();
  const { health, push, setPush, recheck, round } = usePushHealth();
  const states = useNodePush(round);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const host = hostName();
  const online = reach === "online";

  const change = async (work: () => Promise<PushState>, failure: string) => {
    if (!push || !installation) return;
    setBusy(true);
    setProblem(undefined);
    try {
      setPush(await work());
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      setProblem(`${failure} ${reason}`);
    } finally {
      setBusy(false);
      recheck();
    }
  };
  const subscribe = () =>
    change(
      () => turnOn(push!.vapid_public_key, installation!),
      "Notifications are not on.",
    );
  const unsubscribe = () =>
    change(
      () => turnOff(push?.endpoint ?? ""),
      `Couldn't turn notifications off, so they are still on for ${host}.`,
    );

  const live = nodes.filter((n) => n.live && n.id);
  const silent = live.flatMap((node) => {
    const why = silence(states[node.id], health);
    return why ? [{ node, why }] : [];
  });
  const everyNodeSends =
    multi &&
    silent.length === 0 &&
    live.length === nodes.length &&
    live.every((n) => states[n.id]);

  return (
    <AppShell back={paths.list()} title="This phone">
      <div className="pad stack">
        <ul className="checks">
          <Check
            ok={online && !!me}
            warn={!online}
            label={location.host}
            detail={
              online && me
                ? `reachable over HTTPS · factory node ${me.version}`
                : `${host} can't be reached right now.`
            }
          />
          {me ? (
            <Check
              ok
              label={`Signed in as ${me.caller}`}
              detail={`through Tailscale · ${host} answers this login only`}
            />
          ) : null}
          <Notifications
            health={health}
            detail={
              everyNodeSends
                ? "every node sends them, with one shared key"
                : undefined
            }
            busy={busy || !push?.vapid_public_key}
            onSubscribe={subscribe}
            onUnsubscribe={unsubscribe}
          />
          {silent.map(({ node, why }) => (
            <Check
              key={node.id}
              ok={false}
              warn
              label={`${node.short} can't notify this phone`}
              detail={why}
            />
          ))}
          {push ? (
            <Check
              ok={!!push.last_sent && !push.problem}
              warn={!!push.problem}
              label="Last notification"
              detail={
                push.problem ??
                (push.last_sent
                  ? `${day(push.last_sent)} · ${clock(push.last_sent)}`
                  : "none yet")
              }
            />
          ) : null}
        </ul>
        {problem ? (
          <p className="stat warn" role="status">
            <span className="gl" aria-hidden="true">
              !
            </span>
            <span>{problem}</span>
          </p>
        ) : null}
        <section className="box">
          <h3 className="bt ink">nodes</h3>
          <ul className="nodes">
            {nodes.map((node) => (
              <li key={node.id || "home"}>
                <NodeRow node={node} />
              </li>
            ))}
          </ul>
        </section>
        <footer className="ab">
          <button
            type="button"
            className="btn"
            onClick={() => {
              retry();
              recheck();
            }}
          >
            Check again
          </button>
          <button
            type="button"
            className="btn pri"
            disabled={!online}
            onClick={() => navigate(paths.add())}
          >
            + Add a node
          </button>
        </footer>
      </div>
    </AppShell>
  );
}

function NodeRow({ node }: { node: NodeView }) {
  const { roster } = useApp();
  const pending = roster?.members.find((m) => m.id === node.id)?.pending ?? 0;
  const count = node.snapshot?.factories.length;
  const [mark, tone] = node.live
    ? ["●", "grn"]
    : node.lost
      ? ["↮", "dk"]
      : ["◌", "mute"];
  const state = node.live
    ? ["running", node.snapshot?.version]
    : node.lost
      ? [
          node.savedAt
            ? `unreachable since ${clock(node.savedAt)}`
            : "unreachable",
        ]
      : ["asking…"];
  const detail = [
    ...state,
    count === undefined ? undefined : factoryCount(count),
    node.home ? "app home" : undefined,
    pending
      ? `${pending} list ${pending === 1 ? "change" : "changes"} not confirmed yet`
      : undefined,
  ]
    .filter(Boolean)
    .join(" · ");
  const body = (
    <>
      <span className={`g ${tone}`} aria-hidden="true">
        {mark}
      </span>
      <span className="l">{node.short}</span>
      <span className="d">{detail}</span>
      <span className="go" aria-hidden="true">
        ›
      </span>
    </>
  );
  if (!node.id) return <span className="nr">{body}</span>;
  return (
    <Link className="nr" href={paths.node(node.id)}>
      {body}
    </Link>
  );
}

function silence(
  state: PushState | undefined,
  phone: PushHealth | undefined,
): string | undefined {
  if (!state) return undefined;
  if (state.health !== "ready") return state.health_detail;
  if (phone === "on" && !state.subscribed)
    return "It hasn't got this phone's subscription yet. Your nodes pass it on; check again in a moment.";
  return undefined;
}

function Notifications(props: {
  health: PushHealth | undefined;
  detail?: string;
  busy: boolean;
  onSubscribe: () => void;
  onUnsubscribe: () => void;
}) {
  switch (props.health) {
    case "on":
      return (
        <Check ok label="Notifications on" detail={props.detail}>
          <button
            type="button"
            className="btn fit"
            disabled={props.busy}
            onClick={props.onUnsubscribe}
          >
            Turn off notifications
          </button>
        </Check>
      );
    case "blocked":
      return (
        <Check
          ok={false}
          warn
          label="Notifications blocked"
          detail="The phone blocks them for factory. Allow them in its settings for factory, then come back."
        />
      );
    case "expired":
      return (
        <Check
          ok={false}
          warn
          label="Subscription expired"
          detail="The push service dropped this phone's subscription."
        >
          <button
            type="button"
            className="btn fit"
            disabled={props.busy}
            onClick={props.onSubscribe}
          >
            Subscribe again
          </button>
        </Check>
      );
    case "rekeyed":
      return (
        <Check
          ok={false}
          warn
          label="Subscribe again"
          detail="This phone subscribed with a push key its node no longer sends with. One key serves every node."
        >
          <button
            type="button"
            className="btn fit"
            disabled={props.busy}
            onClick={props.onSubscribe}
          >
            Subscribe again
          </button>
        </Check>
      );
    case "off":
      return (
        <Check ok={false} label="Notifications off">
          {pushSupport() === "ready" ? (
            <button
              type="button"
              className="btn pri fit"
              disabled={props.busy}
              onClick={props.onSubscribe}
            >
              Turn on notifications
            </button>
          ) : null}
        </Check>
      );
  }
  return <Check ok={false} label="Notifications" detail="checking…" />;
}
