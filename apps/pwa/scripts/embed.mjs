import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const app = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const built = path.join(app, "dist");
const embedded = path.join(app, "../../bundle/pwa/dist");
const required = ["index.html", "sw.js", "manifest.webmanifest", "build.json"];

function stamp(dir) {
  return JSON.parse(fs.readFileSync(path.join(dir, "build.json"), "utf8"))
    .version;
}

function verify() {
  const missing = required.filter(
    (name) => !fs.existsSync(path.join(embedded, name)),
  );
  if (missing.length > 0) {
    throw new Error(
      `bundle/pwa/dist lacks ${missing.join(", ")}; run make build`,
    );
  }
  if (stamp(embedded) !== stamp(built)) {
    throw new Error(
      "bundle/pwa/dist is older than apps/pwa/dist; run make build",
    );
  }
}

if (process.argv.includes("--verify")) {
  verify();
} else {
  fs.rmSync(embedded, { recursive: true, force: true });
  fs.cpSync(built, embedded, { recursive: true });
  verify();
}
