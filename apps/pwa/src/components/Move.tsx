import { useEffect, useState } from "react";
import { nodeApi, progressOf, warningOf } from "../api/client.ts";
import type {
  BatchProgress,
  ItemOutcome,
  LocalFactory,
  Progress,
  Readiness,
} from "../api/types.ts";
import { paths } from "../route.ts";
import { settledMove, useApp, type Move, type NodeView } from "../state.tsx";
import { ConfirmSheet, useSheet } from "./FactoryDetail.tsx";
import { Link } from "./Link.tsx";
import { factoryCount, Problem, ResultRow } from "./Nodes.tsx";
import { Check } from "./Setup.tsx";

const BATCH_POLL = 1_000;

const reason = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

const sentence = (text: string | undefined) =>
  text ? `${text.replace(/\.$/, "")}.` : "";

type Look = { glyph: string; tone: string; label: string; detail?: string };

export function describe(p: Progress, id: string): Look {
  const f = p.factory || id;
  const source = p.source_name || "its node";
  const target = p.target_name || "the other node";
  const stillRuns = p.forced ? "" : ` ${f} still runs on ${source}.`;
  switch (p.state) {
    case "checkpoint_waiting":
      return {
        glyph: "◷",
        tone: "yel",
        label: `Waiting for ${f} to reach a checkpoint`,
        detail: p.waiting,
      };
    case "preflight_failed":
      return {
        glyph: "✕",
        tone: "red",
        label: `${target} can't run ${f} yet`,
        detail: sentence(p.refusal) + stillRuns,
      };
    case "refused":
      return {
        glyph: "!",
        tone: "yel",
        label: `${f} was not moved`,
        detail: sentence(p.refusal) + stillRuns,
      };
    case "cancelled":
      return {
        glyph: "○",
        tone: "dk",
        label: "The move was cancelled",
        detail: stillRuns.trim(),
      };
    case "staged":
      return {
        glyph: "◌",
        tone: "cyn",
        label: `${target} has the checkpoint`,
        detail: p.forced
          ? `Starting ${f} on ${target}.`
          : `Stopping ${f} on ${source}.`,
      };
    case "released":
      return {
        glyph: "◌",
        tone: "cyn",
        label: `${f} is stopped on ${source}`,
        detail: `${target} is starting it.`,
      };
    case "recovery_needed":
    case "interrupted":
      return {
        glyph: "!",
        tone: "red",
        label: `${f} stopped mid-move`,
        detail: sentence(p.failure),
      };
    case "active":
      return {
        glyph: "✓",
        tone: "grn",
        label: `${f} runs on ${target} now`,
        detail: p.forced
          ? undefined
          : `Its files stay on ${source} until you delete them there.`,
      };
  }
  if (!p.factory)
    return { glyph: "◌", tone: "cyn", label: "Starting the move…" };
  if (p.forced)
    return {
      glyph: "◌",
      tone: "cyn",
      label: `${target} is taking ${f} over from its checkpoint`,
    };
  return {
    glyph: "◌",
    tone: "cyn",
    label:
      p.phase === "checkpoint"
        ? `Sending the checkpoint of ${f} to ${target}`
        : `Checking that ${target} can run ${f}`,
  };
}

const cancellable = ({ progress: p, takeover }: Move) =>
  !takeover &&
  !settledMove(p) &&
  p.phase !== "released" &&
  p.phase !== "active";

export function MoveProgress({ move }: { move: Move }) {
  const { track, snapshot } = useApp();
  const [problem, setProblem] = useState<string>();
  const p = move.progress;
  const look = describe(p, move.id);
  const arrived =
    p.state === "active"
      ? snapshot?.factories.find(
          (f) => f.incarnation === move.incarnation && f.route !== move.route,
        )
      : undefined;
  const cancel = async () => {
    setProblem(undefined);
    try {
      track({
        ...move,
        progress: await nodeApi(move.origin).cancel(p.operation),
      });
    } catch (error) {
      const later = progressOf(error);
      if (later) track({ ...move, progress: later });
      setProblem(reason(error));
    }
  };
  const failing = (p.checks ?? []).filter((c) => c.mark !== "ok");
  return (
    <section className="pad stack" aria-label={`Moving ${move.id}`}>
      <ul className="checks">
        <Check
          ok={p.state === "active"}
          look={[look.glyph, look.tone]}
          label={look.label}
          detail={look.detail}
        />
        {failing.map((check) => (
          <ResultRow key={check.name} check={check} on={p.target_name ?? ""} />
        ))}
      </ul>
      {p.recovery?.length ? (
        <ol className="steps">
          {p.recovery.map((step) => (
            <li key={step}>
              <span>{step}</span>
            </li>
          ))}
        </ol>
      ) : null}
      {arrived ? (
        <Link className="btn wide" href={paths.factory(arrived.route)}>
          Open {move.id} on {p.target_name}
          <span aria-hidden="true">›</span>
        </Link>
      ) : null}
      {cancellable(move) ? (
        <button type="button" className="btn warn" onClick={cancel}>
          Cancel the move
        </button>
      ) : null}
      {problem ? <Problem>{problem}</Problem> : null}
    </section>
  );
}

