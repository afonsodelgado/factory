import type { LocalFactory, LocalPage, ReviewStack } from "../api/types.ts";

export function Pages({ factory }: { factory: LocalFactory }) {
  const reviewStack = factory.review_stack ? (
    <ul className="items">
      <li>
        <ReviewStackRow
          reviewStack={factory.review_stack}
          repo={factory.repo_name}
        />
      </li>
    </ul>
  ) : null;
  if (factory.pages.length === 0)
    return (
      reviewStack ?? (
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
      {reviewStack}
      <p className="pad sm mute">
        Design and plan pages. Open-thread counts come from the supervisor's
        comment watch.
      </p>
      <PageList pages={factory.pages} />
    </>
  );
}

function ReviewStackRow({
  reviewStack,
  repo,
}: {
  reviewStack: ReviewStack;
  repo: string;
}) {
  if (reviewStack.state === "ready" && reviewStack.href)
    return (
      <a className="it" href={reviewStack.href}>
        <span className="ic grn" aria-hidden="true">
          ▶
        </span>
        <span className="ink b">Review stack</span>
        <span className="sm grn">running</span>
        <span className="s">
          {reviewStack.origin?.replace("https://", "")} ·{" "}
          {reviewStack.signed_in ? "open signed in" : "opens signed out"} ›
        </span>
        {reviewStack.signed_in ? null : (
          <span className="s">
            {repo} has no sign-in command, so sign in or sign up yourself, as QA
            did.
          </span>
        )}
      </a>
    );
  const failed = reviewStack.state === "failed";
  return (
    <span className="it off" aria-disabled="true">
      <span className={`ic ${failed ? "red" : "dk"}`} aria-hidden="true">
        ▶
      </span>
      <span className="dk">Review stack</span>
      <span className={`sm ${failed ? "red" : "dk"}`}>
        {failed ? `failed at ${reviewStack.failure}` : "starting…"}
      </span>
      <span className="s">
        {failed ? reviewStack.reason : "The node is starting the app's stack."}
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
