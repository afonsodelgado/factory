import { useEffect, useState } from "react";
import { nodeApi } from "../api/client.ts";
import { cache, type MessageResult } from "../api/cache.ts";
import { MAX_MESSAGE, type LocalFactory, type Receipt } from "../api/types.ts";
import { clock } from "../format.ts";
import { useBlocked, useNode, useSend } from "../hooks.ts";
import { Blocked, SendLine } from "./Receipt.tsx";

export function SupervisorMessage({ factory }: { factory: LocalFactory }) {
  const { scope: account, live, origin, snapshot } = useNode(factory);
  const blocked = useBlocked(factory);
  const { sending, send } = useSend(factory);
  const [text, setText] = useState("");
  const scope = account && `${account}:${factory.incarnation}`;
  const [held, setHeld] = useState<Held>();
  const results = held && held.scope === scope ? held.results : undefined;
  useEffect(() => {
    if (!account || !scope) return;
    let current = true;
    cache.messages(account, factory.incarnation).then((saved) => {
      if (current)
        setHeld((now) =>
          now?.scope === scope ? now : { scope, results: saved },
        );
    });
    return () => {
      current = false;
    };
  }, [account, factory.incarnation, scope]);

  useEffect(() => {
    if (account && held && held.scope === scope)
      void cache.saveMessages(account, factory.incarnation, held.results);
  }, [account, factory.incarnation, scope, held]);

  const asking = (results ?? [])
    .filter(awaitsNode)
    .map((result) => result.receipt.operation)
    .join(" ");
  const revision = snapshot?.revision;
  useEffect(() => {
    if (!live || !asking || !scope) return;
    let current = true;
    Promise.all(asking.split(" ").map(lookup(origin))).then((fresh) => {
      if (!current) return;
      setHeld((now) =>
        now?.scope === scope
          ? { scope, results: now.results.map(merge(fresh)) }
          : now,
      );
    });
    return () => {
      current = false;
    };
  }, [asking, live, origin, revision, scope]);

  const submit = async () => {
    const sent = text;
    const receipt = await send({ action: "message", text: sent });
    const result = { text: sent, receipt, at: new Date().toISOString() };
    if (scope)
      setHeld((current) => ({
        scope,
        results: [
          result,
          ...(current?.scope === scope ? current.results : []),
        ].slice(0, 3),
      }));
    if (receipt.status !== "refused") setText("");
  };

  const disabled =
    !!blocked || !factory.message.ready || text.trim() === "" || sending;
  const label = factory.message.queues ? "Queue message" : "Send";
  return (
    <div className="pad stack">
      <fieldset className="field">
        <legend>to the supervisor</legend>
        <textarea
          rows={5}
          value={text}
          maxLength={MAX_MESSAGE}
          placeholder="Ask, add scope or decide something"
          aria-label={`Message to the ${factory.id} supervisor`}
          onChange={(event) => setText(event.target.value)}
        />
        <p className="meta">
          <span>Slash commands are sent as typed.</span>
          <span className="num">
            {text.length} / {MAX_MESSAGE}
          </span>
        </p>
      </fieldset>
      <SendLine send={factory.message} />
      <Blocked reason={blocked} />
      {results?.length ? (
        <section>
          <h2 className="sub">Sent from this phone</h2>
          <ul className="checks">
            {results.map((result) => (
              <Result
                key={result.receipt.operation ?? result.at}
                result={result}
                onRestore={setText}
              />
            ))}
          </ul>
        </section>
      ) : null}
      <footer className="ab">
        <button
          type="button"
          className="btn pri"
          disabled={disabled}
          onClick={submit}
        >
          {label}
        </button>
      </footer>
    </div>
  );
}

type Held = { scope: string; results: MessageResult[] };

const REPLY_WAIT = 24 * 60 * 60_000;

const awaitsNode = ({ receipt, at }: MessageResult) => {
  const pending = ["queued", "sending", "unknown"].includes(receipt.status);
  const awaitsReply =
    receipt.status === "delivered" &&
    !receipt.reply &&
    Date.now() - Date.parse(at) < REPLY_WAIT;
  return !!receipt.operation && (pending || awaitsReply);
};

const lookup =
  (origin: string) =>
  async (operation: string): Promise<Receipt | undefined> => {
    try {
      return await nodeApi(origin).receipt(operation);
    } catch {
      return undefined;
    }
  };

const merge =
  (fresh: (Receipt | undefined)[]) =>
  (result: MessageResult): MessageResult => {
    const found = fresh.find(
      (receipt) => receipt?.operation === result.receipt.operation,
    );
    if (!found) return result;
    const delivered = result.receipt.status === "delivered";
    return {
      ...result,
      receipt: delivered ? { ...result.receipt, reply: found.reply } : found,
    };
  };

const resultLooks: Record<Receipt["status"], [string, string, string]> = {
  delivered: ["✓", "grn", "Delivered"],
  queued: ["◷", "cyn", "Queued"],
  sending: ["◌", "cyn", "Sending"],
  refused: ["!", "yel", "Refused"],
  stale: ["!", "yel", "Refused"],
  unknown: ["?", "yel", "Not confirmed"],
};

function Result(props: {
  result: MessageResult;
  onRestore: (text: string) => void;
}) {
  const { result } = props;
  const [glyph, tone, label] = resultLooks[result.receipt.status];
  const refused =
    result.receipt.status === "refused" || result.receipt.status === "stale";
  return (
    <li>
      <span className={`g ${tone}`} aria-hidden="true">
        {glyph}
      </span>
      <span className={`l ${tone}`}>
        {label} {clock(result.at)}
      </span>
      <span className="d">{result.text}</span>
      {result.receipt.reply ? (
        <figure className="reply">
          <figcaption>
            Supervisor replied {clock(result.receipt.reply.at)}
          </figcaption>
          <p>{result.receipt.reply.text}</p>
        </figure>
      ) : null}
      {result.receipt.reason ? (
        <span className={refused ? "fix" : "d"}>{result.receipt.reason}</span>
      ) : null}
      {refused ? (
        <button
          type="button"
          className="btn quiet sm"
          onClick={() => props.onRestore(result.text)}
        >
          Put the text back in the box
        </button>
      ) : null}
    </li>
  );
}
