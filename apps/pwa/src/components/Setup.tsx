import { useState, type ReactNode } from "react";
import { hostName } from "../format.ts";
import { usePushHealth } from "../hooks.ts";
import { pushSupport, standalone, turnOn, type PushHealth } from "../push.ts";
import { paths } from "../route.ts";
import { useApp } from "../state.tsx";
import { AppShell, Closed } from "./AppShell.tsx";

export function Setup() {
  const { me, installation, navigate } = useApp();
  const { health, push, setPush, recheck } = usePushHealth();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const host = hostName();
  const support = pushSupport();
  const https = location.protocol === "https:";

  const enable = async () => {
    if (!push || !installation) return;
    setBusy(true);
    setProblem(undefined);
    try {
      setPush(await turnOn(push.vapid_public_key, installation));
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
            label={
              me ? `Signed in as ${me.caller}` : `Not connected to ${host}`
            }
            detail={
              me
                ? `Tailscale on this phone tells ${host} it's you. No code, no pairing.`
                : `Open factory on a phone signed in to Tailscale as ${host}'s owner.`
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
            label="HTTPS through Tailscale Serve"
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
                !push || busy || support !== "ready" || health === "blocked"
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

export function Exposed() {
  const { exposure } = useApp();
  const host = hostName();
  const fixes = exposure?.fixes ?? [];
  return (
    <Closed label={`${host} refuses every request until Funnel is off`}>
      <h1>
        {fixes.length
          ? `Funnel is on for ${host}`
          : `${host} can't confirm that Funnel is off`}
      </h1>
      <p>
        Anyone on the internet might reach {host}'s node, so it refuses every
        request until Funnel is off. Nothing from {host} is shown here.
      </p>
      <ul className="checks">
        <Check ok={false} warn label="funnel" detail={exposure?.error} />
        {fixes.map((fix) => (
          <Check
            key={fix}
            ok={false}
            warn
            label={`fix on ${host}`}
            detail={<code>{fix}</code>}
          />
        ))}
      </ul>
      <p className="sm mute">
        factory asks again every 15s and opens by itself once Funnel is off.
      </p>
    </Closed>
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
  look?: [string, string];
  children?: ReactNode;
}) {
  const [glyph, tone] =
    props.look ??
    (props.ok ? ["✓", "grn"] : props.warn ? ["!", "yel"] : ["○", "org"]);
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
