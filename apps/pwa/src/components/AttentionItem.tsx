import { useEffect, useState, type ReactNode } from "react";
import { nodeApi } from "../api/client.ts";
import type { Draft } from "../api/cache.ts";
import type {
  ActionBody,
  LocalFactory,
  LocalItem,
  Receipt,
} from "../api/types.ts";
import { clock, hostName, kindLook, openThreads } from "../format.ts";
import {
  settled,
  useBlocked,
  useDraft,
  useNode,
  useNow,
  useSend,
} from "../hooks.ts";
import { age, findItem } from "../order.ts";
import { paths } from "../route.ts";
import { useApp } from "../state.tsx";
import { AppShell } from "./AppShell.tsx";
import { Title } from "./FactoryDetail.tsx";
import { Link } from "./Link.tsx";
import { PageList } from "./Pages.tsx";
import { NextLink, Preview, ReceiptLine, SendLine } from "./Receipt.tsx";

export function AttentionItem({
  route,
  itemKey,
}: {
  route: string;
  itemKey: string;
}) {
  const { snapshot, reach, live } = useApp();
  const { factory, item } = findItem(snapshot, route, itemKey);
  if (!factory)
    return (
      <AppShell back={paths.list()}>
        <p className="empty hint">
          {!snapshot && reach === "loading"
            ? "Opening…"
            : live
              ? `This factory is not on ${hostName()}.`
              : `The details of this notification need ${hostName()}.`}
        </p>
      </AppShell>
    );
  return (
    <AppShell back={paths.factory(route)} title={<Title factory={factory} />}>
      <ItemScreen
        key={`${factory.incarnation}:${itemKey}`}
        factory={factory}
        item={item}
        itemKey={itemKey}
      />
    </AppShell>
  );
}

type Sender = ReturnType<typeof useSend>;

function ItemScreen(props: {
  factory: LocalFactory;
  item?: LocalItem;
  itemKey: string;
}) {
  const sender = useSend(props.factory);
  const [seen, setSeen] = useState<string>();
  const id = props.item?.id;
  useEffect(() => {
    if (id !== undefined) setSeen(id);
  }, [id]);
  if (!props.item)
    return (
      <Closed
        factory={props.factory}
        itemKey={props.itemKey}
        seen={seen}
        receipt={sender.receipt}
      />
    );
  return <OpenItem factory={props.factory} item={props.item} sender={sender} />;
}

function Closed(props: {
  factory: LocalFactory;
  itemKey: string;
  seen?: string;
  receipt?: Receipt;
}) {
  const { live, origin, short } = useNode(props.factory);
  const [linked, setLinked] = useState<string>();
  useEffect(() => {
    if (props.seen !== undefined || !live) return;
    let current = true;
    nodeApi(origin)
      .link(props.itemKey)
      .then(
        (link) => current && setLinked(link.item),
        () => undefined,
      );
    return () => {
      current = false;
    };
  }, [props.seen, props.itemKey, live, origin]);
  const id = props.seen ?? linked;
  const resolution =
    id === undefined
      ? undefined
      : props.factory.resolved?.find((r) => r.id === id);
  return (
    <div className="pad stack">
      {props.receipt ? (
        <ReceiptLine receipt={props.receipt} />
      ) : resolution ? (
        <p className="stat ok">
          <span className="gl" aria-hidden="true">
            ✓
          </span>
          <span>
            {resolution.source
              ? `Answered from the ${resolution.source} at ${clock(resolution.at ?? resolution.resolved_at)}`
              : `Resolved elsewhere at ${clock(resolution.resolved_at)}`}
          </span>
        </p>
      ) : (
        <p className="ink">
          {live
            ? "This item is no longer open."
            : `The details of this notification need ${short}.`}
        </p>
      )}
      <p className="sm mute">
        The supervisor closes each item once it has acted on it.
      </p>
      <NextLink factory={props.factory} />
    </div>
  );
}

type Submit = (body: Omit<ActionBody, "item" | "since">) => Promise<Receipt>;

type FormProps = {
  factory: LocalFactory;
  item: LocalItem;
  draft: Draft;
  setDraft: (draft: Draft) => void;
  submit: Submit;
  receipt?: Receipt;
  locked: boolean;
  disabled: boolean;
  why?: string;
  outcome: ReactNode;
};