export function Moving() {
  const { moves, snapshot } = useApp();
  return moves
    .filter(
      (m) => !snapshot?.factories.some((f) => f.incarnation === m.incarnation),
    )
    .map((m) => {
      const look = describe(m.progress, m.id);
      return (
        <Link
          key={m.progress.operation}
          className="banner info"
          href={paths.factory(m.route)}
        >
          <span className="gl" aria-hidden="true">
            ⇄
          </span>
          <span className="bh">{look.label}</span>
          <span className="x">{look.detail} ›</span>
        </Link>
      );
    });
}

function useReadiness(targets: NodeView[]) {
  const [reports, setReports] = useState<Record<string, Readiness>>({});
  const asked = JSON.stringify(
    targets.filter((n) => n.live).map((n) => [n.id, n.origin]),
  );
  useEffect(() => {
    let current = true;
    for (const [id, origin] of JSON.parse(asked) as [string, string][])
      nodeApi(origin)
        .readiness([])
        .then(
          (report) =>
            current && setReports((all) => ({ ...all, [id]: report })),
          () => undefined,
        );
    return () => {
      current = false;
    };
  }, [asked]);
  return reports;
}

function unavailable(node: NodeView, report: Readiness | undefined) {
  if (node.lost) return `${node.short} can't be reached`;
  if (!node.live) return `waiting for ${node.short} to answer`;
  if (!report) return `asking ${node.short}…`;
  const failing = report.checks?.find((c) => c.mark === "fail");
  if (report.problems > 0 || failing)
    return `can't run factories yet: ${failing ? `${failing.name}: ${failing.detail}` : `${report.problems} to fix`}`;
  return undefined;
}

function PickSheet(props: {
  factory: LocalFactory;
  holder: NodeView;
  targets: NodeView[];
  onCancel: () => void;
  onPick: (target: NodeView) => void;
}) {
  const { factory, holder, targets } = props;
  const { dialog, cancel } = useSheet();
  const reports = useReadiness(targets);
  const [chosen, setChosen] = useState<NodeView>();
  const takeover = holder.lost;
  return (
    <dialog
      ref={dialog}
      className="sheet"
      aria-labelledby="move-title"
      onCancel={props.onCancel}
    >
      <h2 id="move-title">
        <span className="yel" aria-hidden="true">
          ⇄{" "}
        </span>
        {takeover ? `Take ${factory.id} over` : `Move ${factory.id}`}
      </h2>
      <p className="sm">
        {takeover
          ? `${holder.short} can't be reached. A node that kept the last checkpoint of ${factory.id} can take it over from there.`
          : `${holder.short} stops ${factory.id} at a checkpoint, and the node you pick resumes it with /factory resume.`}
      </p>
      <div className="opts flush">
        {targets.map((node) => {
          const why = unavailable(node, reports[node.id]);
          return (
            <button
              key={node.id}
              type="button"
              className="opt"
              aria-pressed={chosen?.id === node.id}
              disabled={!!why}
              onClick={() => setChosen(node)}
            >
              <span className="mk" aria-hidden="true" />
              <span>{node.short}</span>
              <span className="rec">{why ?? "ready"}</span>
            </button>
          );
        })}
      </div>
      <div className="row2">
        <button
          ref={cancel}
          type="button"
          className="btn"
          onClick={props.onCancel}
        >
          Cancel
        </button>
        <button
          type="button"
          className="btn pri"
          disabled={!chosen}
          onClick={() => chosen && props.onPick(chosen)}
        >
          {chosen
            ? `${takeover ? "Take over on" : "Move to"} ${chosen.short}`
            : takeover
              ? "Take over"
              : "Move"}
        </button>
      </div>
    </dialog>
  );
}

type Warning = { target: NodeView; operation: string; text: string };

