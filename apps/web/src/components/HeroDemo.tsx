import { useRef, useState } from "react";
import { HERO_STATES, heroDelay, heroStateAt } from "../lib/demo.ts";
import { renderHero } from "../lib/frames.ts";
import { useDemoPlayback } from "../lib/useDemoPlayback.ts";

export default function HeroDemo() {
  const pre = useRef<HTMLPreElement>(null);
  const index = useRef(0);
  const [shown, setShown] = useState(0);

  const show = (next: number) => {
    index.current = next;
    setShown(next);
  };

  useDemoPlayback({
    target: pre,
    delay: () => heroDelay(index.current),
    tick: () => show((index.current + 1) % HERO_STATES),
    begin: () => show(0),
    still: () => show(0),
  });

  return (
    <pre
      ref={pre}
      className="term term-hero"
      dangerouslySetInnerHTML={{ __html: renderHero(heroStateAt(shown)) }}
    />
  );
}
