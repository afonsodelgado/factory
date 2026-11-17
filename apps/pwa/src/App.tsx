import { useEffect, useState } from "react";
import { nodeApi } from "./api/client.ts";
import { FactoryDetail } from "./components/FactoryDetail.tsx";
import { FactoryList } from "./components/FactoryList.tsx";
import { AttentionItem } from "./components/AttentionItem.tsx";
import { AddNode, CheckNode, NodeDetail } from "./components/Nodes.tsx";
import { Exposed, Setup } from "./components/Setup.tsx";
import { ThisPhone } from "./components/ThisPhone.tsx";
import { AppShell, Refused } from "./components/AppShell.tsx";
import { paths, type Route } from "./route.ts";
import { hostName } from "./format.ts";
import { routeOn, useApp } from "./state.tsx";

const WIDE = "(min-width: 900px)";

function useWide(): boolean {
  const [wide, setWide] = useState(() => matchMedia(WIDE).matches);
  useEffect(() => {
    const query = matchMedia(WIDE);
    const change = () => setWide(query.matches);
    query.addEventListener("change", change);
    return () => query.removeEventListener("change", change);
  }, []);
  return wide;
}

export function App() {
  const { route, reach, snapshot } = useApp();
  const wide = useWide();
  if (reach === "refused") return <Refused />;
  if (reach === "funnel") return <Exposed />;
  if (route.screen === "setup") return <Setup />;
  if (route.screen === "phone") return <ThisPhone />;
  if (route.screen === "node") return <NodeDetail id={route.id} />;
  if (route.screen === "add") return <AddNode />;
  if (route.screen === "check") return <CheckNode name={route.name} />;
  if (route.screen === "link")
    return <LinkResolver node={route.node} notification={route.key} />;
  if (!wide) return <Stack route={route} />;
  const selected =
    route.screen === "list"
      ? snapshot?.factories[0]?.route
      : "route" in route
        ? route.route
        : undefined;
  return (
    <div className="panes">
      <FactoryList selected={selected} />
      {selected ? <Detail route={route} selected={selected} /> : null}
    </div>
  );
}

function Stack({ route }: { route: Route }) {
  if (route.screen === "factory")
    return <FactoryDetail route={route.route} tab={route.tab} />;
  if (route.screen === "item")
    return <AttentionItem route={route.route} itemKey={route.item} />;
  return <FactoryList />;
}

function Detail({ route, selected }: { route: Route; selected: string }) {
  if (route.screen === "item")
    return <AttentionItem route={route.route} itemKey={route.item} />;
  const tab = route.screen === "factory" ? route.tab : "attention";
  return <FactoryDetail route={selected} tab={tab} />;
}

function LinkResolver(props: { node: string; notification: string }) {
  const { navigate, nodes, reach } = useApp();
  const node = nodes.find((n) => n.id === props.node);
  const saved = node?.snapshot?.factories.find((f) =>
    f.items.some((i) => i.key === props.notification),
  );
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (!node) return;
    const open = (route: string) =>
      navigate(paths.item(routeOn(node, route), props.notification), true);
    if (saved) return open(saved.route);
    if (!node.live) return;
    let current = true;
    nodeApi(node.origin)
      .link(props.notification)
      .then(
        (link) => current && open(link.route),
        () => current && setFailed(true),
      );
    return () => {
      current = false;
    };
  }, [node, saved, props.notification, navigate]);
  const missing = failed || (node ? !saved && node.lost : reach !== "loading");
  return (
    <AppShell back={paths.list()}>
      <p className="note">
        {missing
          ? `The details of this notification need ${node?.short ?? hostName()}. Open factory again when this phone is back on the tailnet.`
          : "Opening…"}
      </p>
    </AppShell>
  );
}
