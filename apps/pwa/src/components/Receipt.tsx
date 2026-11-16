import type {
  ActionBody,
  Eligibility,
  LocalFactory,
  Receipt,
} from "../api/types.ts";
import { usePreview } from "../hooks.ts";
import { next } from "../order.ts";
import { paths } from "../route.ts";
import { useApp } from "../state.tsx";
import { Link } from "./Link.tsx";

const receiptLooks: Record<
  Receipt["status"],
  { glyph: string; tone: string; label: string; fallback: string }
> = {
  delivered: {
    glyph: "✓",
    tone: "ok",
    label: "Delivered.",
    fallback: "The supervisor is on it.",
  },
  queued: {
    glyph: "◷",
    tone: "q",
    label: "Queued.",
    fallback: "It is sent once the supervisor is idle at its prompt.",
  },
  sending: {
    glyph: "◌",
    tone: "q",
    label: "Sending…",
    fallback: "",
  },
  refused: {
    glyph: "!",
    tone: "warn",
    label: "Not sent.",
    fallback: "The draft is kept.",
  },
  stale: {
    glyph: "!",
    tone: "warn",
    label: "Not sent.",
    fallback: "The item changed before the answer reached it.",
  },
  unknown: {
    glyph: "?",
    tone: "warn",
    label: "No answer yet.",
    fallback: "This may or may not have arrived. Nothing is sent again.",
  },
};

export function ReceiptLine({ receipt }: { receipt: Receipt }) {
  const look = receiptLooks[receipt.status];
  return (
    <p className={`stat ${look.tone}`} role="status">
      <span className="gl" aria-hidden="true">
        {look.glyph}
      </span>
      <span>{look.label}</span>
      <span className="x">{receipt.reason || look.fallback}</span>
    </p>
  );
}

export function NextLink({ factory }: { factory: LocalFactory }) {
  const { snapshot } = useApp();
  const waiting = snapshot ? next(snapshot, factory.route) : undefined;
  if (!waiting) return null;
  const first = waiting.items[0];
  return (
    <Link
      className="btn wide"
      href={
        first
          ? paths.item(waiting.route, first.key)
          : paths.factory(waiting.route)
      }
    >
      Next: <b className="ink">{waiting.id}</b>
      <span className="org">needs you</span>
      <span aria-hidden="true">›</span>
    </Link>
  );
}

export function SendLine({ send }: { send: Eligibility }) {
  const [tone, glyph] = !send.ready
    ? ["warn", "!"]
    : send.queues
      ? ["q", "◷"]
      : ["ok", "✓"];
  return (
    <p className={`stat ${tone}`}>
      <span className="gl" aria-hidden="true">
        {glyph}
      </span>
      <span>{send.reason}</span>
    </p>
  );
}

export function Preview(props: {
  factory: LocalFactory;
  body: ActionBody | undefined;
}) {
  const text = usePreview(props.factory, props.body);
  if (!text) return null;
  return (
    <p className="preview">
      <b>preview</b>
      {text}
    </p>
  );
}

export function Blocked({ reason }: { reason: string | undefined }) {
  if (!reason) return null;
  return <p className="note yel">{reason}</p>;
}
