package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/google/uuid"
)

// Run: bash scripts/verify_m5_indexer_browser.sh browser
// JS is kept with the acceptance owner and materialized only under owned TMPDIR.
func TestM5IndexerActualBrowserHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M5_INDEXER_BROWSER") != "1" {
		t.Skip("set PULSE_M5_INDEXER_BROWSER=1 using the private browser wrapper")
	}
	assets, repo := os.Getenv("PULSE_M5_INDEXER_ASSETS"), os.Getenv("PULSE_M5_INDEXER_REPO")
	if !filepath.IsAbs(assets) || !strings.HasPrefix(assets, "/data/temp/") || !filepath.IsAbs(repo) {
		t.Fatal("owned assets and repository paths required")
	}
	if _, e := os.Stat(filepath.Join(assets, "index.html")); e != nil {
		t.Fatal(e)
	}
	f := newM5IndexerAcceptance(t)
	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{}
	// Keep production Origin validation intact while serving disposable frontend assets.
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = target.Host
		if pr.In.Header.Get("Origin") == "http://"+pr.In.Host {
			pr.Out.Header.Set("Origin", f.base)
		}
	}
	proxy.FlushInterval = -1
	files := http.FileServer(http.Dir(assets))
	var originalURL, originalKey []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/owned-m5/check" {
			phase, id := r.URL.Query().Get("phase"), r.URL.Query().Get("id")
			fail := func(message string) { t.Error(message); http.Error(w, "owned database assertion failed", 500) }
			if phase == "member" {
				if _, e := f.app.DBPool().Exec(r.Context(), `UPDATE app_user SET role='member' WHERE username='m5-native-admin'`); e != nil {
					fail("member role update failed")
					return
				}
			} else {
				parsed, e := uuid.Parse(id)
				if e != nil {
					fail("invalid owned check ID")
					return
				}
				var urlCipher, keyCipher []byte
				var version int
				var health string
				var deleted bool
				e = f.app.DBPool().QueryRow(r.Context(), `SELECT c.url_encrypted,i.api_key_encrypted,i.config_version,i.health,l.deleted_at IS NOT NULL FROM indexer i JOIN indexer_configuration c ON c.indexer_id=i.id JOIN indexer_lifecycle l ON l.indexer_id=i.id WHERE i.id=$1`, parsed).Scan(&urlCipher, &keyCipher, &version, &health, &deleted)
				if e != nil {
					fail("owned configuration check failed")
					return
				}
				decoded, e := crypto.NewSecretProtector(f.key).DecryptSecret(urlCipher)
				if e != nil || string(decoded) != f.rawURL {
					fail("browser configuration lost exact original URL")
					return
				}
				key, e := crypto.NewSecretProtector(f.key).DecryptSecret(keyCipher)
				if e != nil {
					fail("browser credential decryption failed")
					return
				}
				switch phase {
				case "created":
					originalURL = bytes.Clone(urlCipher)
					originalKey = bytes.Clone(keyCipher)
					if string(key) != "m5-fixture=credential" || version != 1 {
						fail("browser explicit native create mismatch")
						return
					}
				case "edited":
					if !bytes.Equal(urlCipher, originalURL) || !bytes.Equal(keyCipher, originalKey) || version != 2 {
						fail("browser name edit changed exact ciphertext")
						return
					}
				case "healthy":
					if health != "online" || f.caps.Load() != 1 || f.searches.Load() != 1 {
						fail("actual browser native caps/search health was not persisted")
						return
					}
				case "cleared":
					if len(key) != 0 || bytes.Equal(keyCipher, originalKey) || !bytes.Equal(urlCipher, originalURL) {
						fail("browser explicit clear failed")
						return
					}
				case "deleted":
					if !deleted {
						fail("browser delete was physical or not committed")
						return
					}
				case "lost":
					var n int
					if e = f.app.DBPool().QueryRow(r.Context(), `SELECT count(*) FROM indexer WHERE name='M5 lost response'`).Scan(&n); e != nil || n != 1 {
						fail("lost response created more than one native database source")
						return
					}
				default:
					fail("unknown owned database checkpoint")
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(server.Close)
	scratch := t.TempDir()
	script := filepath.Join(scratch, "m5-indexer-browser.mjs")
	if e := os.WriteFile(script, []byte(m5IndexerBrowserProgram), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M5_BROWSER_BASE="+server.URL, "PULSE_M5_BROWSER_SESSION="+f.session, "PULSE_M5_BROWSER_URL="+f.rawURL)
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("actual Chrome/Sites acceptance failed: %v\n%s", e, output)
	}
	if !strings.Contains(string(output), "M5_INDEXER_ACTUAL_CHROME_PASS") {
		t.Fatal("missing actual Chrome acceptance receipt")
	}
	f.durableSafe(t)
	var queries int
	if e = f.app.DBPool().QueryRow(t.Context(), `SELECT count(*) FROM indexer_query WHERE state='succeeded'`).Scan(&queries); e != nil || queries < 1 {
		t.Fatal("native successful history missing after UI deletion")
	}
	t.Log(strings.TrimSpace(string(output)))
}

const m5IndexerBrowserProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
const load=createRequire(process.env.PULSE_M5_INDEXER_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M5_BROWSER_BASE,rawURL=process.env.PULSE_M5_BROWSER_URL,credential='m5-fixture=credential';
const browser=await chromium.launch({executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
try{
 const context=await browser.newContext({viewport:{width:1440,height:1000}});
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_BROWSER_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 await context.addInitScript(()=>{
  window.__m5Resets=0;const Native=window.EventSource;
  window.EventSource=class extends Native{constructor(...args){super(...args);this.addEventListener('reset',()=>window.__m5Resets++);}};
 });
 const page=await context.newPage();page.setDefaultTimeout(20000);
 const errors=[],writes=[],receipts=[];
 page.on('pageerror',()=>errors.push('uncaught browser error'));
 page.on('request',r=>{const u=new URL(r.url());if(u.pathname==='/api/v1/indexers'&&r.method()==='POST')writes.push({key:r.headers()['idempotency-key'],body:r.postDataJSON()});if(u.pathname.startsWith('/api/v1/indexer-commands/'))receipts.push(u.pathname+u.search);});
 const name=()=>page.locator('#managed-site-name');
 const submit=()=>page.getByRole('button',{name:'保存并启用站点',exact:true});
 const row=n=>page.locator('.indexer-row').filter({has:page.getByText(n,{exact:true})});
 const response=(method,path,status)=>page.waitForResponse(r=>new URL(r.url()).pathname===path&&r.request().method()===method).then(r=>{assert.equal(r.status(),status,method+' '+path+' status');return r;});
 const settled=async()=>{await page.waitForFunction(()=>!document.querySelector('[aria-label="待确认站点命令"]'));await page.waitForFunction(()=>document.querySelector('#managed-site-name')?.value==='');};
 const safeStorage=async()=>{const text=await page.evaluate(()=>JSON.stringify({local:{...localStorage},session:{...sessionStorage}}));for(const s of [rawURL,credential,'m5-secret-path','m5-secret-user','m5-query-marker'])assert(!text.includes(s),'secret entered browser recovery storage');};
 const safeDOM=async()=>{const text=await page.locator('body').innerText();for(const s of [rawURL,credential,'m5-secret-path','m5-secret-user','m5-query-marker'])assert(!text.includes(s),'secret entered rendered response');assert.equal(await page.locator('#managed-site-url').inputValue(),'');assert.equal(await page.locator('#managed-site-credential').inputValue(),'');await safeStorage();};
 const db=async(phase,id)=>{const r=await context.request.get(base+'/owned-m5/check?phase='+phase+'&id='+encodeURIComponent(id||''));assert.equal(r.status(),200,'native private database checkpoint '+phase);};
 const list=async()=>{const r=await context.request.get(base+'/api/v1/indexers');assert.equal(r.status(),200);return (await r.json()).items;};
 async function fill(n,protocol='torznab',key=credential){await name().fill(n);await page.locator('#managed-site-protocol').selectOption(protocol);await page.locator('#managed-site-url').fill(rawURL);await page.locator('#managed-site-credential').fill(key);await page.locator('#managed-site-categories').fill('2000');}
 await page.goto(base+'/sites');await name().waitFor();await submit().waitFor({state:'visible'});
 await page.waitForFunction(()=>window.__m5Resets>0);
 await fill('M5 browser source');const creating=response('POST','/api/v1/indexers',201);await submit().click();const created=await (await creating).json();await settled();await row('M5 browser source').waitFor();const id=created.id;assert.equal(created.protocol,'torznab');assert.equal(created.url,new URL(rawURL).origin);assert.equal(writes[0].body.api_key,credential);await db('created',id);await safeDOM();
 const resetsBefore=await page.evaluate(()=>window.__m5Resets);
 const probe=response('POST','/api/v1/indexers/'+id+'/test',200);await row('M5 browser source').getByRole('button',{name:'测试连接',exact:true}).click();assert.equal((await (await probe).json()).alive,true);await row('M5 browser source').getByText('最近观测在线',{exact:true}).waitFor();await db('healthy',id);await page.waitForFunction(before=>window.__m5Resets>before,resetsBefore,{timeout:25000});
 await row('M5 browser source').getByRole('button',{name:'编辑',exact:true}).click();await name().fill('M5 browser renamed');const updating=response('PUT','/api/v1/indexers/'+id,200);await page.getByRole('button',{name:'保存站点修改',exact:true}).click();await updating;await settled();await row('M5 browser renamed').waitFor();await db('edited',id);await safeDOM();
 // Open an old-version draft, then commit an independent real public API edit.
 await row('M5 browser renamed').getByRole('button',{name:'编辑',exact:true}).click();const latest=(await list()).find(s=>s.id===id);
 const concurrent=await page.evaluate(async({id,version})=>{const r=await fetch('/api/v1/indexers/'+id,{method:'PUT',headers:{'Content-Type':'application/json','Idempotency-Key':crypto.randomUUID()},body:JSON.stringify({name:'M5 concurrent edit',protocol:'torznab',categories:[2000],expected_version:version})});return r.status;},{id,version:latest.config_version});assert.equal(concurrent,200);
 await name().fill('M5 stale UI draft');const stale=response('PUT','/api/v1/indexers/'+id,409);await page.getByRole('button',{name:'保存站点修改',exact:true}).click();await stale;await page.getByRole('alert').filter({hasText:'配置版本或命令键冲突'}).waitFor();assert(await page.getByRole('button',{name:'保存站点修改',exact:true}).isDisabled());
 await page.getByRole('button',{name:'重新加载配置（放弃草稿）',exact:true}).click();await row('M5 concurrent edit').waitFor();
 await row('M5 concurrent edit').getByRole('button',{name:'编辑',exact:true}).click();
 await page.getByLabel('清除独立 API Key / Cookie（地址中的认证信息仍保留）',{exact:true}).check();
 let holdReload=false,releaseReload;
 const reloadGate=new Promise(resolve=>{releaseReload=resolve;});
 const delayReload=async route=>{if(route.request().method()==='GET'&&holdReload)await reloadGate;await route.continue();};
 await page.route('**/api/v1/indexers',delayReload);
 try{
  const clearing=response('PUT','/api/v1/indexers/'+id,200).then(r=>{holdReload=true;return r;});
  await page.getByRole('button',{name:'保存站点修改',exact:true}).click();await clearing;await settled();await db('cleared',id);await safeDOM();
  assert(await row('M5 concurrent edit').getByRole('button',{name:'删除站点 M5 concurrent edit',exact:true}).isDisabled(),'old-version actions unlocked before confirmed list reload');
 }finally{holdReload=false;releaseReload();}
 // Keep the released route as pass-through until this owned context closes;
 // unroute would auto-handle requests whose delayed callbacks are still draining.
 page.once('dialog',d=>d.accept());const deleting=response('DELETE','/api/v1/indexers/'+id,204);await row('M5 concurrent edit').getByRole('button',{name:'删除站点 M5 concurrent edit',exact:true}).click();const deleteRequest=(await deleting).request();await row('M5 concurrent edit').waitFor({state:'detached'});await db('deleted',id);const deleteKey=deleteRequest.headers()['idempotency-key'];const deletedReceipt=await context.request.get(base+'/api/v1/indexer-commands/'+deleteKey+'?action=delete');assert.equal(deletedReceipt.status(),200);assert.equal((await deletedReceipt.json()).deleted,true);
 // Forward the POST to the native App/PG, then lose only the response transport.
 let committed=false;const drop=async route=>{if(route.request().method()!=='POST'){await route.continue();return;}const r=await route.fetch();assert.equal(r.status(),201);committed=true;await route.abort('failed');};
 await page.route('**/api/v1/indexers',drop);await fill('M5 lost response');const prior=writes.length;await submit().click();await page.getByRole('region',{name:'待确认站点命令'}).waitFor();await page.getByRole('alert').filter({hasText:/原请求已保留|原请求和命令键已保留/}).waitFor();assert(committed);assert.equal(writes.length,prior+1);const originalKey=writes.at(-1).key;await safeStorage();const lost=(await list()).find(s=>s.name==='M5 lost response');assert(lost);await db('lost',lost.id);await page.unroute('**/api/v1/indexers',drop);
 const recover=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/indexer-commands/'+originalKey&&r.request().method()==='GET'&&r.status()===200);await page.reload();const recovered=await (await recover).json();assert.equal(recovered.indexer.id,lost.id);assert.equal(recovered.indexer.config_version,1);assert.equal(recovered.action,'create');await settled();await row('M5 lost response').waitFor();assert.equal(writes.length,prior+1);await db('lost',lost.id);await safeDOM();
 // Actual narrow viewport and public RSS with deliberately empty credentials.
 await page.setViewportSize({width:375,height:812});await fill('M5 mobile RSS','rss','');const rss=response('POST','/api/v1/indexers',201);await submit().click();assert.equal((await (await rss).json()).protocol,'rss');await settled();await row('M5 mobile RSS').waitFor();await safeDOM();assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1),'375px horizontal overflow');
 await db('member');await page.reload();await page.getByRole('status').filter({hasText:'当前为成员账户'}).waitFor();assert.equal(await page.locator('#managed-site-name').count(),0);assert.equal(await page.getByRole('button',{name:'测试连接',exact:true}).count(),0);const forbidden=await page.evaluate(async()=>{const r=await fetch('/api/v1/indexers',{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':crypto.randomUUID()},body:JSON.stringify({name:'member blocked',protocol:'rss',url:'http://127.0.0.1',categories:[]})});return r.status;});assert.equal(forbidden,403);assert.deepEqual(errors,[]);
 console.log('M5_INDEXER_ACTUAL_CHROME_PASS '+JSON.stringify({realPG:true,realApp:true,cookie:true,desktop:true,mobile375:true,nativeTorznabCapsSearch:true,persistentHealth:true,actualSSEReset:true,exactCipherPreservation:true,explicitClear:true,freshVersionBeforeNextCommand:true,stale409Reread:true,lostResponseOneDBCreate:true,safeGETOriginalKey:true,noPostOnReload:true,noSecretStorage:true,logicalDelete:true,nonadminBlocked:true}));await context.close();
}finally{await browser.close();}
`
