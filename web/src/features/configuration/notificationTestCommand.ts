export type TestCommand={id:string;key:string;body:{expected_updated_at:string}};
export function testCommandKey(account:string,id:string){return `pulsemedia:notification-test:v1:${account}:${id}`}
export function readTestCommand(raw:string|null,id:string):TestCommand|null{
 if(raw===null)return null;
 const v:unknown=JSON.parse(raw);
 if(!v||typeof v!=='object'||Array.isArray(v))throw new Error('invalid record');
 const c=v as TestCommand;
 if(Object.keys(c).sort().join(',')!=='body,id,key'||c.id!==id||typeof c.key!=='string'||!/^[A-Za-z0-9_-]{1,160}$/.test(c.key)||!c.body||typeof c.body!=='object'||Array.isArray(c.body)||Object.keys(c.body).join(',')!=='expected_updated_at'||typeof c.body.expected_updated_at!=='string'||!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(c.body.expected_updated_at)||!Number.isFinite(Date.parse(c.body.expected_updated_at)))throw new Error('invalid record');
 return c;
}
export function persistTestCommand(storage:Storage,key:string,command:TestCommand){
 const raw=JSON.stringify(command);storage.setItem(key,raw);if(storage.getItem(key)!==raw)throw new Error('record not saved');
}
