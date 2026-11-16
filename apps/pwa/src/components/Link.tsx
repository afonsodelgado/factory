import type { AnchorHTMLAttributes, MouseEvent } from "react";
import { useApp } from "../state.tsx";

const modified = (event: MouseEvent) =>
  event.metaKey || event.ctrlKey || event.shiftKey || event.altKey;

export function Link(
  props: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string },
) {
  const { navigate } = useApp();
  const follow = (event: MouseEvent<HTMLAnchorElement>) => {
    if (modified(event) || event.button !== 0) return;
    event.preventDefault();
    navigate(props.href);
  };
  return <a {...props} onClick={follow} />;
}
