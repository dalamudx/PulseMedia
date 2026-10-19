package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Run only through scripts/verify_m6_site_probes_ui.sh after the UI owner is ready.
func TestM6SiteProbesActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_SITE_PROBES_BROWSER") != "1" {
		t.Skip("use the private M6 site probes wrapper")
	}
	assets, repo, shots := os.Getenv("PULSE_M6_SITE_PROBES_ASSETS"), os.Getenv("PULSE_M6_SITE_PROBES_REPO"), os.Getenv("PULSE_M6_SITE_PROBES_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("owned paths under /data/temp required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository path required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f := newM5IndexerAcceptance(t)
	m6WaitInitialLibraryPipeline(t, f)
	before := m6SiteProbeBusinessSnapshot(t, f)
	target, err := url.Parse(f.base)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{FlushInterval: -1}
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = target.Host
		if pr.In.Header.Get("Origin") == "http://"+pr.In.Host {
			pr.Out.Header.Set("Origin", f.base)
		}
	}
	type encrypted struct{ url, key []byte }
	frozen := map[uuid.UUID]encrypted{}
	var mu sync.Mutex
	files := http.FileServer(http.Dir(assets))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		// This fixture-only observer reads PG facts; it never supplies probe outcomes.
		if r.URL.Path == "/owned-m6/check" {
			mu.Lock()
			defer mu.Unlock()
			fail := func(message string) { t.Error(message); http.Error(w, "owned database checkpoint failed", 500) }
			if r.URL.Query().Get("phase") == "demote" {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if _, e := f.app.DBPool().Exec(r.Context(), `UPDATE app_user SET role='member' WHERE username='m5-native-admin' AND role='admin'`); e != nil {
					fail("current account demotion failed")
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			id, e := uuid.Parse(r.URL.Query().Get("id"))
			if e != nil {
				fail("invalid source checkpoint ID")
				return
			}
			var u, k []byte
			var health, reason string
			var queries int
			e = f.app.DBPool().QueryRow(r.Context(), `SELECT c.url_encrypted,i.api_key_encrypted,i.health,i.health_reason,(SELECT count(*) FROM indexer_query q WHERE q.indexer_id=i.id AND q.state='succeeded') FROM indexer i JOIN indexer_configuration c ON c.indexer_id=i.id WHERE i.id=$1`, id).Scan(&u, &k, &health, &reason, &queries)
			if e != nil {
				fail("native source observation read failed")
				return
			}
			if r.URL.Query().Get("phase") == "freeze" {
				if _, exists := frozen[id]; exists {
					fail("ciphertext checkpoint cannot be replaced")
					return
				}
				frozen[id] = encrypted{bytes.Clone(u), bytes.Clone(k)}
			} else if old, exists := frozen[id]; !exists || !bytes.Equal(old.url, u) || !bytes.Equal(old.key, k) {
				fail("probes or name-only management changed frozen ciphertext")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if e = json.NewEncoder(w).Encode(map[string]any{"health": health, "reason": reason, "queries": queries, "caps": f.caps.Load(), "searches": f.searches.Load()}); e != nil {
				t.Error(e)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(server.Close)
	script := filepath.Join(t.TempDir(), "m6-site-probes.mjs")
	if err = os.WriteFile(script, []byte(m6SiteProbesChromeProgram), 0600); err != nil {
		t.Fatal(err)
	}
	// Screenshot review happens in the wrapper after this business deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M6_SITE_PROBES_BASE="+server.URL, "PULSE_M6_SITE_PROBES_SESSION="+f.session, "PULSE_M6_SITE_PROBES_URL="+f.rawURL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual Chrome/native site probe acceptance: %v\n%s", err, output)
	}
	for _, marker := range []string{"M6_SITE_PROBES_BROWSER_PASS", "M6_SITE_PROBES_CHROME_CLEANUP_VERIFIED"} {
		if strings.Count(string(output), marker) != 1 {
			t.Fatalf("missing or repeated browser receipt %s\n%s", marker, output)
		}
	}
	t.Log(strings.TrimSpace(string(output)))
	if after := m6SiteProbeBusinessSnapshot(t, f); after != before {
		var previous, current map[string]json.RawMessage
		if e := json.Unmarshal([]byte(before), &previous); e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal([]byte(after), &current); e != nil {
			t.Fatal(e)
		}
		var changed []string
		for table, old := range previous {
			if !bytes.Equal(old, current[table]) {
				changed = append(changed, table)
			}
		}
		t.Fatalf("site probes changed protected jobs/tasks/reviews/reservations/operations/file facts; tables=%v", changed)
	}
	for id, old := range frozen {
		u, k := f.configuration(t, id.String())
		if !bytes.Equal(u, old.url) || !bytes.Equal(k, old.key) {
			t.Fatal("terminal encrypted execution configuration changed")
		}
		f.checkPlain(t, u, f.rawURL)
		f.checkPlain(t, k, "m5-fixture=credential")
	}
	if len(frozen) < 3 || f.caps.Load() < 1 || f.caps.Load() != f.searches.Load() {
		t.Fatal("missing genuine bounded Torznab capabilities/search evidence")
	}
	f.durableSafe(t)
	t.Log("M6_SITE_PROBES_APP_COOKIE_PG_PASS protected_business_mutations=0 frozen_ciphertexts=unchanged native_caps_search=true")
	t.Log("M6_SITE_PROBES_SCREENSHOTS_READY " + shots)
}

func m6WaitInitialLibraryPipeline(t *testing.T, f *m5IndexerAcceptance) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	// App.Run scans the two default roots and atomically queues missing work.
	// Keep every job identity/payload in the later snapshot, including this work.
	const settled = `SELECT EXISTS(SELECT 1 FROM library_root)
	 AND NOT EXISTS (
	  SELECT 1 FROM library_root r WHERE NOT r.baseline_completed OR NOT EXISTS (
	   SELECT 1 FROM library_scan_schedule schedule
	   JOIN library_scan_job scan ON scan.job_id=schedule.last_scan_job_id AND scan.root_id=r.id AND scan.phase='scan'
	   JOIN job scan_job ON scan_job.id=scan.job_id AND scan_job.kind='library_scan' AND scan_job.state='succeeded'
	   JOIN library_scan_job missing ON missing.root_id=r.id AND missing.run_id=scan.run_id AND missing.phase='missing'
	   JOIN job missing_job ON missing_job.id=missing.job_id AND missing_job.kind='library_missing' AND missing_job.state='succeeded'
	   WHERE schedule.root_id=r.id
	  )
	 ) AND NOT EXISTS(SELECT 1 FROM job WHERE kind IN('library_scan','library_missing') AND state<>'succeeded')`
	for {
		var ready bool
		if err := f.app.DBPool().QueryRow(ctx, settled).Scan(&ready); err != nil {
			t.Fatal("initial library pipeline readiness read failed", err)
		}
		if ready {
			t.Log("M6_SITE_PROBES_INITIAL_LIBRARY_PIPELINE_SETTLED")
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("initial default-root scan and missing reconciliation did not finish")
		case <-ticker.C:
		}
	}
}

func m6SiteProbeBusinessSnapshot(t *testing.T, f *m5IndexerAcceptance) string {
	t.Helper()
	// Health, queries, budgets and management audit/receipts/events are expected
	// writes. Preserve complete protected rows, including every operation binding.
	tables := []string{"download_task", "episode_candidate_review", "tv_coverage_candidate_review", "unit_reservation", "acquisition_reservation", "download_requirement", "transfer_plan", "transfer_step", "library_file", "library_file_unit", "unit_fact", "download_file_inventory", "release_candidate", "search_round", "interactive_search", "subscription", "wanted_item", "automatic_tv_round", "automatic_tv_execution_authority", "movie_upgrade_authority", "complete_tv_execution_authority"}
	parts := []string{`'job',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,kind,payload) ORDER BY id),'[]') FROM job)`}
	for _, table := range tables {
		parts = append(parts, fmt.Sprintf(`'%s',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]') FROM %s x)`, table, table))
	}
	var snapshot string
	if err := f.app.DBPool().QueryRow(t.Context(), "SELECT jsonb_build_object("+strings.Join(parts, ",")+")::text").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

const m6SiteProbesChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {mkdir} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M6_SITE_PROBES_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M6_SITE_PROBES_BASE, rawURL=process.env.PULSE_M6_SITE_PROBES_URL;
const shots=process.env.PULSE_M6_SITE_PROBES_SCREENSHOTS;
const browser=await chromium.launch({executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking']});
try {
 const context=await browser.newContext({viewport:{width:1280,height:1000},serviceWorkers:'block'});
 const session={name:'pulsemedia_session',value:process.env.PULSE_M6_SITE_PROBES_SESSION,url:base,httpOnly:true,sameSite:'Strict'};
 await context.addCookies([session]);
 const page=await context.newPage();page.setDefaultTimeout(12000);
 const errors=[],routeErrors=[],sent=[],successes=[];let active=0,maxActive=0,probeHook=null,listHook=null;
 page.on('pageerror',e=>errors.push(e.message));
 const eventually=async(fn,message)=>{const end=Date.now()+12000;while(Date.now()<end){if(await fn())return;await new Promise(r=>setTimeout(r,30));}assert.fail(message);};
 function gate(){let release,ready;const timers=new Set();const bounded=p=>{let timer;const deadline=new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('owned response gate exceeded 25s')),25000);timers.add(timer);});return Promise.race([p,deadline]).finally(()=>{clearTimeout(timer);timers.delete(timer);});};const opened=bounded(new Promise(r=>{ready=r;}));opened.catch(()=>{});const wait=new Promise(r=>{release=r;});return {opened,mark:ready,release:()=>{for(const timer of timers)clearTimeout(timer);timers.clear();release();},wait:()=>bounded(wait)};}
 const all=()=>page.getByRole('button',{name:'测试全部站点',exact:true});
 const summary=()=>page.getByRole('region',{name:'本轮站点检测',exact:true});
 const row=n=>page.locator('.indexer-row').filter({has:page.getByText(n,{exact:true})});
 const result=(n,state)=>row(n).locator('.site-probe-result[data-state="'+state+'"]');
 const list=async()=>{const r=await context.request.get(base+'/api/v1/indexers');assert.equal(r.status(),200);return (await r.json()).items;};
 const db=async(phase,id)=>{const r=await context.request.get(base+'/owned-m6/check?phase='+phase+'&id='+id);assert.equal(r.status(),200,'PG '+phase);return r.json();};
 const honest=async id=>{const d=await db('check',id);assert.equal(d.health,'online');assert.equal(d.reason,'ok');assert.equal(d.queries,1);assert(d.caps>0&&d.searches>0);const s=(await list()).find(s=>s.id===id);assert.equal(s.health,'online');assert(s.health_checked_at);};
 const mutate=async(method,path,body)=>{const r=await context.request.fetch(base+path,{method,headers:{Origin:base,'Idempotency-Key':crypto.randomUUID()},...(body?{data:body}:{})});return r;};
 const mount=async()=>{await page.goto(base+'/sites');await page.getByRole('heading',{name:/站点.*管理/}).waitFor();await eventually(()=>all().isEnabled(),'admin actions did not unlock');};
 const locked=async()=>{assert(await page.getByRole('button',{name:/^(测试全部站点|正在测试连接…)$/}).first().isDisabled());for(const b of await page.locator('.idx-actions button').all())assert(await b.isDisabled(),'card operation unlocked during probe/refresh');assert(await page.locator('#managed-site-name').isDisabled());assert(await page.getByRole('button',{name:'保存并启用站点',exact:true}).isDisabled());};
 const done=async total=>{await eventually(()=>all().isEnabled(),'batch remained locked');assert.equal(await summary().getAttribute('aria-busy'),'false');assert.equal(await page.getByRole('progressbar',{name:'本轮检测处理进度'}).getAttribute('value'),String(total));await summary().getByText('已结束 '+total+'/'+total+' 项',{exact:true}).waitFor();};
 await page.route('**/api/v1/indexers/*/test',async route=>{
  active++;maxActive=Math.max(maxActive,active);const ordinal=sent.length;sent.push(new URL(route.request().url()).pathname);
  try{assert.equal(route.request().method(),'POST');assert(!route.request().postData(),'existing probe API must not receive a made-up parent version');const response=await route.fetch({timeout:20000,maxRetries:0});if(response.status()===200){const body=await response.json();assert.equal(body.alive,true,'native fixture must actually succeed');assert(Number.isSafeInteger(body.latency_ms)&&body.latency_ms>=0);successes.push({id:sent[ordinal].split('/').at(-2),latency:body.latency_ms});}if(probeHook)await probeHook({route,response,ordinal});else await route.fulfill({response});}
  catch(e){routeErrors.push(e.message);await route.abort().catch(()=>{});}finally{active--;}
 });
 await page.route('**/api/v1/indexers',async route=>{
  try{const hook=listHook;if(route.request().method()==='GET'&&hook){const response=await route.fetch({timeout:20000,maxRetries:0});await hook(route,response);}else await route.continue();}catch(e){routeErrors.push(e.message);await route.abort().catch(()=>{});}
 });
 async function fresh(label){
  probeHook=null;listHook=null;assert.equal(active,0);
  for(const s of await list()){const r=await mutate('DELETE','/api/v1/indexers/'+s.id+'?expected_version='+s.config_version);assert.equal(r.status(),204);}
  const sites=[];
  for(let i=1;i<=3;i++){const r=await mutate('POST','/api/v1/indexers',{name:'M6 '+label+' '+i,protocol:'torznab',url:rawURL,api_key:'m5-fixture=credential',categories:[2000]});assert.equal(r.status(),201);const s=await r.json();assert.equal(s.config_version,1);await db('freeze',s.id);sites.push(s);}
  await mount();const ordered=[];for(const n of await page.locator('.indexer-row strong').allTextContents()){const s=sites.find(s=>s.name===n);assert(s);ordered.push(s);}assert.equal(ordered.length,3);return ordered;
 }
 async function safe(){const text=await page.locator('body').innerText();const storage=await page.evaluate(()=>JSON.stringify({local:{...localStorage},session:{...sessionStorage}}));for(const s of [rawURL,'m5-secret-path','m5-secret-user','m5-secret-pass','m5-fixture=credential','m5-query-marker']){assert(!text.includes(s),'secret in DOM');assert(!storage.includes(s),'secret in recovery storage');}}
 async function bounds(width){
  await page.setViewportSize({width,height:1000});
  await page.waitForFunction(viewportWidth=>{
   const nav=document.getElementById('app-navigation');
   const menu=document.querySelector('.mobile-menu-btn');
   if(!nav||!menu||innerWidth!==viewportWidth)return false;
   const compact=viewportWidth<=1024;
   if(nav.getAnimations().some(animation=>animation.playState==='running'))return false;
   return compact ? nav.inert&&menu.getAttribute('aria-expanded')==='false'&&nav.getBoundingClientRect().right<=1 : !nav.inert&&nav.getBoundingClientRect().left>=-1;
  },width);
  const failures=await page.locator('.sites-container').evaluate((root)=>{
   const out=[];if(document.documentElement.scrollWidth>innerWidth+1)out.push('viewport overflow');
   const visible=e=>{const r=e.getBoundingClientRect();return r.width>0&&r.height>0;};
   for(const e of root.querySelectorAll('.indexer-row,button,.site-probe-summary,.site-probe-result,[role="status"]')){
    if(!visible(e))continue;const r=e.getBoundingClientRect();if(r.left < -1||r.right>innerWidth+1)out.push(e.tagName+' outside viewport');
    const card=e.closest('.indexer-row');if(card&&e!==card){const c=card.getBoundingClientRect();if(r.left<c.left-1||r.right>c.right+1)out.push('outside card');}
    if(e.tagName==='BUTTON'&&(r.height<44||r.width<44))out.push('control below 44px: '+e.textContent);
    const walker=document.createTreeWalker(e,NodeFilter.SHOW_TEXT);let node;while((node=walker.nextNode())){if(!node.textContent.trim())continue;const range=document.createRange();range.selectNodeContents(node);for(const t of range.getClientRects())if(t.left<r.left-1||t.right>r.right+1||t.top<r.top-1||t.bottom>r.bottom+1)out.push('text clipped: '+e.textContent);}
   }return [...new Set(out)];
  });assert.deepEqual(failures,[],width+' card/control/status bounds');await safe();await page.screenshot({path:shots+'/sites-'+width+'.png',fullPage:true});
 }
 await mkdir(shots,{recursive:true});
 // Forward the second actual POST completely, hold only its response transport.
 let sites=await fresh('stop'),start=sent.length;let post=gate(),refresh=gate();
 probeHook=async({route,response,ordinal})=>{if(ordinal===start+1){assert.equal(response.status(),200);post.mark();await post.wait();}await route.fulfill({response});};
 try{
  await all().click();await post.opened;assert.equal(sent.length,start+2);await result(sites[0].name,'online').waitFor();await result(sites[1].name,'running').waitFor();await result(sites[2].name,'queued').waitFor();await locked();
  const first=successes.find(s=>s.id===sites[0].id);assert.equal(await result(sites[0].name,'online').innerText(),'探针完成：连通正常（'+first.latency+' ms）');await honest(sites[0].id);
  await page.getByRole('button',{name:'停止后续检测',exact:true}).click();await result(sites[2].name,'cancelled').waitFor();await result(sites[1].name,'running').waitFor();await locked();assert.equal(sent.length,start+2);
  listHook=async(route,response)=>{assert.equal(response.status(),200);refresh.mark();await refresh.wait();await route.fulfill({response});};post.release();await refresh.opened;await locked();assert.equal(sent.length,start+2);refresh.release();listHook=null;
  await done(3);assert.equal(sent.length,start+2);assert.equal(await page.locator('.site-probe-result[data-state="online"]').count(),2);assert.equal(await page.locator('.site-probe-result[data-state="cancelled"]').count(),1);await honest(sites[1].id);assert.equal((await db('check',sites[2].id)).queries,0);
  for(const s of sites.slice(0,2))await row(s.name).getByText('最近观测在线',{exact:true}).waitFor();
  for(const width of [1280,768,375,320])await bounds(width);
  // A standalone refresh must also lock the parent create/edit/delete controls.
  refresh=gate();listHook=async(route,response)=>{assert.equal(response.status(),200);refresh.mark();await refresh.wait();await route.fulfill({response});};await page.getByRole('button',{name:'刷新列表',exact:true}).click();await refresh.opened;await locked();refresh.release();listHook=null;await eventually(()=>all().isEnabled(),'standalone refresh did not unlock');assert.equal(sent.length,start+2);
 }finally{post.release();refresh.release();listHook=null;}
 await page.setViewportSize({width:1280,height:1000});
 // Lose a committed response: no retries and no third send, honest persisted health.
 sites=await fresh('lost');start=sent.length;
 probeHook=async({route,response,ordinal})=>{assert.equal(response.status(),200);if(ordinal===start+1)await route.abort('failed');else await route.fulfill({response});};
 await all().click();await result(sites[1].name,'unknown').waitFor();await done(3);assert.equal(sent.length,start+2);await result(sites[0].name,'online').waitFor();await result(sites[2].name,'cancelled').waitFor();await honest(sites[1].id);await page.getByRole('button',{name:'刷新列表',exact:true}).click();await eventually(()=>all().isEnabled(),'manual health refresh');assert.equal(sent.length,start+2);await row(sites[1].name).getByText('最近观测在线',{exact:true}).waitFor();
 // Single button freezes version 1; a real management PUT commits version 2.
 sites=await fresh('version');start=sent.length;post=gate();
 probeHook=async({route,response})=>{assert.equal(response.status(),200);post.mark();await post.wait();await route.fulfill({response});};
 try{
  await row(sites[0].name).getByRole('button',{name:'测试连接',exact:true}).click();await post.opened;await locked();
  const renamed='M6 version changed while actual reply held';const r=await mutate('PUT','/api/v1/indexers/'+sites[0].id,{name:renamed,protocol:'torznab',categories:[2000],expected_version:1});assert.equal(r.status(),200);assert.equal((await r.json()).config_version,2);post.release();await done(1);await row(renamed).waitFor();
  const stale=result(renamed,'stale');await stale.waitFor();assert.match(await stale.innerText(),/配置已变化|不适用/);assert(!/\d+ ms/.test(await stale.innerText()),'old latency labeled as current');assert.equal(sent.length,start+1);await db('check',sites[0].id);
  await page.reload();await eventually(()=>all().isEnabled(),'reload admin');assert.equal(await page.locator('.site-probe-result').count(),0,'ephemeral results survived mount');
 }finally{post.release();}
 // A failed real list refresh halts the queue explicitly.
 sites=await fresh('refresh-failure');start=sent.length;
 probeHook=async({route,response})=>{assert.equal(response.status(),200);listHook=async(r,actual)=>{assert.equal(actual.status(),200);await r.abort('failed');};await route.fulfill({response});};
 await all().click();await page.getByRole('alert').filter({hasText:/站点列表读取失败/}).waitFor();await eventually(async()=>await summary().getAttribute('aria-busy')==='false','refresh failure did not terminate batch');assert.equal(sent.length,start+1);await summary().getByText('已结束 3/3 项',{exact:true}).waitFor();await honest(sites[0].id);listHook=null;
 // Remove the real Cookie between sends; backend 401 must stop the queue.
 sites=await fresh('unauthorized');start=sent.length;post=gate();
 probeHook=async({route,response,ordinal})=>{if(ordinal===start){assert.equal(response.status(),200);post.mark();await post.wait();}else assert.equal(response.status(),401);await route.fulfill({response});};
 try{await all().click();await post.opened;listHook=async(route,response)=>{assert.equal(response.status(),200);await context.clearCookies();listHook=null;await route.fulfill({response});};post.release();await page.waitForURL('**/login');await eventually(()=>active===0,'401 route completion');assert.equal(sent.length,start+2);assert.equal(await page.locator('.site-probe-result').count(),0);}finally{post.release();listHook=null;}
 await context.addCookies([session]);
 // Dispose the old actor scope while a genuine successful response is held.
 sites=await fresh('identity');start=sent.length;post=gate();
 probeHook=async({route,response})=>{assert.equal(response.status(),200);post.mark();await post.wait();await route.fulfill({response});};
 try{
  await all().click();await post.opened;const logout=await context.request.post(base+'/api/v1/auth/logout',{headers:{Origin:base}});assert.equal(logout.status(),204);await page.evaluate(()=>window.__m6SiteProbesAuth.checkStatus());await page.waitForURL('**/login');post.release();await eventually(()=>active===0,'disposed native response drain');assert.equal(sent.length,start+1);assert.equal(await page.locator('.site-probe-result').count(),0);
 }finally{post.release();}
 const login=await context.request.post(base+'/api/v1/auth/login',{headers:{Origin:base},data:{username:'m5-native-admin',password:'OwnedM5NativeFixture2026!'}});assert.equal(login.status(),200);
 // Demote the actual current account between requests: native backend 403.
 sites=await fresh('forbidden');start=sent.length;post=gate();refresh=gate();
 probeHook=async({route,response,ordinal})=>{if(ordinal===start){assert.equal(response.status(),200);post.mark();await post.wait();}else assert.equal(response.status(),403);await route.fulfill({response});};
 try{
  await all().click();await post.opened;listHook=async(route,response)=>{assert.equal(response.status(),200);const demote=await context.request.post(base+'/owned-m6/check?phase=demote');assert.equal(demote.status(),204);listHook=async(r,actual)=>{assert.equal(actual.status(),403);refresh.mark();await refresh.wait();await r.fulfill({response:actual});};await route.fulfill({response});};post.release();await refresh.opened;assert.equal(sent.length,start+2);await result(sites[1].name,'rejected').waitFor();await result(sites[2].name,'cancelled').waitFor();await locked();refresh.release();listHook=null;await eventually(async()=>await summary().getAttribute('aria-busy')==='false','403 queue did not terminate');await summary().getByText('已结束 3/3 项',{exact:true}).waitFor();
  await page.evaluate(()=>window.__m6SiteProbesAuth.checkStatus());await page.getByRole('status').filter({hasText:'当前为成员账户'}).waitFor();assert.equal(await page.locator('.site-probe-result').count(),0,'old role results survived');assert.equal(await all().count(),0);assert.equal(await page.getByRole('button',{name:'测试连接',exact:true}).count(),0);assert.equal(await page.locator('#managed-site-name').count(),0);
  const count=sent.length;await page.getByRole('button',{name:'刷新列表',exact:true}).click();await page.reload();await page.getByRole('status').filter({hasText:'当前为成员账户'}).waitFor();assert.equal(sent.length,count,'member emitted probe network');assert.equal(await page.locator('.site-probe-result').count(),0);
 }finally{post.release();refresh.release();listHook=null;}
 assert.equal(maxActive,1,'more than one concurrent POST');assert.deepEqual(routeErrors,[]);assert.deepEqual(errors,[]);await safe();await context.close();
 console.log('M6_SITE_PROBES_BROWSER_PASS '+JSON.stringify({realApp:true,cookie:true,PG18_6:true,realNativeResponses:successes.length,maxConcurrentPOST:maxActive,stopUnsentOnly:true,refreshFence:true,unknownNoRetry:true,versionSuppression:true,refreshFailureHalt:true,real401:true,real403:true,actorDisposal:true,memberZeroProbes:true,viewportAndCardTextBounds:[1280,768,375,320],controls44:true}));
} finally {await browser.close();assert.equal(browser.isConnected(),false);console.log('M6_SITE_PROBES_CHROME_CLEANUP_VERIFIED');}
`
