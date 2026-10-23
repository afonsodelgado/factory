export const PLATFORMS = [
  { id: "darwin_arm64", label: "macOS, Apple silicon" },
  { id: "darwin_amd64", label: "macOS, Intel" },
  { id: "linux_amd64", label: "Linux, x86-64" },
  { id: "linux_arm64", label: "Linux, ARM64" },
] as const;

export type Platform = (typeof PLATFORMS)[number]["id"];

export const DEFAULT_PLATFORM: Platform = "darwin_arm64";

export function isPlatform(value: string | undefined): value is Platform {
  return PLATFORMS.some((p) => p.id === value);
}

export type Token = string | { cls: string; parts: Token[] };

export interface Line {
  prompt: boolean;
  tokens: Token[];
}

const flag = (text: string): Token => ({ cls: "flag", parts: [text] });
const str = (...parts: Token[]): Token => ({ cls: "str", parts });
const cmt = (text: string): Token => ({ cls: "cmt", parts: [text] });
const plat = (platform: Platform): Token => ({
  cls: "plat-name",
  parts: [platform],
});
const run = (...tokens: Token[]): Line => ({ prompt: true, tokens });
const more = (...tokens: Token[]): Line => ({ prompt: false, tokens });

const chmod = run("chmod +x ~/.local/bin/factory");

export function heroDownload(): Line[] {
  return [
    run("gh release download \\"),
    more("    ", flag("--repo"), " afonsodelgado/factory \\"),
    more("    ", flag("--pattern"), " ", str("'factory_darwin_arm64'"), " \\"),
    more("    ", flag("--output"), " ~/.local/bin/factory"),
    chmod,
  ];
}

export function download(platform: Platform): Line[] {
  return [
    run("gh release download ", flag("--repo"), " afonsodelgado/factory \\"),
    more(
      "    ",
      flag("--pattern"),
      " ",
      str("'factory_", plat(platform), "'"),
      " \\",
    ),
    more("    ", flag("--output"), " ~/.local/bin/factory"),
    chmod,
  ];
}

export const COMMANDS = {
  go: [run("go install github.com/afonsodelgado/factory/cmd/factory@latest")],
  doctor: [run("factory doctor")],
  bootstrap: [run("factory bootstrap")],
  open: [run("factory")],
  update: [
    run(
      "factory version    ",
      cmt("# what is running, and the latest release"),
    ),
    run("factory --version  ", cmt("# only what is running, no network call")),
    run(
      "factory update     ",
      cmt("# install the latest release, then run its bootstrap"),
    ),
  ],
} satisfies Record<string, Line[]>;

export type CommandId = "hero" | "download" | keyof typeof COMMANDS;

export function commandLines(id: CommandId, platform: Platform): Line[] {
  if (id === "hero") return heroDownload();
  if (id === "download") return download(platform);
  return COMMANDS[id];
}

function tokenText(token: Token): string {
  return typeof token === "string"
    ? token
    : token.parts.map(tokenText).join("");
}

export function commandText(lines: Line[]): string {
  return lines.map((line) => line.tokens.map(tokenText).join("")).join("\n");
}

const esc = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

function tokenHtml(token: Token): string {
  if (typeof token === "string") return esc(token);
  return `<span class="${token.cls}">${token.parts.map(tokenHtml).join("")}</span>`;
}

export const PROMPT_HTML = '<span class="pr">❯ </span>';

export function commandHtml(lines: Line[]): string {
  return lines
    .map(
      (line) =>
        (line.prompt ? PROMPT_HTML : "") + line.tokens.map(tokenHtml).join(""),
    )
    .join("\n");
}
