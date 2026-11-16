import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

const fonts = fileURLToPath(new URL("../web/public/fonts/", import.meta.url));

function shell(): Plugin {
  return {
    name: "factory-shell",
    apply: "build",
    generateBundle(_, bundle) {
      for (const name of ["OFL.txt", "NOTICE.md"]) {
        this.emitFile({
          type: "asset",
          fileName: `fonts/${name}`,
          source: readFileSync(fonts + name),
        });
      }
      const worker = bundle["sw.js"];
      if (!worker || worker.type !== "chunk") {
        this.error("the service worker chunk is missing");
      }
      const files = Object.keys(bundle)
        .filter((name) => name !== "sw.js")
        .sort();
      const precache = [
        "/",
        "/manifest.webmanifest",
        "/icons/icon-192.png",
        "/icons/icon-512.png",
        "/icons/maskable-512.png",
        "/icons/apple-touch-icon.png",
        ...files
          .filter((name) => /^(assets|fonts)\/.+\.(js|css|woff2)$/.test(name))
          .map((name) => "/" + name),
      ];
      const version = createHash("sha256")
        .update(
          files
            .map((name) => {
              const file = bundle[name];
              return name + (file.type === "chunk" ? file.code : file.source);
            })
            .join("\0"),
        )
        .digest("hex")
        .slice(0, 16);
      worker.code = worker.code
        .replaceAll(/["'`]__PRECACHE__["'`]/g, JSON.stringify(precache))
        .replaceAll("__VERSION__", version);
      if (worker.code.includes("__PRECACHE__")) {
        this.error("the service worker's precache list was not filled in");
      }
      this.emitFile({
        type: "asset",
        fileName: "build.json",
        source: JSON.stringify({ version }) + "\n",
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), shell()],
  server: { fs: { allow: [".", fonts] } },
  build: {
    rollupOptions: {
      input: { index: "index.html", sw: "src/sw.ts" },
      output: {
        entryFileNames: (chunk) =>
          chunk.name === "sw" ? "sw.js" : "assets/[name]-[hash].js",
      },
    },
  },
});
