export interface AutomationCommand {
 key: string;
 owner: string;
 scope: string;
 version: number;
 hash: string;
 sourceVersion?: number;
 rejected?: true;
}
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
export function automationStorageKey(owner: string, scope: string) {
 return `pulse.automation-settings.v1:${owner}:${scope}`;
}
export function parseAutomationCommand(text: string, owner: string, scope: string): AutomationCommand | null {
 if (text.length > 1024 || !uuid.test(owner)) return null;
 try {
  const v: unknown = JSON.parse(text);
  if (!v || typeof v !== 'object' || Array.isArray(v)) return null;
  const x = v as Record<string, unknown>;
  if (Object.keys(x).some(k => !['key','owner','scope','version','hash','sourceVersion','rejected'].includes(k))) return null;
  if (typeof x.key !== 'string' || !uuid.test(x.key) || x.owner !== owner || x.scope !== scope) return null;
  if (typeof x.version !== 'number' || !Number.isSafeInteger(x.version) || x.version < 0) return null;
  if (typeof x.hash !== 'string' || !/^[a-f0-9]{64}$/.test(x.hash)) return null;
  if (x.sourceVersion !== undefined && (typeof x.sourceVersion !== 'number' || !Number.isSafeInteger(x.sourceVersion) || x.sourceVersion < 1)) return null;
  if (x.rejected !== undefined && x.rejected !== true) return null;
  return {key:x.key,owner,scope,version:x.version,hash:x.hash,...(x.sourceVersion === undefined ? {} : {sourceVersion:x.sourceVersion as number}),...(x.rejected === true ? {rejected:true as const} : {})};
 } catch { return null; }
}
export function serializeAutomationCommand(command: AutomationCommand) {
 const text = JSON.stringify({key:command.key,owner:command.owner,scope:command.scope,version:command.version,hash:command.hash,...(command.sourceVersion === undefined ? {} : {sourceVersion:command.sourceVersion}),...(command.rejected === true ? {rejected:true} : {})});
 if (!parseAutomationCommand(text, command.owner, command.scope)) throw Error('Invalid recovery metadata');
 return text;
}
function canonical(value: unknown): unknown {
 if (Array.isArray(value)) return value.map(canonical);
 if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).filter(([,v])=>v !== undefined).sort(([a],[b])=>a.localeCompare(b)).map(([k,v])=>[k,canonical(v)]));
 return value;
}
export async function automationInputHash(input: unknown): Promise<string> {
 const bytes = new TextEncoder().encode(JSON.stringify(canonical(input)));
 return [...new Uint8Array(await crypto.subtle.digest('SHA-256',bytes))].map(x=>x.toString(16).padStart(2,'0')).join('');
}
