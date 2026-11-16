import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const app = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const favicon = fs.readFileSync(
  path.join(app, "../web/public/favicon.svg"),
  "utf8",
);
const out = path.join(app, "public/icons");

const icons = [
  { name: "icon-192.png", size: 192, inset: 0 },
  { name: "icon-512.png", size: 512, inset: 0 },
  { name: "maskable-512.png", size: 512, inset: 0.2 },
  { name: "apple-touch-icon.png", size: 180, inset: 0 },
];

const browser = await chromium.launch();
const page = await browser.newPage();
for (const { name, size, inset } of icons) {
  const glyph = size * (1 - inset);
  await page.setViewportSize({ width: size, height: size });
  await page.setContent(
    `<body style="margin:0;width:${size}px;height:${size}px;display:grid;place-items:center;background:#7aa2f7">
      <div style="width:${glyph}px;height:${glyph}px">${favicon.replace("<svg ", '<svg width="100%" height="100%" ')}</div>
    </body>`,
  );
  await page.screenshot({ path: path.join(out, name), omitBackground: false });
}
await browser.close();
