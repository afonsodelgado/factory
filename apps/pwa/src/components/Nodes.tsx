import { useCallback, useEffect, useState, type ReactNode } from "react";
import { api, nodeApi } from "../api/client.ts";
import type {
  Candidate,
  Check as Result,
  CheckReport,
  Readiness,
} from "../api/types.ts";
import { clock } from "../format.ts";
import { paths } from "../route.ts";
import { useApp, type NodeView } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";
import { ConfirmSheet } from "./FactoryDetail.tsx";
import { Check } from "./Setup.tsx";

const marks: Record<string, [string, string]> = {
  ok: ["✓", "grn"],
  warn: ["!", "yel"],
  fail: ["✕", "red"],
  skip: ["○", "dk"],
};

export const factoryCount = (n: number) =>
  n === 1 ? "1 factory" : `${n} factories`;

const reason = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

const repoName = (url: string) =>
  (url.split("/").pop() ?? url).replace(/\.git$/, "");

const uncloned = (check: Result, repos: string[]) =>
  check.name === "repos"
    ? repos.filter((url) =>
        check.detail.includes(`${repoName(url)} not cloned`),
      )
    : [];

const phoneWording = (detail: string) =>
  detail.replace(/ not cloned yet;[^·]*?(?= · |$)/g, " isn't cloned yet");

function Problem({ children }: { children: ReactNode }) {
  return (
    <p className="stat warn" role="status">
      <span className="gl" aria-hidden="true">
        !
      </span>
      <span>{children}</span>
    </p>
  );
}

function ResultRow(props: { check: Result; on: string; children?: ReactNode }) {
  const { check } = props;
  return (
    <Check
      ok={check.mark === "ok"}
      look={marks[check.mark] ?? marks.skip}
      label={check.name}
      detail={
        <>
          {phoneWording(check.detail)}
          {check.fix ? (
            <>
              {" "}
              · on {check.on || props.on}: <code>{check.fix}</code>
            </>
          ) : null}
        </>
      }
    >
      {check.lines?.map((line) => (
        <span key={line} className="d">
          {line}
        </span>
      ))}
      {props.children}
    </Check>
  );
}

export function NodeDetail({ id }: { id: string }) {
  const { nodes, roster, navigate } = useApp();
  const node = nodes.find((n) => n.id === id);
  const [removing, setRemoving] = useState(false);
  const [problem, setProblem] = useState<string>();
  if (!node)
    return (
      <AppShell back={paths.phone()} title="Node">
        <p className="empty hint">This node is not in your node list.</p>
      </AppShell>
    );
  const listers = [
    "The hub",
    "this phone",
    ...nodes.filter((n) => !n.home && n.id !== id).map((n) => n.short),
  ];
  const count = node.snapshot?.factories.length ?? 0;
  const pending = roster?.members.find((m) => m.id === id)?.pending ?? 0;
  const remove = async () => {
    setRemoving(false);
    setProblem(undefined);
    try {
      await api.remove(id);
      navigate(paths.phone());
    } catch (error) {
      setProblem(reason(error));
    }
  };
  return (
    <AppShell back={paths.phone()} title={node.short}>
      <div className="pad stack">
        <ul className="checks">
          {node.lost ? <Unreachable node={node} /> : null}
          {pending > 0 ? (
            <Check
              ok={false}
              label={`${pending} list ${pending === 1 ? "change" : "changes"} not confirmed by ${node.short} yet`}
              detail="It gets them when it next answers."
            />
          ) : null}
        </ul>
        {node.live ? <NodeReadiness key={node.id} node={node} /> : null}
        {problem ? <Problem>{problem}</Problem> : null}
        <footer className="ab">
          <Again node={node} />
          <button
            type="button"
            className="btn warn"
            onClick={() => setRemoving(true)}
          >
            Remove
          </button>
        </footer>
      </div>
      {removing ? (
        <ConfirmSheet
          id="remove-title"
          glyph="✕"
          title={`Remove ${node.short}?`}
          confirm="Remove"
          onCancel={() => setRemoving(false)}
          onConfirm={remove}
          note={`When ${node.short} is back, it learns it was removed and stops notifying this phone.`}
        >
          {listers.slice(0, -1).join(", ")} and {listers.at(-1)} stop listing
          it. Its {factoryCount(count)} keep running there if it is on.
        </ConfirmSheet>
      ) : null}
    </AppShell>
  );
}

function Again({ node }: { node: NodeView }) {
  const { retry } = useApp();
  return (
    <button
      type="button"
      className="btn"
      disabled={node.reach === "loading"}
      onClick={retry}
    >
      Check again
    </button>
  );
}

