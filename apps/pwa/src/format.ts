import type { LocalFactory, LocalItem } from "./api/types.ts";

export type Tone =
  "grn" | "org" | "yel" | "red" | "mag" | "cyn" | "dk" | "mute";
export type Look = { glyph: string; tone: Tone };

export const hostName = () => location.hostname.split(".")[0] || "the computer";

export const shortName = (name: string) => name.split(".")[0];

export const originOf = (name: string) => `${location.protocol}//${name}`;

export function clock(iso: string | undefined): string {
  if (!iso) return "";
  return new Date(iso).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

export function day(iso: string | undefined): string {
  if (!iso) return "";
  return new Date(iso).toLocaleDateString([], {
    day: "numeric",
    month: "short",
  });
}

const statuses: Record<string, Look> = {
  running: { glyph: "●", tone: "grn" },
  starting: { glyph: "◌", tone: "cyn" },
  "needs you": { glyph: "◆", tone: "org" },
  "on CI": { glyph: "⟳", tone: "mag" },
  "on agent": { glyph: "⋯", tone: "mag" },
  stalled: { glyph: "☾", tone: "yel" },
  exited: { glyph: "○", tone: "yel" },
  gone: { glyph: "⊘", tone: "red" },
  done: { glyph: "✓", tone: "dk" },
  unreachable: { glyph: "↮", tone: "red" },
};

export const statusLook = (status: string): Look =>
  statuses[status] ?? { glyph: "●", tone: "mute" };

const kinds: Record<string, Look> = {
  question: { glyph: "?", tone: "org" },
  gate: { glyph: "⚑", tone: "org" },
  comment: { glyph: "✎", tone: "cyn" },
  cap: { glyph: "⇥", tone: "yel" },
  failure: { glyph: "✕", tone: "red" },
  permission: { glyph: "◈", tone: "org" },
  prompt: { glyph: "◈", tone: "org" },
  silent: { glyph: "◌", tone: "yel" },
  handoff: { glyph: "✓", tone: "grn" },
  asleep: { glyph: "☾", tone: "yel" },
  unreadable: { glyph: "!", tone: "red" },
};

export const kindLook = (kind: string): Look =>
  kinds[kind] ?? { glyph: "◆", tone: "org" };

const kindWords: Record<string, string> = { silent: "not reporting" };

export const kindWord = (kind: string) => kindWords[kind] ?? kind;

export function itemTag(item: LocalItem): string {
  const questions = item.questions?.length ?? 0;
  const choices = item.menu?.choices.length ?? 0;
  const extra =
    item.kind === "gate"
      ? item.stage
      : item.kind === "permission"
        ? item.permission?.tool
        : choices > 0
          ? `${choices} choices`
          : questions > 1
            ? `${questions} asks`
            : undefined;
  return extra ? `${kindWord(item.kind)} · ${extra}` : kindWord(item.kind);
}

export const openThreads = (factory: LocalFactory) =>
  factory.pages.reduce((sum, page) => sum + (page.open_threads ?? 0), 0);
