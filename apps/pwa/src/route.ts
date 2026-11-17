export type Tab = "attention" | "pages" | "message";

export type Route =
  | { screen: "list" }
  | { screen: "factory"; route: string; tab: Tab }
  | { screen: "item"; route: string; item: string }
  | { screen: "link"; key: string }
  | { screen: "setup" }
  | { screen: "phone" }
  | { screen: "node"; id: string }
  | { screen: "add" }
  | { screen: "check"; name: string };

export function parse(location: Pick<Location, "pathname">): Route {
  const parts = location.pathname
    .split("/")
    .filter(Boolean)
    .map(decodeURIComponent);
  switch (parts[0]) {
    case "setup":
      return { screen: "setup" };
    case "phone":
      if (parts[1] === "nodes" && parts[2])
        return { screen: "node", id: parts[2] };
      if (parts[1] === "add" && parts[2])
        return { screen: "check", name: parts[2] };
      if (parts[1] === "add") return { screen: "add" };
      return { screen: "phone" };
    case "n":
      if (parts[1]) return { screen: "link", key: parts[1] };
      break;
    case "f":
      if (!parts[1]) break;
      if (parts[2] === "i" && parts[3])
        return { screen: "item", route: parts[1], item: parts[3] };
      if (parts[2] === "pages" || parts[2] === "message")
        return { screen: "factory", route: parts[1], tab: parts[2] };
      return { screen: "factory", route: parts[1], tab: "attention" };
  }
  return { screen: "list" };
}

export const paths = {
  list: () => "/",
  factory: (route: string, tab: Tab = "attention") =>
    `/f/${encodeURIComponent(route)}${tab === "attention" ? "" : "/" + tab}`,
  item: (route: string, item: string) =>
    `/f/${encodeURIComponent(route)}/i/${encodeURIComponent(item)}`,
  setup: () => "/setup",
  phone: () => "/phone",
  node: (id: string) => `/phone/nodes/${encodeURIComponent(id)}`,
  add: () => "/phone/add",
  check: (name: string) => `/phone/add/${encodeURIComponent(name)}`,
};