const forms: Record<string, (props: FormProps) => ReactNode> = {
  question: QuestionForm,
  gate: GateForm,
  comment: CommentForm,
  cap: CapForm,
  failure: CapForm,
  permission: PermissionForm,
  handoff: HandoffForm,
};

function OpenItem(props: {
  factory: LocalFactory;
  item: LocalItem;
  sender: Sender;
}) {
  const { factory, item, sender } = props;
  const { pending } = useApp();
  const blocked = useBlocked(factory);
  const { draft, setDraft, dropDraft } = useDraft(factory, item.key);
  const waiting = pending.some(
    (p) =>
      p.factory === factory.key &&
      p.node === factory.node &&
      p.item === item.id,
  );
  const locked = !!item.answer || settled(sender.receipt);
  const why =
    blocked ??
    (waiting
      ? "Waiting for the receipt of the last send. Nothing is sent twice."
      : undefined);
  const submit: Submit = async (body) => {
    const receipt = await sender.send({
      ...body,
      item: item.id,
      since: item.since,
    });
    if (settled(receipt)) dropDraft();
    return receipt;
  };
  const outcome = sender.receipt ? (
    <>
      <ReceiptLine receipt={sender.receipt} />
      {sender.receipt.status === "sending" ? null : (
        <NextLink factory={factory} />
      )}
    </>
  ) : null;
  const Form = forms[item.kind] ?? Unanswerable;
  return (
    <div className="pad stack">
      <ItemHead item={item} />
      {item.answer ? <Answered answer={item.answer} /> : null}
      <Form
        factory={factory}
        item={item}
        draft={draft}
        setDraft={setDraft}
        submit={submit}
        receipt={sender.receipt}
        locked={locked}
        disabled={!!why || locked || sender.sending || !item.send.ready}
        why={why}
        outcome={outcome}
      />
    </div>
  );
}