export function MoveTo({ factory }: { factory: LocalFactory }) {
  const { nodeOf, nodes, moves, track } = useApp();
  const holder = nodeOf(factory);
  const [picking, setPicking] = useState(false);
  const [warning, setWarning] = useState<Warning>();
  const [problem, setProblem] = useState<string>();
  const targets = nodes.filter((n) => n.id !== holder.id);
  const moving = moves.some(
    (m) => m.incarnation === factory.incarnation && !settledMove(m.progress),
  );
  const follow = (origin: string, progress: Progress) =>
    track({
      incarnation: factory.incarnation,
      id: factory.id,
      route: factory.route,
      origin,
      takeover: holder.lost,
      progress,
    });
  const start = async (target: NodeView) => {
    setPicking(false);
    setProblem(undefined);
    try {
      if (!holder.lost) {
        follow(
          holder.origin,
          await nodeApi(holder.origin).move(factory.key, target.id),
        );
        return;
      }
      const operation = crypto.randomUUID();
      try {
        follow(
          target.origin,
          await nodeApi(target.origin).takeover(
            operation,
            factory.incarnation,
            false,
          ),
        );
      } catch (error) {
        const unconfirmed = warningOf(error);
        if (!unconfirmed) throw error;
        setWarning({ target, operation, text: unconfirmed.warning });
      }
    } catch (error) {
      setProblem(reason(error));
    }
  };
  const confirm = async (w: Warning) => {
    setWarning(undefined);
    try {
      follow(
        w.target.origin,
        await nodeApi(w.target.origin).takeover(
          w.operation,
          factory.incarnation,
          true,
        ),
      );
    } catch (error) {
      setProblem(reason(error));
    }
  };
  const ready = holder.live || holder.lost;
  return (
    <>
      <button
        type="button"
        className="btn"
        disabled={
          !ready || moving || targets.length === 0 || !!factory.unverified
        }
        onClick={() => setPicking(true)}
      >
        <span aria-hidden="true">⇄ </span>
        {holder.lost ? "Take over…" : "Move to…"}
      </button>
      {problem ? <Problem>{problem}</Problem> : null}
      {picking ? (
        <PickSheet
          factory={factory}
          holder={holder}
          targets={targets}
          onCancel={() => setPicking(false)}
          onPick={start}
        />
      ) : null}
      {warning ? (
        <ConfirmSheet
          id="takeover-title"
          glyph="!"
          title={`Take ${factory.id} over on ${warning.target.short}?`}
          confirm="Take over anyway"
          onCancel={() => setWarning(undefined)}
          onConfirm={() => confirm(warning)}
          note="Nothing changes until you tap Take over anyway."
        >
          {warning.text}
        </ConfirmSheet>
      ) : null}
    </>
  );
}

function outcome(item: ItemOutcome, target: string) {
  switch (item.state) {
    case "active":
      return `moved to ${target}`;
    case "queued":
      return "waiting its turn";
    case "cancelled":
      return "not moved: cancelled";
    case "refused":
    case "preflight_failed":
      return `not moved: ${item.reason ?? item.state}`;
    case "recovery_needed":
    case "interrupted":
      return `stopped mid-move: ${item.reason ?? item.state}`;
  }
  return item.progress
    ? describe(item.progress, item.factory).label
    : item.state;
}

const outcomeLooks: Record<string, [string, string]> = {
  active: ["✓", "grn"],
  refused: ["!", "yel"],
  preflight_failed: ["!", "yel"],
  cancelled: ["○", "dk"],
  recovery_needed: ["!", "red"],
  interrupted: ["!", "red"],
};

export function MoveAll({ node }: { node: NodeView }) {
  const { nodes } = useApp();
  const home = nodes[0];
  const [asking, setAsking] = useState(false);
  const [batch, setBatch] = useState<BatchProgress>();
  const [problem, setProblem] = useState<string>();
  const count = home.snapshot?.factories.length ?? 0;
  useEffect(() => {
    if (!batch?.running) return;
    const timer = setTimeout(
      () =>
        nodeApi(home.origin)
          .batch(batch.operation)
          .then(setBatch, (error) => setProblem(reason(error))),
      BATCH_POLL,
    );
    return () => clearTimeout(timer);
  }, [batch, home.origin]);
  const start = async () => {
    setAsking(false);
    setProblem(undefined);
    try {
      setBatch(await nodeApi(home.origin).moveAll(node.id));
    } catch (error) {
      setProblem(reason(error));
    }
  };
  const cancel = async () => {
    if (!batch) return;
    try {
      setBatch(await nodeApi(home.origin).cancelAll(batch.operation));
    } catch (error) {
      setProblem(reason(error));
    }
  };
  return (
    <>
      {batch ? (
        <ul className="checks" aria-label={`Moving to ${batch.target_name}`}>
          {batch.items.map((item) => (
            <Check
              key={item.key}
              ok={item.state === "active"}
              look={outcomeLooks[item.state] ?? ["◌", "cyn"]}
              label={item.factory}
              detail={outcome(item, batch.target_name)}
            />
          ))}
        </ul>
      ) : null}
      {batch?.running ? (
        <button type="button" className="btn warn" onClick={cancel}>
          Cancel the moves not released yet
        </button>
      ) : (
        <button
          type="button"
          className="btn"
          disabled={!home.live || !node.live || count === 0}
          onClick={() => setAsking(true)}
        >
          <span aria-hidden="true">⇄ </span>Move all from {home.short} to{" "}
          {node.short}…
        </button>
      )}
      {problem ? <Problem>{problem}</Problem> : null}
      {asking ? (
        <ConfirmSheet
          id="move-all-title"
          glyph="⇄"
          title={`Move all to ${node.short}?`}
          confirm="Move all"
          onCancel={() => setAsking(false)}
          onConfirm={start}
          note="A move not yet released can be cancelled. Moves that finished stay finished."
        >
          Each of the {factoryCount(count)} on {home.short} stops at a
          checkpoint and resumes on {node.short}, one at a time. Those that
          can't move stay on {home.short} and say why.
        </ConfirmSheet>
      ) : null}
    </>
  );
}
