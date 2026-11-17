import { useEffect, useState } from "react";
import { api } from "./api/client.ts";
import { FactoryDetail } from "./components/FactoryDetail.tsx";
import { FactoryList } from "./components/FactoryList.tsx";
import { AttentionItem } from "./components/AttentionItem.tsx";
import { Setup } from "./components/Setup.tsx";
import { ThisPhone } from "./components/ThisPhone.tsx";
import { AppShell, Refused } from "./components/AppShell.tsx";
import { paths, type Route } from "./route.ts";
import { useApp } from "./state.tsx";

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
  if (route.screen === "setup") return <Setup />;
  if (route.screen === "phone") return <ThisPhone />;
  if (route.screen === "link") return <LinkResolver notification={route.key} />;
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

function LinkResolver({ notification }: { notification: string }) {
  const { navigate, snapshot, reach } = useApp();
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    const saved = snapshot?.factories.find((f) =>
      f.items.some((i) => i.key === notification),
    );
    if (saved) {
      navigate(paths.item(saved.route, notification), true);
      return;
    }
    if (reach !== "online") {
      setMissing(true);
      return;
    }
    api.link(notification).then(
      (link) => navigate(paths.item(link.route, notification), true),
      () => setMissing(true),
    );
  }, [notification, snapshot, reach, navigate]);
  return (
    <AppShell back={paths.list()}>
      <p className="note">
        {missing
          ? "The details of this notification need the computer. Open factory again when this phone is back on the tailnet."
          : "Opening…"}
      </p>
    </AppShell>
  );
}
