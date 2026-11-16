import { useEffect, useRef, useState } from "react";
import type { LocalFactory, Receipt } from "../api/types.ts";
import { clock, hostName, kindLook, statusLook } from "../format.ts";
import { useBlocked, useNow, useSend } from "../hooks.ts";
import { age } from "../order.ts";
import { paths, type Tab } from "../route.ts";
import { useApp } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";
import { Link } from "./Link.tsx";
import { Pages } from "./Pages.tsx";
import { ReceiptLine } from "./Receipt.tsx";
import { SupervisorMessage } from "./SupervisorMessage.tsx";

export function FactoryDetail({ route, tab }: { route: string; tab: Tab }) {
  const { snapshot, reach } = useApp();
  const factory = snapshot?.factories.find((f) => f.route === route);
  if (!factory)
    return (
      <AppShell back={paths.list()}>
        <p className="empty hint">
          {!snapshot && (reach === "loading" || reach === "online")
            ? "Loading…"
            : `This factory is not on ${hostName()}.`}
        </p>
      </AppShell>
    );
  return (
    <AppShell back={paths.list()} title={<Title factory={factory} />}>
      <Head key={factory.incarnation} factory={factory} />
      <Tabs factory={factory} tab={tab} />
      {tab === "attention" ? <Attention factory={factory} /> : null}
      {tab === "pages" ? <Pages factory={factory} /> : null}
      {tab === "message" ? (
        <SupervisorMessage key={factory.incarnation} factory={factory} />
      ) : null}
    </AppShell>
  );
}

export function Title({ factory }: { factory: LocalFactory }) {
  return (
    <>
      <span className="mute">{factory.repo_name} / </span>
      {factory.id}
    </>
  );
}

function Head({ factory }: { factory: LocalFactory }) {
  const now = useNow();
  const look = statusLook(factory.status);
  const blocked = useBlocked();
  const { receipt, sending, send } = useSend(factory);
  const [asking, setAsking] = useState(false);
  const [wakingFrom, setWakingFrom] = useState<string>();
  const waking =
    wakingFrom === factory.status && factory.can_wake && !failed(receipt);
  const wake = () => {
    setWakingFrom(factory.status);
    send({ action: "wake" });
  };
  const pause = () => {
    setAsking(false);
    send({ action: "pause" });
  };
  return (
    <div className="fhead">
      <p className="l1">
        <span className={look.tone} aria-hidden="true">
          {look.glyph}
        </span>
        <span className="id">{factory.id}</span>
        <span className={`w ${look.tone}`}>{factory.status}</span>
      </p>
      {factory.title ? <p className="prompt">{factory.title}</p> : null}
      <p className="l3">
        {[factory.stage, age(factory.needs_since, now)]
          .filter(Boolean)
          .join(" · ")}
      </p>
      {factory.can_pause || factory.can_wake ? (
        <div className="acts">
          {factory.can_wake ? (
            <button
              type="button"
              className="btn pri"
              disabled={!!blocked || waking || sending}
              onClick={wake}
            >
              <span aria-hidden="true">↯ </span>
              {waking ? "Waking…" : `Wake ${factory.id}`}
            </button>
          ) : null}
          {factory.can_pause ? (
            <button
              type="button"
              className="btn"
              disabled={!!blocked || sending}
              onClick={() => setAsking(true)}
            >
              <span aria-hidden="true">‖ </span>Pause
            </button>
          ) : null}
        </div>
      ) : null}
      {blocked && (factory.can_pause || factory.can_wake) ? (
        <p className="xs mute">{blocked}</p>
      ) : null}
      {receipt && failed(receipt) ? <ReceiptLine receipt={receipt} /> : null}
      {asking ? (
        <PauseDialog
          factory={factory}
          onCancel={() => setAsking(false)}
          onPause={pause}
        />
      ) : null}
    </div>
  );
}

const failed = (receipt: Receipt | undefined) =>
  receipt?.status === "refused" ||
  receipt?.status === "unknown" ||
  receipt?.status === "stale";

function PauseDialog(props: {
  factory: LocalFactory;
  onCancel: () => void;
  onPause: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!dialog.current?.open) dialog.current?.showModal();
    cancel.current?.focus();
  }, []);
  const { factory } = props;
  const name = factory.title
    ? `${factory.id} · ${factory.title.replace(/\.+$/, "")}`
    : factory.id;
  return (
    <dialog
      ref={dialog}
      className="sheet"
      aria-labelledby="pause-title"
      onCancel={props.onCancel}
    >
      <h2 id="pause-title">
        <span className="yel" aria-hidden="true">
          ‖{" "}
        </span>
        Pause {factory.id}?
      </h2>
      <p className="sm">
        Stops Claude Code and closes the pane for {name}. The worktree, its
        branch and <code>.claude/factory/{factory.id}</code> stay. Wake brings
        it back.
      </p>
      <p className="sm mute">
        You get no notifications from it while it is paused.
      </p>
      <div className="row2">
        <button
          ref={cancel}
          type="button"
          className="btn"
          onClick={props.onCancel}
        >
          Cancel
        </button>
        <button type="button" className="btn warn" onClick={props.onPause}>
          <span aria-hidden="true">‖ </span>Pause
        </button>
      </div>
    </dialog>
  );
}

function Tabs({ factory, tab }: { factory: LocalFactory; tab: Tab }) {
  const waiting = factory.items.length > 0 && tab !== "attention";
  const links: [Tab, string, string, string][] = [
    ["attention", "◆", "Attention", String(factory.items.length)],
    ["pages", "▤", "Pages", String(factory.pages.length)],
    ["message", "›", "Message", ""],
  ];
  return (
    <nav className="tabs" aria-label={factory.id}>
      {links.map(([name, glyph, label, count]) => (
        <Link
          key={name}
          href={paths.factory(factory.route, name)}
          aria-current={tab === name ? "page" : undefined}
          className={name === "attention" && waiting ? "hot" : undefined}
        >
          <span aria-hidden="true">{glyph}</span>
          {label}
          {count ? ` ${count}` : ""}
        </Link>
      ))}
    </nav>
  );
}

function Attention({ factory }: { factory: LocalFactory }) {
  const now = useNow();
  if (factory.items.length === 0)
    return (
      <div className="empty">
        <p className="ink">Nothing needs you here.</p>
        <p className="hint sm">
          Open items appear here when the supervisor asks, a gate lights, a cap
          trips, a stage fails twice, or the session waits on a permission
          prompt.
        </p>
      </div>
    );
  return (
    <ul className="items">
      {factory.items.map((item) => {
        const look = kindLook(item.kind);
        return (
          <li key={item.key}>
            <Link className="it" href={paths.item(factory.route, item.key)}>
              <span className={`ic ${look.tone}`} aria-hidden="true">
                {look.glyph}
              </span>
              <span className={`w ${look.tone}`}>
                {[item.kind, item.id].filter(Boolean).join(" · ")}
              </span>
              <span className="age">
                {clock(item.since)} · {age(item.since, now)}
              </span>
              <span className="t">{item.title}</span>
              {item.body ? <span className="s">{item.body}</span> : null}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
