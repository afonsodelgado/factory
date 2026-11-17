import type { LocalFactory, LocalPage, Walkthrough } from "../api/types.ts";

export function Pages({ factory }: { factory: LocalFactory }) {
  const walkthrough = factory.walkthrough ? (
    <ul className="items">
      <li>
        <WalkthroughRow walkthrough={factory.walkthrough} />
      </li>
    </ul>
  ) : null;
  if (factory.pages.length === 0)
    return (
      walkthrough ?? (
        <div className="empty">
          <p className="ink">No pages yet.</p>
          <p className="hint sm">
            Design and plan pages show up here once the designer or the
            architect writes them.
          </p>
        </div>
      )
    );
  return (
    <>
      {walkthrough}
      <p className="pad sm mute">
        Design and plan pages. Open-thread counts come from the supervisor's
        comment watch.
      </p>
      <PageList pages={factory.pages} />
    </>
  );
}

function WalkthroughRow({ walkthrough }: { walkthrough: Walkthrough }) {
  if (walkthrough.state === "ready" && walkthrough.href)
    return (
      <a className="it" href={walkthrough.href}>
        <span className="ic grn" aria-hidden="true">
          ▶
        </span>
        <span className="ink b">Walkthrough</span>
        <span className="sm grn">running</span>
        <span className="s">
          {walkthrough.origin?.replace("https://", "")} · open signed in ›
        </span>
      </a>
    );
  const failed = walkthrough.state === "failed";
  return (
    <span className="it off" aria-disabled="true">
      <span className={`ic ${failed ? "red" : "dk"}`} aria-hidden="true">
        ▶
      </span>
      <span className="dk">Walkthrough</span>
      <span className={`sm ${failed ? "red" : "dk"}`}>
        {failed ? `failed at ${walkthrough.failure}` : "starting…"}
      </span>
      <span className="s">
        {failed ? walkthrough.reason : "The node is starting the app's stack."}
      </span>
    </span>
  );
}

export function PageList({ pages }: { pages: LocalPage[] }) {
  return (
    <ul className="items">
      {pages.map((page) => (
        <li key={page.name}>
          <PageRow page={page} />
        </li>
      ))}
    </ul>
  );
}

function PageRow({ page }: { page: LocalPage }) {
  const threads = page.open_threads ?? 0;
  const count = (
    <span className={`sm ${threads ? "cyn" : "dk"}`}>
      <span aria-hidden="true">✎ </span>
      {page.open_threads === undefined ? "–" : `${threads} open`}
    </span>
  );
  if (!page.href)
    return (
      <span className="it off" aria-disabled="true">
        <span className="ic dk" aria-hidden="true">
          ▤
        </span>
        <span className="dk">{page.title || page.name}</span>
        {count}
        <span className="s">
          {page.name} · {page.unavailable ?? "It can't be opened here."}
        </span>
      </span>
    );
  return (
    <a className="it" href={page.href}>
      <span className="ic blu" aria-hidden="true">
        ▤
      </span>
      <span className="ink b">{page.title || page.name}</span>
      {count}
      <span className="s">{page.name} · open on the phone ›</span>
    </a>
  );
}
