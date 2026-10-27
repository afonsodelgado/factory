import { useEffect, useRef, type RefObject } from "react";

const VISIBLE_RATIO = 0.2;

interface Playback {
  target: RefObject<Element | null>;
  delay: () => number;
  tick: () => void;
  begin: () => void;
  still: () => void;
}

export function useDemoPlayback(playback: Playback) {
  const latest = useRef(playback);
  latest.current = playback;

  useEffect(() => {
    const element = latest.current.target.current?.closest(".win");
    if (!element) return;
    const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
    let ratio = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let wait = 0;
    let startedAt = 0;
    let remaining: number | null = null;
    let playing = false;

    const allowed = () =>
      ratio >= VISIBLE_RATIO && !document.hidden && !motion.matches;

    const prime = () => {
      if (playing || motion.matches) return;
      playing = true;
      latest.current.begin();
    };

    const start = () => {
      if (timer !== undefined || !allowed()) return;
      wait = remaining ?? latest.current.delay();
      remaining = null;
      startedAt = performance.now();
      timer = setTimeout(() => {
        timer = undefined;
        latest.current.tick();
        start();
      }, wait);
    };

    const pause = () => {
      if (timer === undefined) return;
      clearTimeout(timer);
      timer = undefined;
      remaining = Math.max(0, wait - (performance.now() - startedAt));
    };

    const update = () => (allowed() ? start() : pause());

    const onMotion = () => {
      if (motion.matches) {
        pause();
        remaining = null;
        playing = false;
        latest.current.still();
        return;
      }
      prime();
      update();
    };

    prime();

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) ratio = entry.intersectionRatio;
        update();
      },
      { threshold: [0, VISIBLE_RATIO, 0.5, 1] },
    );
    observer.observe(element);
    document.addEventListener("visibilitychange", update);
    motion.addEventListener("change", onMotion);

    return () => {
      clearTimeout(timer);
      observer.disconnect();
      document.removeEventListener("visibilitychange", update);
      motion.removeEventListener("change", onMotion);
    };
  }, []);
}
