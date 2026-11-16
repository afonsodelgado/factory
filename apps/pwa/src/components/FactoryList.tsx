import { useState } from "react";
import type { LocalFactory } from "../api/types.ts";
import { clock, hostName, itemTag, kindLook, statusLook } from "../format.ts";
import { usePushHealth, useNow } from "../hooks.ts";
import { age, group, needsYou } from "../order.ts";
import { paths } from "../route.ts";
import { useApp, type NodeView } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";
import { Link } from "./Link.tsx";

export function FactoryList({ selected }: { selected?: string }) {
  const { reach, snapshot } = useApp();
  return (
    <AppShell>
      <Joined />
      {snapshot ? (
        <Populated factories={snapshot.factories} selected={selected} />
      ) : reach === "loading" || reach === "online" ? (
        <Loading />
      ) : (
        <p className="empty hint">
          This phone has no saved list yet. It loads once {hostName()} answers.
        </p>
      )}
    </AppShell>
  );
}

function Joined() {
  const { joined, nodes } = useApp();
  return joined.map((name) => {
    const count =
      nodes.find((n) => n.short === name)?.snapshot?.factories.length ?? 0;
    return (
      <div key={name} className="banner info" role="status">
        <span className="gl" aria-hidden="true">
          ↔
        </span>
        <span className="bh">{name} joined your nodes.</span>
        <span className="x">
          {count
            ? `Its factories show here, marked ↔ ${name}.`
            : `It has no factories yet. Those you start or move there show here, marked ↔ ${name}.`}{" "}
          Nothing to set up on this phone.
        </span>
      </div>
    );
  });
}

function useLost() {
  const { nodeOf } = useApp();
  return (f: LocalFactory) => nodeOf(f).lost;
}

function Populated(props: { factories: LocalFactory[]; selected?: string }) {
  const { savedAt, multi, nodeOf } = useApp();
  const now = useNow();
  const lost = useLost();
  const groups = group(props.factories, lost);
  if (props.factories.length === 0)
    return (
      <>
        <Counts factories={props.factories} />
        <SetupNudge />
        <Empty />
        <Asking />
      </>
    );
  return (
    <nav aria-label="Factories">
      <Counts factories={props.factories} />
      <SetupNudge />
      {groups.needsYou.length > 0 ? (
        <section>
          <h2 className="grp">
            <span className="n org">
              <span aria-hidden="true">◆ </span>Needs you
            </span>
            <span className="p">
              {savedAt ? `as of ${clock(savedAt)}` : "longest wait first"}
            </span>
          </h2>
          <Rows
            factories={groups.needsYou}
            selected={props.selected}
            now={now}
          />
        </section>
      ) : (
        <p className="calm grn b">
          <span aria-hidden="true">✓ </span>Nothing needs you.
        </p>
      )}
      {groups.repos.map((repo) => {
        const node = multi ? nodeOf(repo.factories[0]) : undefined;
        return (
          <section key={`${repo.repo}:${repo.node ?? ""}`}>
            <h2 className="grp">
              <span className={`n${node?.lost ? " mute" : ""}`}>
                <span aria-hidden="true">▪ </span>
                {repo.name}
              </span>
              {node ? (
                <NodeMark node={node} now={now} />
              ) : (
                <span className="p">{repo.repo}</span>
              )}
            </h2>
            <Rows
              factories={repo.factories}
              selected={props.selected}
              now={now}
            />
          </section>
        );
      })}
      <Done factories={groups.done} selected={props.selected} now={now} />
      <Asking />
    </nav>
  );
}

function NodeMark({ node, now }: { node: NodeView; now: number }) {
  if (!node.lost) return <span className="nd">↔ {node.short}</span>;
  const lostFor = node.savedAt ? ` · lost ${age(node.savedAt, now)}` : "";
  return (
    <span className="nd lost">
      ↮ {node.short}
      {lostFor}
    </span>
  );
}

function Asking() {
  const { nodes, multi } = useApp();
  const asking = nodes.filter((n) => n.reach === "loading" && !n.snapshot);
  if (!multi || asking.length === 0) return null;
  return (
    <div aria-busy="true">
      <p className="grp">
        <span className="n mute">
          ◌ asking {asking.map((n) => n.short).join(", ")}…
        </span>
      </p>
      {[0, 1, 2].map((n) => (
        <div key={n} className="skel" aria-hidden="true">
          <i />
          <i />
          <i />
          <i className="w2" />
        </div>
      ))}
    </div>
  );
}

