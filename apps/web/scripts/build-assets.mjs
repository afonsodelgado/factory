import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium } from "@playwright/test";
import { heroStateAt } from "../src/lib/demo.ts";
import { IC, renderHero } from "../src/lib/frames.ts";

const app = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const pub = path.join(app, "public");
const css = (name) =>
  fs.readFileSync(path.join(app, "src/styles", name), "utf8");

const OG_WIDTH = 1200;
const OG_HEIGHT = 630;

async function buildOgImage() {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "factory-og-"));
  const fonts = css("fonts.css").replaceAll(
    'url("/fonts/',
    `url("${pathToFileURL(path.join(pub, "fonts")).href}/`,
  );
  const page = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<style>${fonts}${css("site.css")}${css("terminal.css")}
body { width: ${OG_WIDTH}px; height: ${OG_HEIGHT}px; display: grid; place-items: center; overflow: hidden; }
.term-hero { --fs-min: 20px; --fs-max: 20px; }
</style>
<div class="frame hero-pane" style="width: 560px">
  <div class="win">
    <div class="bar"><span class="lights"><i></i><i></i><i></i></span><span class="wt">devbox:hub</span><span class="dim">38×22</span></div>
    <div class="scroll"><pre class="term term-hero">${renderHero(heroStateAt(0))}</pre></div>
  </div>
</div>
</html>`;
  const file = path.join(work, "og.html");
  fs.writeFileSync(file, page);
  const browser = await chromium.launch();
  try {
    const tab = await browser.newPage({
      viewport: { width: OG_WIDTH, height: OG_HEIGHT },
    });
    await tab.goto(pathToFileURL(file).href);
    const loaded = await tab.evaluate(async () => {
      await document.fonts.ready;
      return [...document.fonts].map((f) => `${f.weight} ${f.status}`);
    });
    if (loaded.join() !== "400 loaded,700 loaded") {
      throw new Error(`fonts did not load: ${loaded.join(", ")}`);
    }
    await tab.screenshot({ path: path.join(pub, "og.png") });
  } finally {
    await browser.close();
    fs.rmSync(work, { recursive: true, force: true });
  }
}

const faviconScript = `
import sys
from fontTools.ttLib import TTFont
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen

font = TTFont(sys.argv[1])
glyphs = font.getGlyphSet()
glyph = glyphs[font.getBestCmap()[int(sys.argv[2])]]
bounds = BoundsPen(glyphs)
glyph.draw(bounds)
x0, y0, x1, y1 = bounds.bounds
size, inner = 64, 44
scale = inner / max(x1 - x0, y1 - y0)
dx = (size - (x1 - x0) * scale) / 2 - x0 * scale
dy = (size + (y1 - y0) * scale) / 2 + y0 * scale
pen = SVGPathPen(glyphs, ntos=lambda v: f"{v:.2f}".rstrip("0").rstrip("."))
glyph.draw(TransformPen(pen, (scale, 0, 0, -scale, dx, dy)))
print(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {size} {size}"><rect width="{size}" height="{size}" fill="#7aa2f7"/><path fill="#15161e" d="{pen.getCommands()}"/></svg>')
`;

function buildFavicon() {
  const font = path.join(pub, "fonts/jetbrains-mono-bold.woff2");
  const svg = execFileSync(
    "python3",
    ["-c", faviconScript, font, String(IC.app)],
    { encoding: "utf8" },
  );
  fs.writeFileSync(path.join(pub, "favicon.svg"), svg);
}

await buildOgImage();
buildFavicon();