function ItemHead({ item }: { item: LocalItem }) {
  const now = useNow();
  const look = kindLook(item.kind);
  return (
    <div className="ihead">
      <p className="k">
        <span className={`${look.tone} b`}>
          <span aria-hidden="true">{look.glyph} </span>
          {item.kind}
        </span>
        {item.id && item.kind !== "permission" ? (
          <span className="id">{item.id}</span>
        ) : null}
        <span className="mute sm">
          {clock(item.since)} · {age(item.since, now)}
        </span>
      </p>
      <h2>{item.title}</h2>
      {item.body ? <p className="sm">{item.body}</p> : null}
      {item.role ? <p className="sm mute">from {item.role}</p> : null}
      {item.revising ? (
        <p className="sm mute">
          The {item.revising} is working on your comments. You are told when
          the page is ready to review again.
        </p>
      ) : null}
      {item.detail?.length ? (
        <ul className="detail">
          {item.detail.map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function Answered({ answer }: { answer: Receipt }) {
  const where = answer.source ? `from the ${answer.source}` : "elsewhere";
  const when = answer.at ? ` at ${clock(answer.at)}` : "";
  return (
    <p className="stat ok" role="status">
      <span className="gl" aria-hidden="true">
        ✓
      </span>
      <span>
        Answered {where}
        {when}
      </span>
      <span className="x">Nothing more can be sent for this item.</span>
    </p>
  );
}

function Actions(props: { why?: string; children: ReactNode }) {
  return (
    <footer className="ab col">
      {props.why ? <p className="note yel">{props.why}</p> : null}
      {props.children}
    </footer>
  );
}

const bodyFor = (item: LocalItem, body: Omit<ActionBody, "item" | "since">) =>
  ({ ...body, item: item.id, since: item.since }) satisfies ActionBody;

function Field(props: {
  legend: string;
  label: string;
  value: string;
  rows?: number;
  readOnly?: boolean;
  onChange: (value: string) => void;
}) {
  return (
    <fieldset className="field">
      <legend>{props.legend}</legend>
      <textarea
        rows={props.rows ?? 3}
        aria-label={props.label}
        value={props.value}
        readOnly={props.readOnly}
        onChange={(event) => props.onChange(event.target.value)}
      />
    </fieldset>
  );
}

function QuestionForm(props: FormProps) {
  const questions = props.item.questions ?? [];
  const answers = questions.map((_, i) => props.draft.answers?.[i] ?? "");
  const missing = answers.findIndex((answer) => answer.trim() === "");
  const [open, setOpen] = useState(Math.max(missing, 0));
  const [typing, setTyping] = useState<number[]>([]);
  if (questions.length === 0) return <TextAnswer {...props} />;

  const custom = (i: number) =>
    typing.includes(i) ||
    (answers[i] !== "" && !questions[i].options.includes(answers[i]));
  const answer = (i: number, value: string) =>
    props.setDraft({
      ...props.draft,
      answers: answers.map((a, j) => (j === i ? value : a)),
    });
  const pick = (i: number, option: string) => {
    answer(i, option);
    setTyping(typing.filter((j) => j !== i));
    const after = answers.findIndex((a, j) => j > i && a.trim() === "");
    if (after !== -1) setOpen(after);
  };
  const startTyping = (i: number) => {
    if (questions[i].options.includes(answers[i])) answer(i, "");
    setTyping([...typing, i]);
  };
  const choose = (i: number) => {
    answer(i, "");
    setTyping(typing.filter((j) => j !== i));
  };
  const body =
    missing === -1
      ? bodyFor(props.item, { action: "answer", answers })
      : undefined;
  const send = questions.length > 1 ? "Send answers" : "Send answer";

  return (
    <>
      <ol className="qs">
        {questions.map((question, i) =>
          i === open ? (
            <li key={i} className="q">
              <p className="qt">
                <span className="qn">{i + 1}</span>
                <span>{question.text}</span>
              </p>
              {custom(i) ? (
                <div className="opts">
                  <Field
                    legend="your answer"
                    label={`Your answer to question ${i + 1}`}
                    value={answers[i]}
                    readOnly={props.locked}
                    onChange={(value) => answer(i, value)}
                  />
                  <button
                    type="button"
                    className="btn quiet"
                    disabled={props.locked}
                    onClick={() => choose(i)}
                  >
                    Choose an option instead
                  </button>
                </div>
              ) : (
                <div
                  className="opts"
                  role="group"
                  aria-label={`Answer to question ${i + 1}`}
                >
                  {question.options.map((option) => (
                    <button
                      key={option}
                      type="button"
                      className="opt"
                      aria-pressed={answers[i] === option}
                      disabled={props.locked}
                      onClick={() => pick(i, option)}
                    >
                      <span className="mk" aria-hidden="true" />
                      <span>{option}</span>
                      {option === question.recommended ? (
                        <span className="rec">recommended</span>
                      ) : null}
                    </button>
                  ))}
                  <button
                    type="button"
                    className="opt"
                    aria-pressed={false}
                    disabled={props.locked}
                    onClick={() => startTyping(i)}
                  >
                    <span className="mk" aria-hidden="true" />
                    <span className="cyn">
                      <span aria-hidden="true">✎ </span>Type an answer
                    </span>
                  </button>
                </div>
              )}
            </li>
          ) : (
            <li key={i}>
              <button
                type="button"
                className="folded"
                aria-expanded={false}
                onClick={() => setOpen(i)}
              >
                <span className="dk">{i + 1}</span>
                <span className={answers[i] ? "grn" : "org"} aria-hidden="true">
                  {answers[i] ? "✓" : "○"}
                </span>
                <span className="ink">{question.text}</span>
                <span className="a">
                  {answers[i] ? `→ ${answers[i]}` : "not answered yet"}
                </span>
              </button>
            </li>
          ),
        )}
      </ol>
      {missing === -1 ? (
        <SendLine send={props.item.send} />
      ) : (
        <p className="stat warn">
          <span className="gl" aria-hidden="true">
            !
          </span>
          <span>Question {missing + 1} has no answer yet.</span>
        </p>
      )}
      <Preview factory={props.factory} body={body} />
      {props.outcome}
      <Actions why={props.why}>
        <button
          type="button"
          className="btn pri"
          disabled={props.disabled || missing !== -1}
          onClick={() => props.submit({ action: "answer", answers })}
        >
          {send}
        </button>
      </Actions>
    </>
  );
}

function TextAnswer(props: FormProps) {
  const text = props.draft.text ?? "";
  return (
    <>
      <Field
        legend="answer"
        label="Your answer"
        value={text}
        readOnly={props.locked}
        onChange={(value) => props.setDraft({ ...props.draft, text: value })}
      />
      <SendLine send={props.item.send} />
      <Preview
        factory={props.factory}
        body={
          text.trim()
            ? bodyFor(props.item, { action: "answer", text })
            : undefined
        }
      />
      {props.outcome}
      <Actions why={props.why}>
        <button
          type="button"
          className="btn pri"
          disabled={props.disabled || !text.trim()}
          onClick={() => props.submit({ action: "answer", text })}
        >
          Send answer
        </button>
      </Actions>
    </>
  );
}

const ARMED = 4_000;

function GateForm(props: FormProps) {
  const notes = props.draft.notes ?? "";
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const timer = setTimeout(() => setArmed(false), ARMED);
    return () => clearTimeout(timer);
  }, [armed]);
  const threads = openThreads(props.factory);
  const busy = props.factory.pages.filter((page) => page.open_threads);
  const approve = () => {
    if (!armed) return setArmed(true);
    setArmed(false);
    props.submit({ action: "approve", text: notes });
  };
  return (
    <>
      {props.factory.pages.length > 0 ? (
        <PageList pages={props.factory.pages} />
      ) : null}
      <Field
        legend="notes"
        label="Notes sent with the approval"
        value={notes}
        readOnly={props.locked}
        onChange={(value) => props.setDraft({ ...props.draft, notes: value })}
      />
      <SendLine send={props.item.send} />
      <Preview
        factory={props.factory}
        body={bodyFor(props.item, { action: "approve", text: notes })}
      />
      {props.outcome}
      <Actions why={props.why}>
        {armed && threads > 0 ? (
          <p className="note yel">
            ! Open threads:{" "}
            {busy.map((page) => `${page.name} ${page.open_threads}`).join(", ")}
            . Approving leaves them as they are.
          </p>
        ) : null}
        <div className="row2">
          <button
            type="button"
            className="btn"
            disabled={props.disabled || !notes.trim()}
            onClick={() => props.submit({ action: "changes", text: notes })}
          >
            Request changes
          </button>
          <button
            type="button"
            className={armed ? "btn hot" : "btn ok"}
            disabled={props.disabled}
            onClick={approve}
          >
            {armed ? (
              "Tap again to approve"
            ) : (
              <>
                <span aria-hidden="true">✓ </span>
                {props.item.stage ? `Approve ${props.item.stage}` : "Approve"}
              </>
            )}
          </button>
        </div>
      </Actions>
    </>
  );
}

function CommentForm(props: FormProps) {
  const text = props.draft.text ?? "";
  return (
    <>
      {props.item.id ? (
        <p className="sm mute">Your reply goes to thread {props.item.id}.</p>
      ) : null}
      <Field
        legend="reply"
        label="Your reply"
        value={text}
        readOnly={props.locked}
        onChange={(value) => props.setDraft({ ...props.draft, text: value })}
      />
      <SendLine send={props.item.send} />
      <Preview
        factory={props.factory}
        body={
          text.trim()
            ? bodyFor(props.item, { action: "reply", text })
            : undefined
        }
      />
      {props.outcome}
      <Actions why={props.why}>
        <div className="row2">
          <button
            type="button"
            className="btn"
            disabled={props.disabled}
            onClick={() => props.submit({ action: "handled" })}
          >
            Handled
          </button>
          <button
            type="button"
            className="btn pri"
            disabled={props.disabled || !text.trim()}
            onClick={() => props.submit({ action: "reply", text })}
          >
            Send reply
          </button>
        </div>
      </Actions>
    </>
  );
}

function HandoffForm(props: FormProps) {
  const text = props.draft.text ?? "";
  return (
    <>
      {props.factory.pages.length > 0 ? (
        <PageList pages={props.factory.pages} />
      ) : null}
      <Field
        legend="or tell it what to do next"
        label="What to do next"
        value={text}
        readOnly={props.locked}
        onChange={(value) => props.setDraft({ ...props.draft, text: value })}
      />
      <SendLine send={props.item.send} />
      <Preview
        factory={props.factory}
        body={
          text.trim()
            ? bodyFor(props.item, { action: "continue", text })
            : undefined
        }
      />
      {props.outcome}
      <Actions why={props.why}>
        <div className="row2">
          <button
            type="button"
            className="btn"
            disabled={props.disabled || !text.trim()}
            onClick={() => props.submit({ action: "continue", text })}
          >
            Continue working
          </button>
          <button
            type="button"
            className="btn ok"
            disabled={props.disabled}
            onClick={() => props.submit({ action: "handled" })}
          >
            <span aria-hidden="true">✓ </span>Mark done
          </button>
        </div>
      </Actions>
    </>
  );
}

function CapForm(props: FormProps) {
  const text = props.draft.text ?? "";
  const cap = props.item.kind === "cap";
  const skip = props.item.stage ? `Skip ${props.item.stage}` : "Skip";
  return (
    <>
      <Field
        legend="or answer in your own words"
        label="Your answer"
        value={text}
        readOnly={props.locked}
        onChange={(value) => props.setDraft({ ...props.draft, text: value })}
      />
      <SendLine send={props.item.send} />
      {props.outcome}
      <Actions why={props.why}>
        <div className="row2">
          <button
            type="button"
            className="btn"
            disabled={props.disabled}
            onClick={() => props.submit({ action: cap ? "continue" : "retry" })}
          >
            {cap ? "Continue one more round" : "Retry"}
          </button>
          <button
            type="button"
            className="btn"
            disabled={props.disabled}
            onClick={() => props.submit({ action: "skip" })}
          >
            {skip}
          </button>
        </div>
        <button
          type="button"
          className="btn pri"
          disabled={props.disabled || !text.trim()}
          onClick={() => props.submit({ action: "answer", text })}
        >
          Send answer
        </button>
      </Actions>
    </>
  );
}

function PermissionForm(props: FormProps) {
  const menu = props.item.permission;
  const [pick, setPick] = useState<string>();
  const text = props.draft.text ?? "";
  if (!menu)
    return (
      <>
        <p className="stat idle">
          <span className="gl" aria-hidden="true">
            ◈
          </span>
          <span>Answer it in the session.</span>
        </p>
        {props.outcome}
      </>
    );
  const chosen = menu.options.find((option) => option.id === pick);
  const needsText = !!chosen?.tells && !text.trim();
  const answer = async () => {
    if (!chosen) return;
    const receipt = await props.submit({
      action: "permission",
      option: chosen.id,
      fingerprint: menu.fingerprint,
      text: chosen.tells ? text : undefined,
    });
    if (receipt.status === "stale") setPick(undefined);
  };
  return (
    <>
      {props.receipt?.status === "stale" ? (
        <p className="stat warn">
          <span className="gl" aria-hidden="true">
            !
          </span>
          <span>
            The prompt changed before your answer reached it. Nothing was typed.
            This is the new prompt.
          </span>
        </p>
      ) : null}
      <section className="box">
        <h3 className="bt mute">{menu.tool}</h3>
        {menu.title ? <p className="sm">{menu.title}</p> : null}
        {menu.command.map((line, i) => (
          <p key={i} className="cmd">
            {line}
          </p>
        ))}
      </section>
      <p className="ink">{menu.question}</p>
      <div
        className="opts flush"
        role="group"
        aria-label="Answer to the permission prompt"
      >
        {menu.options.map((option) => (
          <button
            key={option.id}
            type="button"
            className="opt"
            aria-pressed={pick === option.id}
            disabled={props.locked}
            onClick={() => setPick(option.id)}
          >
            <span className="mk" aria-hidden="true" />
            <span>
              {option.id}. {option.label}
            </span>
          </button>
        ))}
      </div>
      {chosen?.tells ? (
        <Field
          legend="what Claude should do differently"
          label="What Claude should do differently"
          value={text}
          readOnly={props.locked}
          onChange={(value) => props.setDraft({ ...props.draft, text: value })}
        />
      ) : null}
      <SendLine send={props.item.send} />
      {props.receipt?.status === "stale" ? null : props.outcome}
      <Actions why={props.why}>
        <button
          type="button"
          className="btn pri"
          disabled={props.disabled || !chosen || needsText}
          onClick={answer}
        >
          {chosen ? `Answer: ${chosen.id}. ${chosen.label}` : "Pick an answer"}
        </button>
      </Actions>
    </>
  );
}

function Unanswerable(props: FormProps) {
  return (
    <>
      {props.item.kind === "asleep" ? (
        <p className="sm">
          Wake it from{" "}
          <Link href={paths.factory(props.factory.route)}>
            {props.factory.id}
          </Link>
          's screen.
        </p>
      ) : null}
      {props.outcome}
    </>
  );
}
