import assert from "node:assert/strict";
import { test } from "node:test";
import {
  ANSWER,
  HERO_STATES,
  HUB_PHASES,
  heroStateAt,
  hubStateAt,
} from "../src/lib/demo.ts";
import {
  COLS,
  ROWS,
  renderAgents,
  renderHero,
  renderHub,
} from "../src/lib/frames.ts";

const decode = (s: string) =>
  s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");

function cells(row: string) {
  const text = row
    .replace(/<i class="ic[^"]*" data-n="&#x[0-9a-f]+;"><\/i>/g, "X")
    .replace(/<[^>]+>/g, "");
  return Array.from(decode(text)).length;
}

function assertGrid(name: string, markup: string, cols: number, rows: number) {
  const lines = markup.split("\n");
  assert.equal(lines.length, rows, `${name}: rows`);
  lines.forEach((line, i) =>
    assert.equal(cells(line), cols, `${name} row ${i}`),
  );
}

test("all demo states keep fixed terminal cell dimensions", () => {
  for (let i = 0; i < HERO_STATES; i++) {
    assertGrid(`hero ${i}`, renderHero(heroStateAt(i)), COLS.hero, ROWS.hero);
  }
  for (let i = 0; i < HUB_PHASES.length; i++) {
    const st = hubStateAt(i);
    assertGrid(
      `hub ${i} wide`,
      renderHub(st, COLS.hubWide),
      COLS.hubWide,
      ROWS.hub,
    );
    assertGrid(
      `hub ${i} narrow`,
      renderHub(st, COLS.hubNarrow),
      COLS.hubNarrow,
      ROWS.hub,
    );
  }
  for (const typed of [0, 9, ANSWER.length]) {
    const st = hubStateAt(3);
    st.answer = ANSWER.slice(0, typed);
    assertGrid(
      `typing ${typed} wide`,
      renderHub(st, COLS.hubWide),
      COLS.hubWide,
      ROWS.hub,
    );
    assertGrid(
      `typing ${typed} narrow`,
      renderHub(st, COLS.hubNarrow),
      COLS.hubNarrow,
      ROWS.hub,
    );
  }
  assertGrid("agents", renderAgents(), COLS.agents, ROWS.agents);
});
