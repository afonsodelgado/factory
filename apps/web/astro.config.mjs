import { defineConfig } from "astro/config";
import react from "@astrojs/react";
import sitemap from "@astrojs/sitemap";

function siteOrigin(value = "http://localhost:4321") {
  const url = URL.parse(value);
  const isOrigin =
    url !== null &&
    ["http:", "https:"].includes(url.protocol) &&
    url.pathname === "/" &&
    !url.search &&
    !url.hash;
  if (!isOrigin) {
    throw new Error(`SITE_URL must be an http(s) origin, got ${value}`);
  }
  return url.origin;
}

const site = siteOrigin(process.env.SITE_URL || undefined);

export default defineConfig({
  site,
  output: "static",
  integrations: [
    react(),
    sitemap({ filter: (page) => new URL(page).pathname === "/" }),
  ],
  server: { host: "127.0.0.1", port: 4321 },
  vite: {
    server: { strictPort: true },
    preview: { strictPort: true },
  },
});
