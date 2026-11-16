import { useState, type ReactNode } from "react";
import { hostName } from "../format.ts";
import { usePushHealth } from "../hooks.ts";
import { pushSupport, standalone, turnOn, type PushHealth } from "../push.ts";
import { paths } from "../route.ts";
import { useApp } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";

export function Setup() {
  const { me, setMe, navigate } = useApp();
  const { health, recheck } = usePushHealth(me);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const host = hostName();
  const support = pushSupport();
  const https = location.protocol === "https:";

  const enable = async () => {
    if (!me) return;
    setBusy(true);
    setProblem(undefined);
    try {
      setMe({ ...me, push: await turnOn(me) });
      recheck();
    } catch (error) {
      setProblem(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AppShell title="Set up this phone">
      <div className="pad stack">
        <ul className="checks">
          <Check
            ok={!!me}
            label={me ? `Paired with ${host}` : `Not connected to ${host}`}
            detail={
              me
                ? `as ${me.device.name}`
                : "Pairing is checked again when it answers."
            }
          />
          <Check
            ok={standalone()}
            label="On the Home Screen"
            detail={
              standalone()
                ? "Opened as an installed app"
                : "iOS needs this for notifications"
            }
          />
          <Check
            ok={https}
            label="HTTPS through tailscale serve"
            detail={location.host}
          />
          <Check
            ok={health === "on"}
            label="Notifications"
            detail={notificationDetail(health, support)}
          />
        </ul>
        <p className="sm">
          Notifications come through the phone's push service, so they reach you
          on or off the tailnet. Each one names the factory and the kind of
          item. The details load when you open the item on the tailnet.
        </p>
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
            className="btn ghost"
            onClick={() => navigate(paths.list())}
          >
            {health === "on" ? "Done" : "Not now"}
          </button>
          {health === "on" ? null : (
            <button
              type="button"
              className="btn pri"
              disabled={
                !me || busy || support !== "ready" || health === "blocked"
              }
              onClick={enable}
            >
              {busy ? "Turning on…" : "Turn on"}
            </button>
          )}
        </footer>
      </div>
    </AppShell>
  );
}

function notificationDetail(
  health: PushHealth | undefined,
  support: ReturnType<typeof pushSupport>,
): string {
  if (support === "install-first")
    return "Add factory to the Home Screen first. iOS 16.4 or later.";
  if (support === "unsupported")
    return "This browser can't receive notifications.";
  switch (health) {
    case "on":
      return "on";
    case "blocked":
      return "Blocked. Allow them in the phone's settings for factory, then come back.";
    case "expired":
      return "The subscription expired. Turn them on again.";
    case undefined:
      return "checking…";
  }
  return "off";
}

export function Check(props: {
  ok: boolean;
  label: ReactNode;
  detail?: ReactNode;
  warn?: boolean;
  children?: ReactNode;
}) {
  const [glyph, tone] = props.ok
    ? ["✓", "grn"]
    : props.warn
      ? ["!", "yel"]
      : ["○", "org"];
  return (
    <li>
      <span className={`g ${tone}`} aria-hidden="true">
        {glyph}
      </span>
      <span className="l">{props.label}</span>
      {props.detail ? (
        <span className={props.warn ? "fix" : "d"}>{props.detail}</span>
      ) : null}
      {props.children}
    </li>
  );
}
