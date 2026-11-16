import { useEffect, useState, type FormEvent } from "react";
import { api, HttpError } from "../api/client.ts";
import { hostName } from "../format.ts";
import { isIOS, standalone } from "../push.ts";
import { AppShell } from "./AppShell.tsx";

type InstallPrompt = Event & { prompt: () => Promise<void> };

type Stage =
  | { step: "ready" }
  | { step: "pairing" }
  | { step: "paired"; name: string }
  | { step: "spent" }
  | { step: "failed"; reason: string };

const isAndroid = () => /Android/.test(navigator.userAgent);

function defaultName(): string {
  const agent = navigator.userAgent;
  if (/iPad/.test(agent)) return "iPad";
  if (/iPhone|iPod/.test(agent)) return "iPhone";
  if (/Android/.test(agent)) return "Android phone";
  return "This browser";
}

const spent = (error: unknown) =>
  error instanceof HttpError && error.status === 410;

export function Pairing(props: { code: string }) {
  const [code] = useState(props.code);
  const [name, setName] = useState(defaultName);
  const [stage, setStage] = useState<Stage>({ step: "ready" });
  const [expiresAt, setExpiresAt] = useState<number>();
  const [install, setInstall] = useState<InstallPrompt>();

  useEffect(() => {
    history.replaceState(null, "", "/");
    const keep = (event: Event) => {
      event.preventDefault();
      setInstall(event as InstallPrompt);
    };
    addEventListener("beforeinstallprompt", keep);
    return () => removeEventListener("beforeinstallprompt", keep);
  }, []);

  useEffect(() => {
    api.pairStatus(code).then(
      (status) => setExpiresAt(Date.parse(status.expires_at)),
      (error) =>
        setStage(
          spent(error)
            ? { step: "spent" }
            : { step: "failed", reason: errorText(error) },
        ),
    );
  }, [code]);

  const pair = async (event: FormEvent) => {
    event.preventDefault();
    setStage({ step: "pairing" });
    try {
      const paired = await api.pair(code, name.trim() || defaultName());
      setStage({ step: "paired", name: paired.device.name });
    } catch (error) {
      setStage(
        spent(error)
          ? { step: "spent" }
          : { step: "failed", reason: errorText(error) },
      );
    }
  };

  if (stage.step === "spent") return <Spent />;
  if (stage.step === "paired")
    return <Paired name={stage.name} install={install} />;
  return (
    <div className="page pad stack">
      <h1>Pair this phone with {hostName()}</h1>
      <p>
        You opened the code the hub is showing. It pairs one phone, once
        {expiresAt ? (
          <>
            , and expires in{" "}
            <Countdown
              until={expiresAt}
              onEnd={() => setStage({ step: "spent" })}
            />
          </>
        ) : null}
        .
      </p>
      <p className="code" aria-label={`Pairing code ${code}`}>
        {code}
      </p>
      <form className="stack" onSubmit={pair}>
        <fieldset className="field">
          <legend>name in the hub's device list</legend>
          <input
            type="text"
            value={name}
            aria-label="Device name"
            maxLength={64}
            onChange={(event) => setName(event.target.value)}
          />
        </fieldset>
        <button
          type="submit"
          className="btn pri wide"
          disabled={stage.step === "pairing"}
        >
          {stage.step === "pairing" ? "Pairing…" : "Pair this phone"}
        </button>
      </form>
      {stage.step === "failed" ? (
        <p className="stat warn" role="status">
          <span className="gl" aria-hidden="true">
            !
          </span>
          <span>{stage.reason}</span>
        </p>
      ) : null}
      <p className="sm mute">
        {hostName()} keeps a credential for this phone and no one else. Revoke
        it from the hub at any time.
      </p>
    </div>
  );
}

const errorText = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

function Countdown(props: { until: number; onEnd: () => void }) {
  const [now, setNow] = useState(Date.now);
  const left = Math.max(0, Math.round((props.until - now) / 1000));
  const { onEnd } = props;
  useEffect(() => {
    if (left === 0) return onEnd();
    const timer = setTimeout(() => setNow(Date.now()), 1000);
    return () => clearTimeout(timer);
  }, [left, now, onEnd]);
  const seconds = String(left % 60).padStart(2, "0");
  return (
    <b className="ink num">
      {Math.floor(left / 60)}:{seconds}
    </b>
  );
}

