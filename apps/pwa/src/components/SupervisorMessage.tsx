import { useEffect, useState } from "react";
import { nodeApi } from "../api/client.ts";
import { cache, type MessageResult } from "../api/cache.ts";
import { MAX_MESSAGE, type LocalFactory, type Receipt } from "../api/types.ts";
import { clock } from "../format.ts";
import { useBlocked, useNode, useSend } from "../hooks.ts";
import { Blocked, SendLine } from "./Receipt.tsx";

export function SupervisorMessage({ factory }: { factory: LocalFactory }) {
  const { scope: account, live, origin } = useNode(factory);
  const blocked = useBlocked(factory);
  const { sending, send } = useSend(factory);
  const [text, setText] = useState("");
  const [results, setResults] = useState<MessageResult[]>([]);

  useEffect(() => {
    if (!account) return;
    let current = true;
    cache.messages(account, factory.incarnation).then(async (saved) => {
      if (!current) return;
      setResults(saved);
      if (!live) return;
      const fresh = await Promise.all(saved.map(refresh(origin)));
      if (current) setResults(fresh);
    });
    return () => {
      current = false;
    };
  }, [account, factory.incarnation, live, origin]);

  const submit = async () => {
    const sent = text;
    const receipt = await send({ action: "message", text: sent });
    const result = { text: sent, receipt, at: new Date().toISOString() };
    setResults((recent) => [result, ...recent].slice(0, 3));
    if (receipt.status !== "refused") setText("");
    if (account) await cache.addMessage(account, factory.incarnation, result);
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
      {results.length > 0 ? (
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

const refresh =
  (origin: string) =>
  async (result: MessageResult): Promise<MessageResult> => {
    const pending =
      result.receipt.status === "queued" || result.receipt.status === "unknown";
    if (!pending || !result.receipt.operation) return result;
    try {
      return {
        ...result,
        receipt: await nodeApi(origin).receipt(result.receipt.operation),
      };
    } catch {
      return result;
    }
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
