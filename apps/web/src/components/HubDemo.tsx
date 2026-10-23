import { useRef, useState, type ReactNode } from "react";
import { HUB_TICK_MS, hubStart, hubStatic, hubTick } from "../lib/demo.ts";
import { COLS, renderHub } from "../lib/frames.ts";
import { useDemoPlayback } from "../lib/useDemoPlayback.ts";

const label =
  "The hub window. rate-limits asks two questions, g jumps to it, the answer is typed in the Needs attention panel and sent with ctrl+s, and the factory carries on.";

const captions: [number, ReactNode][] = [
  [1, "rate-limits asks two questions"],
  [
    2,
    <span>
      <kbd>g</kbd> jumps to it
    </span>,
  ],
  [3, "you answer in the panel"],
  [
    4,
    <span>
      <kbd>ctrl+s</kbd> sends it, and it carries on
    </span>,
  ],
];

export default function HubDemo({ children }: { children?: ReactNode }) {
  const win = useRef<HTMLDivElement>(null);
  const [playback, setPlayback] = useState(hubStatic);

  useDemoPlayback({
    target: win,
    delay: () => HUB_TICK_MS,
    tick: () => setPlayback(hubTick),
    begin: () => setPlayback(hubStart()),
    still: () => setPlayback(hubStatic()),
  });

  const { state } = playback;
  return (
    <>
      <div className="frame hubframe">
        <div className="win" ref={win}>
          <div className="bar">
            <span className="lights" aria-hidden="true">
              <i />
              <i />
              <i />
            </span>
            <span className="wt">devbox:hub</span>
            <span className="dim">
              <span className="dim-wide">106×28</span>
              <span className="dim-narrow">56×28</span>
            </span>
          </div>
          <div className="scroll" role="img" aria-label={label}>
            <pre
              className="term hub-wide"
              dangerouslySetInnerHTML={{
                __html: renderHub(state, COLS.hubWide),
              }}
            />
            <pre
              className="term hub-narrow"
              dangerouslySetInnerHTML={{
                __html: renderHub(state, COLS.hubNarrow),
              }}
            />
          </div>
        </div>
      </div>
      <div className="below">
        <ol className="steps" aria-label="What the demo shows">
          {captions.map(([step, text]) => (
            <li
              key={step}
              data-step={step}
              className={step === state.step ? "on" : undefined}
            >
              <b>{step}</b>
              {text}
            </li>
          ))}
        </ol>
        {children}
      </div>
    </>
  );
}
