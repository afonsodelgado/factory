import type { Receipt, Snapshot } from "./types.ts";

export const DATABASE = "factory-phone";
const STORE = "records";

export type SavedSnapshot = { savedAt: string; snapshot: Snapshot };
export type Draft = { answers?: string[]; text?: string; notes?: string };
export type MessageResult = { text: string; receipt: Receipt; at: string };
export type Pending = {
  operation: string;
  factory: string;
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

const scope = (device: string, incarnation: string) =>
  `${device}:${incarnation}`;

export const cache = {
  device: () => read<string>("device"),
  setDevice: (device: string) => write("device", device),

  snapshot: (device: string) => read<SavedSnapshot>(`snapshot:${device}`),
  saveSnapshot: (device: string, snapshot: Snapshot) =>
    write(`snapshot:${device}`, {
      savedAt: new Date().toISOString(),
      snapshot,
    } satisfies SavedSnapshot),

  draft: (device: string, incarnation: string, item: string) =>
    read<Draft>(`draft:${scope(device, incarnation)}:${item}`),
  saveDraft: (
    device: string,
    incarnation: string,
    item: string,
    draft: Draft,
  ) => write(`draft:${scope(device, incarnation)}:${item}`, draft),
  dropDraft: (device: string, incarnation: string, item: string) =>
    remove(`draft:${scope(device, incarnation)}:${item}`),

  messages: async (device: string, incarnation: string) =>
    (await read<MessageResult[]>(`messages:${scope(device, incarnation)}`)) ??
    [],
  addMessage: async (
    device: string,
    incarnation: string,
    result: MessageResult,
  ) => {
    const recent = await cache.messages(device, incarnation);
    await write(
      `messages:${scope(device, incarnation)}`,
      [
        result,
        ...recent.filter(
          (r) => r.receipt.operation !== result.receipt.operation,
        ),
      ].slice(0, 3),
    );
  },

  pending: async (device: string) =>
    (await read<Pending[]>(`pending:${device}`)) ?? [],
  addPending: async (device: string, pending: Pending) =>
    write(`pending:${device}`, [...(await cache.pending(device)), pending]),
  settlePending: async (device: string, operation: string) =>
    write(
      `pending:${device}`,
      (await cache.pending(device)).filter((p) => p.operation !== operation),
    ),

  pruneDrafts: async (device: string, openKeys: Set<string>) => {
    const prefix = `draft:${device}:`;
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
