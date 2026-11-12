---
name: factory-artifact
description: |
  The page contract for a factory's HTML artifacts: design variants, plan review pages and
  schematics that the developer opens on the factory app's review page and comments on.
  Use when the factory designer or architect writes an HTML page, on any agent tool.
---

# Factory artifacts

The factory app serves every HTML file in the factory directory on `127.0.0.1`, adds its comment layer, and reloads the page when you rewrite it. On a server, the developer views it on their laptop through an ssh tunnel, so link to other pages with relative URLs, never with this machine's host or port. The developer clicks an element and comments on it, and the comments come back to you as a batch. Write pages that read well there and that comments can anchor to.

## The page

- One self-contained `.html` file: inline `<style>`, inline `<script>` only when the page needs behaviour.
- External files only from `cdnjs.cloudflare.com`, `cdn.jsdelivr.net/npm/`, `unpkg.com` or Google Fonts, each with an exact version. No images from the web: draw them with CSS or inline SVG.
- `<meta charset="utf-8">`, `<meta name="viewport" content="width=device-width, initial-scale=1">` and a `<title>` of two to four words.
- Colours, radii and spacing as tokens on `:root`. Redefine the colour tokens under `@media (prefers-color-scheme: dark)`, and give `body` an explicit background and text colour.
- It works at phone width: a 16px side gutter, nothing wider than the screen, tables that scroll inside their own box.
- Leave the bottom-right corner free of fixed elements: the comment toolbar sits there.

## Anchors for comments

A comment remembers the element's CSS selector and its text. Help both survive your next edit:

- Give every section, state and decision a stable `id`: `id="q1"`, `id="state-empty"`, `id="variant-b-table"`. Keep an id when you rewrite its content.
- Use real elements (`button`, `table`, `label`, `h2`) rather than nested `div`s, so the selector reads as what the developer clicked.
- Do not rename or reorder sections between rounds unless a comment asks for it. A comment whose element disappears shows as detached.

## Answering comments

A batch starts with `review-<id>-<n> comments: <page>` and lists each thread: its id, what it is (fix, change or question), the element and the developer's words.

- **Fix or change:** edit the page, then `factory review reply --thread <id> --resolve <what you changed, in one sentence>`.
- **Question:** answer without changing the page: `factory review reply --thread <id> <answer>`. Leave it open.
- **Cannot do it, or it needs the developer:** reply with why, and leave it open.

Answer every thread in the batch. The page reloads by itself; you never publish anything.

## Diagrams

For a schematic, draw inline SVG in the page:

- A `viewBox` and `width: 100%`, no fixed pixel width, so it scales to the screen.
- Strokes and fills from the colour tokens (`stroke="var(--line)"`), so it works in both themes.
- Text as real `<text>` in the page font, at least 12px at phone width.
- Show the real mechanism: the components, the data that moves between them and the order it moves in, numbered. One diagram per question it answers, each with a one-line caption.