function Counts(props: { factories: LocalFactory[] }) {
  const lost = useLost();
  const factories = props.factories.filter((f) => !lost(f));
  const unreachable = props.factories.length - factories.length;
  const count = (words: string[]) =>
    factories.filter((f) => words.includes(f.status)).length;
  const need = factories.filter(needsYou).length;
  const others: [string, string, number][] = [
    ["grn", "● {n} running", count(["running"])],
    ["mag", "⟳ {n} waiting", count(["on CI", "on agent"])],
    ["yel", "☾ {n} asleep", count(["stalled", "exited", "gone"])],
    ["cyn", "◌ {n} starting", count(["starting"])],
    ["red", "✕ {n} failed", count(["failed to start", "unreachable"])],
    ["dk", "↮ {n} unreachable", unreachable],
  ];
  return (
    <p className="counts">
      <span className={need ? "org b" : "dk"}>◆ {need} need you</span>
      {others
        .filter(([, , n]) => n > 0)
        .map(([tone, label, n]) => (
          <span key={label} className={tone}>
            {label.replace("{n}", String(n))}
          </span>
        ))}
    </p>
  );
}

function SetupNudge() {
  const { health } = usePushHealth();
  if (!health || health === "on") return null;
  return (
    <Link className="banner info" href={paths.setup()}>
      <span className="gl" aria-hidden="true">
        ○
      </span>
      <span className="bh">Notifications are off on this phone.</span>
      <span className="x">
        Turn them on so you hear when something needs you ›
      </span>
    </Link>
  );
}

type RowsProps = { factories: LocalFactory[]; selected?: string; now: number };

function Rows(props: RowsProps) {
  return (
    <ul className="rows">
      {props.factories.map((f) => (
        <li key={f.key}>
          <Row
            factory={f}
            selected={props.selected === f.route}
            now={props.now}
          />
        </li>
      ))}
    </ul>
  );
}

const lostLook = { glyph: "↮", tone: "dk" };

function Row(props: { factory: LocalFactory; selected: boolean; now: number }) {
  const { factory, selected, now } = props;
  const { multi, nodeOf } = useApp();
  const node = nodeOf(factory);
  const lost = node.lost && factory.status !== "done";
  const look = lost ? lostLook : statusLook(factory.status);
  const waiting = needsYou(factory) && !lost;
  const stage = waiting
    ? [factory.repo_name, factory.stage].filter(Boolean).join(" · ")
    : factory.stage;
  const asOf =
    lost && node.savedAt
      ? [stage, `as of ${clock(node.savedAt)}`].filter(Boolean).join(", ")
      : stage;
  return (
    <Link
      className={`row${factory.status === "done" ? " done" : ""}${lost ? " lost" : ""}${selected ? " on" : ""}`}
      href={paths.factory(factory.route)}
      aria-current={selected ? "true" : undefined}
    >
      <span className={`ic ${look.tone}`} aria-hidden="true">
        {look.glyph}
      </span>
      <span className="id">{factory.id}</span>
      <span className={`w ${look.tone}`}>
        {lost ? "unreachable" : factory.status}
      </span>
      {factory.status === "done" ? null : (
        <span className="l2">
          {multi && waiting ? <span className="nd">↔ {node.short}</span> : null}
          <span className="stage">{asOf}</span>
          <span className="age">{age(factory.needs_since, now)}</span>
        </span>
      )}
      {factory.items.length > 0 ? (
        <span className="tags">
          {factory.items.map((item) => {
            const kind = kindLook(item.kind);
            return (
              <span key={item.key} className={`tag ${kind.tone}`}>
                <span aria-hidden="true">{kind.glyph}</span>
                {itemTag(item)}
              </span>
            );
          })}
        </span>
      ) : null}
    </Link>
  );
}

function Done(props: RowsProps) {
  const [open, setOpen] = useState(false);
  if (props.factories.length === 0) return null;
  return (
    <section>
      <button
        type="button"
        className="grp fold"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <span className="n dk">
          <span aria-hidden="true">{open ? "▾ " : "▸ "}</span>
          {props.factories.length} done
        </span>
      </button>
      {open ? <Rows {...props} /> : null}
    </section>
  );
}

function Loading() {
  return (
    <div aria-busy="true" aria-label="Loading factories">
      <p className="counts mute" aria-hidden="true">
        <span>◆ –</span>
        <span>● –</span>
        <span>⟳ –</span>
      </p>
      <p className="grp">
        <span className="n mute">loading from {hostName()}…</span>
      </p>
      {[0, 1, 2, 3, 4].map((n) => (
        <div key={n} className="skel" aria-hidden="true">
          <i />
          <i />
          <i />
          <i className="w2" />
        </div>
      ))}
    </div>
  );
}

function Empty() {
  const { multi, nodes } = useApp();
  const where = multi ? `your ${nodes.length} nodes` : hostName();
  return (
    <div className="empty">
      <h2>No factories on {where}</h2>
      <p>
        Factories are started from the hub, not from the phone. Press{" "}
        <code>n</code> in the hub, or run{" "}
        <code>factory open &lt;id&gt; &lt;prompt&gt;</code> in a repo.
      </p>
      <p className="hint">
        A new factory shows up here as soon as its supervisor starts, and you
        get a notification when it first needs you.
      </p>
      <p className="hint">
        Another machine joins with <code>factory node install</code> there. Its
        factories show here too, marked with its name.
      </p>
    </div>
  );
}