function Spent() {
  return (
    <div className="page pad stack">
      <p className="big-g yel" aria-hidden="true">
        !
      </p>
      <h1>This pairing code was used or has expired.</h1>
      <p>Show a new one in the hub: Phone, then Pair.</p>
      <p className="sm mute">
        Each code pairs one phone, once, and expires 5 minutes after the hub
        shows it.
      </p>
    </div>
  );
}

function Paired(props: { name: string; install?: InstallPrompt }) {
  const proceed = () => location.assign("/");
  return (
    <div className="page pad stack">
      <p className="grn b" role="status">
        <span aria-hidden="true">✓ </span>Paired as {props.name}
      </p>
      {isIOS() && !standalone() ? (
        <IOSSteps />
      ) : isAndroid() && !standalone() ? (
        <AndroidInstall install={props.install} />
      ) : null}
      <button type="button" className="btn pri wide" onClick={proceed}>
        Open factory
      </button>
    </div>
  );
}

function IOSSteps() {
  return (
    <>
      <h1>Add factory to your Home Screen</h1>
      <p>
        iOS sends notifications only to apps on the Home Screen, from iOS 16.4.
      </p>
      <ol className="steps">
        <li>
          <span>
            Tap <b className="ink">Share</b> in Safari's toolbar.
          </span>
        </li>
        <li>
          <span>
            Choose <b className="ink">Add to Home Screen</b>, then{" "}
            <b className="ink">Add</b>.
          </span>
        </li>
        <li>
          <span>
            Open <b className="ink">factory</b> from the Home Screen. It
            finishes setting up there.
          </span>
        </li>
      </ol>
    </>
  );
}

function AndroidInstall({ install }: { install?: InstallPrompt }) {
  const [asked, setAsked] = useState(false);
  if (install && !asked)
    return (
      <>
        <h1>Install factory</h1>
        <p>Installed, it opens like an app and can show notifications.</p>
        <button
          type="button"
          className="btn wide"
          onClick={() => {
            setAsked(true);
            install.prompt();
          }}
        >
          Install factory
        </button>
      </>
    );
  return (
    <>
      <h1>Install factory</h1>
      <ol className="steps">
        <li>
          <span>
            Open Chrome's menu <b className="ink">⋮</b>.
          </span>
        </li>
        <li>
          <span>
            Choose <b className="ink">Install app</b> or{" "}
            <b className="ink">Add to Home screen</b>.
          </span>
        </li>
        <li>
          <span>
            Open <b className="ink">factory</b> from the Home screen.
          </span>
        </li>
      </ol>
    </>
  );
}

export function Unpaired() {
  const [entry, setEntry] = useState("");
  const host = hostName();
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const code = codeFrom(entry);
    if (code) location.assign(`/#pair=${encodeURIComponent(code)}`);
  };
  return (
    <AppShell>
      <div className="pad stack">
        <h2>This phone is not paired</h2>
        <p>
          {host} refused this phone's credential. Either it was revoked in the
          hub, or this phone never paired.
        </p>
        <p>
          The app deleted the factory list it had saved, and this phone gets no
          more notifications.
        </p>
        <section className="box">
          <h3 className="bt ink">pair again</h3>
          <ol className="steps">
            <li>
              <span>
                In the hub, open the <b className="ink">Phone</b> popup and
                choose <b className="ink">Pair</b>.
              </span>
            </li>
            <li>
              <span>Scan its QR code with the camera.</span>
            </li>
            <li>
              <span>
                Tap <b className="ink">Pair this phone</b>.
              </span>
            </li>
          </ol>
        </section>
        <form className="stack" onSubmit={submit}>
          <fieldset className="field">
            <legend>or paste a fresh code or pairing link</legend>
            <input
              type="text"
              value={entry}
              aria-label="Pairing code or link"
              autoCapitalize="characters"
              autoComplete="off"
              onChange={(event) => setEntry(event.target.value)}
            />
          </fieldset>
          <button
            type="submit"
            className="btn wide"
            disabled={!codeFrom(entry)}
          >
            Use this code
          </button>
        </form>
      </div>
    </AppShell>
  );
}

function codeFrom(entry: string): string {
  const text = entry.trim();
  const hash = text.indexOf("#");
  if (hash === -1) return text;
  return new URLSearchParams(text.slice(hash + 1)).get("pair") ?? "";
}