function Unreachable({ node }: { node: NodeView }) {
  const factories = node.snapshot?.factories ?? [];
  const since = node.savedAt ? ` since ${clock(node.savedAt)}` : "";
  return (
    <>
      <Check
        ok={false}
        look={["↮", "dk"]}
        label={`Unreachable${since}`}
        detail={`No answer at ${node.origin || location.origin}. It may be asleep or off, or its node isn't running.`}
      />
      {factories.length ? (
        <Check
          ok={false}
          look={["◆", "org"]}
          label={`${factoryCount(factories.length)}${node.savedAt ? `, as of ${clock(node.savedAt)}` : ""}`}
          detail={factories.map((f) => `${f.id} ${f.status}`).join(" · ")}
        />
      ) : null}
    </>
  );
}

function NodeReadiness({ node }: { node: NodeView }) {
  const [repos, setRepos] = useState<string[]>([]);
  const [report, setReport] = useState<Readiness>();
  const [problem, setProblem] = useState<string>();
  const [cloning, setCloning] = useState<string>();
  const run = useCallback(async () => {
    setProblem(undefined);
    try {
      const wanted = Object.keys((await api.repos()) ?? {});
      setRepos(wanted);
      setReport(await nodeApi(node.origin).readiness(wanted));
    } catch (error) {
      setProblem(reason(error));
    }
  }, [node.origin]);
  useEffect(() => {
    run();
  }, [run]);
  const clone = async (url: string) => {
    setCloning(url);
    setProblem(undefined);
    try {
      await nodeApi(node.origin).clone(url);
      await run();
    } catch (error) {
      setProblem(`${repoName(url)} was not cloned: ${reason(error)}`);
    } finally {
      setCloning(undefined);
    }
  };
  const version = node.snapshot?.version;
  return (
    <>
      <ul className="checks">
        <Check
          ok
          look={["●", "grn"]}
          label="Running"
          detail={[
            version && `factory ${version}`,
            node.os,
            factoryCount(node.snapshot?.factories.length ?? 0),
            node.home && "the app lives here",
          ]
            .filter(Boolean)
            .join(" · ")}
        />
        {report ? (
          (report.checks ?? []).map((check, i) => (
            <ResultRow key={i} check={check} on={node.short}>
              {uncloned(check, repos).map((url) => (
                <button
                  key={url}
                  type="button"
                  className="btn fit"
                  disabled={!!cloning}
                  onClick={() => clone(url)}
                >
                  {cloning === url
                    ? `Cloning ${repoName(url)}…`
                    : `Clone ${repoName(url)} now, with ${node.short}'s git login`}
                </button>
              ))}
            </ResultRow>
          ))
        ) : (
          <Check
            ok={false}
            look={["◌", "cyn"]}
            label={`asking ${node.short}…`}
          />
        )}
      </ul>
      {problem ? <Problem>{problem}</Problem> : null}
    </>
  );
}

const homeName = (nodes: NodeView[]) => nodes[0].short;

function NeedsHome({ nodes }: { nodes: NodeView[] }) {
  if (nodes[0].live) return null;
  return (
    <Problem>
      Adding a node goes through {homeName(nodes)}, where this app lives, and{" "}
      {homeName(nodes)} isn't answering. Try again once it is back.
    </Problem>
  );
}

export function AddNode() {
  const { nodes, roster, navigate } = useApp();
  const [candidates, setCandidates] = useState<Candidate[]>();
  const [problem, setProblem] = useState<string>();
  const [picked, setPicked] = useState<string>();
  const [typed, setTyped] = useState("");
  const live = nodes[0].live;
  useEffect(() => {
    if (!live) return;
    let current = true;
    api.candidates().then(
      (found) => current && setCandidates(found.candidates ?? []),
      (error) => current && setProblem(reason(error)),
    );
    return () => {
      current = false;
    };
  }, [live]);
  const tailnet = roster?.members.find((m) => m.self)?.tailnet;
  const target = typed.trim() || picked;
  const chosen = candidates?.find((c) => c.name === target);
  return (
    <AppShell back={paths.phone()} title="Add a node">
      <div className="pad stack">
        <p className="sm">
          Your machines{tailnet ? ` on ${tailnet}` : ""}. {homeName(nodes)} asks
          each one; nothing changes until you add.
        </p>
        <NeedsHome nodes={nodes} />
        {candidates ? (
          <div className="opts flush" role="group" aria-label="Machines">
            {candidates.map((c) => (
              <button
                key={c.name}
                type="button"
                className="opt"
                aria-pressed={picked === c.name && !typed.trim()}
                disabled={!c.selectable}
                onClick={() => {
                  setPicked(c.name);
                  setTyped("");
                }}
              >
                <span className="mk" aria-hidden="true" />
                <span>{c.short}</span>
                <span className="rec">{candidateDetail(c)}</span>
              </button>
            ))}
            {candidates.length === 0 ? (
              <p className="sm mute">
                None of your other machines runs macOS or Linux on the tailnet.
              </p>
            ) : null}
          </div>
        ) : live && !problem ? (
          <p className="grp">
            <span className="n mute">◌ asking your machines…</span>
          </p>
        ) : null}
        {problem ? <Problem>{problem}</Problem> : null}
        <fieldset className="field">
          <legend>or type a tailnet name</legend>
          <input
            type="text"
            value={typed}
            placeholder="homelab or 100.101.7.12"
            aria-label="Tailnet name"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            onChange={(event) => setTyped(event.target.value)}
          />
        </fieldset>
        <footer className="ab">
          <button
            type="button"
            className="btn ghost"
            onClick={() => navigate(paths.phone())}
          >
            Cancel
          </button>
          <button
            type="button"
            className="btn pri"
            disabled={!target || !live}
            onClick={() => target && navigate(paths.check(target))}
          >
            Check {chosen?.short ?? target ?? ""}
          </button>
        </footer>
      </div>
    </AppShell>
  );
}

