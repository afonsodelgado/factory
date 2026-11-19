import type { Receipt, Roster, Snapshot } from "./types.ts";

export const DATABASE = "factory-phone";
const STORE = "records";

export type SavedSnapshot = { savedAt: string; snapshot: Snapshot };
export type Draft = { answers?: string[]; text?: string; notes?: string };
export type MessageResult = { text: string; receipt: Receipt; at: string };
export type Pending = {
  operation: string;
  factory: string;
  node?: string;
  item?: string;
  at: string;
};

let opened: Promise<IDBDatabase> | undefined;

function database(): Promise<IDBDatabase> {
  opened ??= new Promise((resolve, reject) => {
    const request = indexedDB.open(DATABASE, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  return opened;
}

async function run<T>(
  mode: IDBTransactionMode,
  work: (store: IDBObjectStore) => IDBRequest<T>,
): Promise<T> {
  const db = await database();
  return new Promise((resolve, reject) => {
    const request = work(db.transaction(STORE, mode).objectStore(STORE));
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

const read = <T>(key: string) =>
  run<T | undefined>("readonly", (store) => store.get(key));
const write = (key: string, value: unknown) =>
  run("readwrite", (store) => store.put(value, key));
const remove = (key: string) => run("readwrite", (store) => store.delete(key));
const keys = () =>
  run<IDBValidKey[]>("readonly", (store) => store.getAllKeys());

const scope = (account: string, incarnation: string) =>
  `${account}:${incarnation}`;

export const accountOf = (node: string, owner: string) => `${node}/${owner}`;
export const ownerOf = (account: string) =>
  account.slice(account.indexOf("/") + 1);

export const cache = {
  installation: async () => {
    const known = await read<string>("installation");
    if (known) return known;
    const fresh = crypto.randomUUID();
    await write("installation", fresh);
    return fresh;
  },
  movedFrom: () => read<string>("moved-from"),
  setMovedFrom: (installation: string) => write("moved-from", installation),
  forgetMovedFrom: () => remove("moved-from"),
  account: () => read<string>("account"),
  setAccount: (account: string) => write("account", account),

  adoptLegacy: async (account: string) => {
    const device = await read<string>("device");
    if (!device) return;
    for (const key of await keys()) {
      const name = String(key);
      const [kind, owner, ...rest] = name.split(":");
      if (owner !== device) continue;
      if (kind !== "snapshot")
        await write([kind, account, ...rest].join(":"), await read(name));
      await remove(name);
    }
    await remove("device");
  },

  roster: (account: string) => read<Roster>(`roster:${account}`),
  saveRoster: (account: string, roster: Roster) =>
    write(`roster:${account}`, roster),
  announced: (account: string) => read<string[]>(`announced:${account}`),
  announce: (account: string, nodes: string[]) =>
    write(`announced:${account}`, nodes),

  keepNodes: async (owner: string, nodes: string[]) => {
    for (const key of await keys()) {
      const name = String(key);
      const scope = name.split(":")[1] ?? "";
      const node = scope.slice(0, scope.indexOf("/"));
      const unlisted =
        scope.endsWith(`/${owner}`) && node && !nodes.includes(node);
      if (unlisted) await remove(name);
    }
  },

  snapshot: (account: string) => read<SavedSnapshot>(`snapshot:${account}`),
  saveSnapshot: (account: string, snapshot: Snapshot) =>
    write(`snapshot:${account}`, {
      savedAt: new Date().toISOString(),
      snapshot,
    } satisfies SavedSnapshot),

  draft: (account: string, incarnation: string, item: string) =>
    read<Draft>(`draft:${scope(account, incarnation)}:${item}`),
  saveDraft: (
    account: string,
    incarnation: string,
    item: string,
    draft: Draft,
  ) => write(`draft:${scope(account, incarnation)}:${item}`, draft),
  dropDraft: (account: string, incarnation: string, item: string) =>
    remove(`draft:${scope(account, incarnation)}:${item}`),

  messages: async (account: string, incarnation: string) =>
    (await read<MessageResult[]>(`messages:${scope(account, incarnation)}`)) ??
    [],
  saveMessages: (
    account: string,
    incarnation: string,
    results: MessageResult[],
  ) => write(`messages:${scope(account, incarnation)}`, results),

  pending: async (account: string) =>
    (await read<Pending[]>(`pending:${account}`)) ?? [],
  addPending: async (account: string, pending: Pending) =>
    write(`pending:${account}`, [...(await cache.pending(account)), pending]),
  settlePending: async (account: string, operation: string) =>
    write(
      `pending:${account}`,
      (await cache.pending(account)).filter((p) => p.operation !== operation),
    ),

  pruneDrafts: async (account: string, openKeys: Set<string>) => {
    const prefix = `draft:${account}:`;
    for (const key of await keys()) {
      const name = String(key);
      const item = name.slice(name.lastIndexOf(":") + 1);
      if (name.startsWith(prefix) && !openKeys.has(item)) await remove(name);
    }
  },

  purge: async () => {
    if (opened) (await opened).close();
    opened = undefined;
    await new Promise<void>((resolve) => {
      const request = indexedDB.deleteDatabase(DATABASE);
      request.onsuccess = request.onerror = request.onblocked = () => resolve();
    });
  },
};
