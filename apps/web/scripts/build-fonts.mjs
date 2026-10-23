import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  ANSWER,
  HERO_STATES,
  HUB_PHASES,
  heroStateAt,
  hubStateAt,
} from "../src/lib/demo.ts";
import {
  COLS,
  IC,
  renderAgents,
  renderHero,
  renderHub,
} from "../src/lib/frames.ts";

const app = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const fontDir =
  process.env.NERD_FONT_DIR || path.join(os.homedir(), "Library/Fonts");

const inputs = [
  {
    weight: "regular",
    file: "JetBrainsMonoNerdFont-Regular.ttf",
    sha256: "1c680e8cde9fcf8b88a5605ce8d1fb94dd3fb15841f7ca7bf4c55664855e5611",
  },
  {
    weight: "bold",
    file: "JetBrainsMonoNerdFont-Bold.ttf",
    sha256: "e490660ad75e0b152c93b1604c2ea1a4ea675f1b8a3fe0d005b1998568f99f1f",
  },
];

const ranges = [
  "20-7E",
  "A0-FF",
  "2010-2027",
  "2190-21FF",
  "2200-22FF",
  "2300-23FF",
  "2500-25FF",
  "2600-26FF",
  "2700-27BF",
  "27F0-27FF",
];

function renderedFrames() {
  const out = [renderAgents()];
  for (let i = 0; i < HERO_STATES; i++) out.push(renderHero(heroStateAt(i)));
  for (let i = 0; i < HUB_PHASES.length; i++) {
    for (let spin = 0; spin < 10; spin++) {
      const st = hubStateAt(i);
      st.spin = spin;
      st.answer = ANSWER.slice(0, spin);
      out.push(renderHub(st, COLS.hubWide), renderHub(st, COLS.hubNarrow));
    }
  }
  return out;
}

function sourceTexts() {
  const files = fs.readdirSync(path.join(app, "src"), {
    recursive: true,
    withFileTypes: true,
  });
  return files
    .filter((f) => f.isFile() && /\.(astro|ts|tsx|css)$/.test(f.name))
    .map((f) => fs.readFileSync(path.join(f.parentPath, f.name), "utf8"));
}

function usedCodepoints() {
  const used = new Set(Object.values(IC));
  for (const text of [...renderedFrames(), ...sourceTexts()]) {
    for (const ch of text) used.add(ch.codePointAt(0));
    for (const m of text.matchAll(/&#x([0-9a-f]+);/gi))
      used.add(parseInt(m[1], 16));
  }
  return [...used].filter((cp) => cp >= 0x20).sort((a, b) => a - b);
}

function sha256(file) {
  return createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function cmap(file) {
  const script =
    "import sys\nfrom fontTools.ttLib import TTFont\nprint(' '.join(str(c) for c in TTFont(sys.argv[1]).getBestCmap()))";
  return new Set(
    execFileSync("python3", ["-c", script, file], { encoding: "utf8" })
      .trim()
      .split(" ")
      .map(Number),
  );
}

const used = usedCodepoints();
const unicodes = ranges
  .concat(used.map((cp) => cp.toString(16).toUpperCase()))
  .join(",");
let failed = false;

for (const { weight, file, sha256: expected } of inputs) {
  const source = path.join(fontDir, file);
  const actual = sha256(source);
  if (actual !== expected) {
    console.error(`${file}: sha256 ${actual}, expected ${expected}`);
    process.exit(1);
  }
  const out = path.join(app, "public/fonts", `jetbrains-mono-${weight}.woff2`);
  execFileSync("pyftsubset", [
    source,
    `--unicodes=${unicodes}`,
    "--flavor=woff2",
    "--no-hinting",
    "--name-IDs=*",
    `--output-file=${out}`,
  ]);
  const inSource = cmap(source);
  const inSubset = cmap(out);
  const dropped = used.filter((cp) => inSource.has(cp) && !inSubset.has(cp));
  const absent = used.filter((cp) => !inSource.has(cp));
  console.log(
    `${path.basename(out)}: ${(fs.statSync(out).size / 1024).toFixed(1)} KB, ${inSubset.size} glyphs`,
  );
  if (absent.length) {
    const list = absent
      .map(
        (cp) =>
          `U+${cp.toString(16).toUpperCase()} ${String.fromCodePoint(cp)}`,
      )
      .join(", ");
    console.warn(`  not in ${file}, drawn by the fallback font: ${list}`);
  }
  if (dropped.length) {
    console.error(
      `  dropped by the subset: ${dropped.map((cp) => `U+${cp.toString(16).toUpperCase()}`).join(", ")}`,
    );
    failed = true;
  }
}

if (failed) process.exit(1);