function candidateDetail(c: Candidate): string {
  if (c.selectable)
    return `● ${[`factory ${c.hello?.version ?? ""}`.trim(), c.os].join(" · ")}`;
  const fix = c.fix ? ` · on ${c.on}: ${c.fix}` : "";
  return c.detail + fix;
}

export function CheckNode({ name }: { name: string }) {
  const { nodes, navigate } = useApp();
  const [report, setReport] = useState<CheckReport>();
  const [problem, setProblem] = useState<string>();
  const [busy, setBusy] = useState(false);
  const live = nodes[0].live;
  const run = useCallback(async () => {
    setBusy(true);
    setReport(undefined);
    setProblem(undefined);
    try {
      setReport(await api.check(name));
    } catch (error) {
      setProblem(reason(error));
    } finally {
      setBusy(false);
    }
  }, [name]);
  useEffect(() => {
    if (live) run();
  }, [live, run]);
  const short = report?.target.short || name.split(".")[0];
  const refused = !!report?.refusal;
  const add = async () => {
    setBusy(true);
    setProblem(undefined);
    try {
      await api.add(name);
      navigate(paths.phone());
    } catch (error) {
      const body = (error as { body?: { report?: CheckReport } }).body;
      if (body?.report) setReport(body.report);
      setProblem(reason(error));
    } finally {
      setBusy(false);
    }
  };
  return (
    <AppShell back={paths.add()} title={`Add ${short}`}>
      <div className="pad stack">
        <NeedsHome nodes={nodes} />
        <ul className="checks">
          {report ? (
            (report.checks ?? []).map((check, i) => (
              <ResultRow key={i} check={check} on={short} />
            ))
          ) : busy ? (
            <Check ok={false} look={["◌", "cyn"]} label={`asking ${short}…`} />
          ) : null}
        </ul>
        {refused ? (
          <p className="sm mute">
            Adding never uses ssh, and nothing was changed on {short}.
          </p>
        ) : null}
        {problem && !refused ? <Problem>{problem}</Problem> : null}
        {report && !refused ? (
          <>
            <section className="box">
              <h3 className="bt ink">adding {short} also</h3>
              <ul className="also">
                <li>
                  <span className="cyn" aria-hidden="true">
                    ↔
                  </span>
                  <span>the hub and every node list it</span>
                </li>
                <li>
                  <span className="org" aria-hidden="true">
                    ◆
                  </span>
                  <span>
                    it keeps its own push key for now, so it can't notify this
                    phone yet
                  </span>
                </li>
                <li>
                  <span className="blu" aria-hidden="true">
                    ▣
                  </span>
                  <span>this app reaches it directly</span>
                </li>
              </ul>
            </section>
            {report.problems > 0 ? (
              <Problem>
                Added either way. Factories won't start or move to {short} until
                the{" "}
                {report.problems === 1
                  ? "check above passes"
                  : "checks above pass"}{" "}
                there.
              </Problem>
            ) : null}
          </>
        ) : null}
        <footer className="ab">
          <button
            type="button"
            className="btn"
            disabled={busy || !live}
            onClick={run}
          >
            Check again
          </button>
          {report && !refused ? (
            <button
              type="button"
              className="btn pri"
              disabled={busy || !live}
              onClick={add}
            >
              Add {short}
            </button>
          ) : null}
        </footer>
      </div>
    </AppShell>
  );
}
