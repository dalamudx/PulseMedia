export type PushCommand = {
  owner: string;
  key: string;
  id: string;
  kind: "register" | "revoke";
  hash: string;
  vapidVersion?: string;
  expectedUpdatedAt?: string;
};
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
export const pushStorageKey = (owner: string) => `pulse.webpush.command.v1:${owner}`;
export function parsePushCommand(raw: string, owner: string): PushCommand | null {
  if (raw.length > 1024 || !uuid.test(owner)) return null;
  try {
    const x = JSON.parse(raw) as Record<string, unknown>;
    if (!x || typeof x !== "object" || Array.isArray(x) || Object.keys(x).some(k => !["owner", "key", "id", "kind", "hash", "vapidVersion", "expectedUpdatedAt"].includes(k))) return null;
    if (x.owner !== owner || typeof x.key !== "string" || !uuid.test(x.key) || typeof x.id !== "string" || !uuid.test(x.id) || typeof x.hash !== "string" || !/^[a-f0-9]{64}$/.test(x.hash)) return null;
    if (x.kind === "register" && typeof x.vapidVersion === "string" && /^[a-f0-9]{64}$/.test(x.vapidVersion) && x.expectedUpdatedAt === undefined) return x as PushCommand;
    if (x.kind === "revoke" && typeof x.expectedUpdatedAt === "string" && !Number.isNaN(Date.parse(x.expectedUpdatedAt)) && x.vapidVersion === undefined) return x as PushCommand;
    return null;
  } catch { return null; }
}
export function serializePushCommand(command: PushCommand) {
  const raw = JSON.stringify(command);
  if (!parsePushCommand(raw, command.owner)) throw Error("Invalid browser push command");
  return raw;
}
