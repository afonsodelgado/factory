import { useState } from "react";
import type { PushState } from "../api/types.ts";
import { clock, day, hostName } from "../format.ts";
import { usePushHealth } from "../hooks.ts";
import { pushSupport, turnOff, turnOn } from "../push.ts";
import { paths } from "../route.ts";
import { useApp } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";
import { Check } from "./Setup.tsx";

export function ThisPhone() {
  const { me, reach, setMe, retry } = useApp();
  const { health, recheck } = usePushHealth(me);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const host = hostName();
  const online = reach === "online";

  const change = async (work: () => Promise<PushState>, failure: string) => {
    if (!me) return;
    setBusy(true);
    setProblem(undefined);
    try {
      setMe({ ...me, push: await work() });
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      setProblem(`${failure} ${reason}`);
    } finally {
      setBusy(false);
      recheck();
    }
  };
  const subscribe = () =>
    change(() => turnOn(me!), "Notifications are not on.");
  const unsubscribe = () =>
    change(
      turnOff,
      `Couldn't turn notifications off, so they are still on for ${host}.`,
    );

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
                ? `reachable over HTTPS · factory serve ${me.version}`
                : `${host} can't be reached right now.`
            }
          />
          {me ? (
            <Check
              ok
              label={`Paired as ${me.device.name}`}
              detail={`since ${day(me.device.paired_at)} · revoke it in the hub's Phone popup`}
            />
          ) : null}
          <Notifications
            health={health}
            busy={busy || !me}
            onSubscribe={subscribe}
          />
          {me ? (
            <Check
              ok={!!me.push.last_sent && !me.push.problem}
              warn={!!me.push.problem}
              label="Last notification"
              detail={
                me.push.problem ??
                (me.push.last_sent
                  ? `${day(me.push.last_sent)} · ${clock(me.push.last_sent)}`
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
          {health === "on" ? (
            <button
              type="button"
              className="btn"
              disabled={busy || !me}
              onClick={unsubscribe}
            >
              Turn off notifications
            </button>
          ) : health === "off" && pushSupport() === "ready" ? (
            <button
              type="button"
              className="btn pri"
              disabled={busy || !me}
              onClick={subscribe}
            >
              Turn on notifications
            </button>
          ) : null}
        </footer>
      </div>
    </AppShell>
  );
}

function Notifications(props: {
  health: ReturnType<typeof usePushHealth>["health"];
  busy: boolean;
  onSubscribe: () => void;
}) {
  switch (props.health) {
    case "on":
      return <Check ok label="Notifications on" />;
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
    case "off":
      return <Check ok={false} label="Notifications off" />;
  }
  return <Check ok={false} label="Notifications" detail="checking…" />;
}
