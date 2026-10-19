package httpapi_test

import (
	"context"
	"io"
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
)

// These two root cases require Main's ALL_RUNTIME_READY and an owned PG18.6.
func TestM5EpisodeTrackingStatesActualChromeHTTPPG(t *testing.T) {
	m5EpisodeTrackingBrowser(t, "states")
}
func TestM5EpisodeTrackingRecoveryActualChromeHTTPPG(t *testing.T) {
	m5EpisodeTrackingBrowser(t, "recovery")
}

func m5EpisodeTrackingBrowser(t *testing.T, mode string) {
	t.Helper()
	if os.Getenv("PULSE_M5_EPISODE_BROWSER") != "1" {
		t.Skip("use the owned episode tracking runner after ALL_RUNTIME_READY")
	}
	assets, repo := os.Getenv("PULSE_M5_CREATION_ASSETS"), os.Getenv("PULSE_M5_CREATION_REPO")
	if !filepath.IsAbs(repo) || !filepath.IsAbs(assets) || !strings.HasPrefix(assets, "/data/temp/") {
		t.Fatal("owned assets and absolute repository required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f, p := newM5CreationAcceptance(t)
	f.session = m5CreationLogin(t, f, "creation-admin")
	m5CreationBrowserCatalog(t, p)
	created := m5CreationPost(t, f, "episode-browser-create", map[string]any{"tmdb_id": 550, "media_type": "tv", "scope_type": "all"})
	sub := m5CreationID(t, created, "id")
	media := m5CreationID(t, created, "media_id")
	units := m5CreationUnits(t, f, media, []string{"S01E01", "S01E03", "S02E01", "S02E03"})
	// A complete real season response retires an existing unknown-date unit; no
	// invented episode numbers, dates, Wanted rows or execution graph are inserted.
	p.set("/3/tv/550/season/2", `{"season_number":2,"episodes":[{"id":5500201,"season_number":2,"episode_number":1,"air_date":"2099-01-01"}]}`)
	refreshed := f.request(t, "POST", "/api/v1/subscriptions/"+sub.String()+"/metadata/refresh", "episode-browser-retire", map[string]any{"expected_version": m5CreationVersion(t, f, sub), "seasons": []int{2}}, f.session)
	m5IndexerStatus(t, refreshed, 200)
	// Freeze only scheduling time, keeping the running App and real read model.
	m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day' WHERE subscription_id=$1`, sub)
	target, _ := url.Parse(f.base)
	proxy := &httputil.ReverseProxy{FlushInterval: -1}
	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = target.Host
		if pr.In.Header.Get("Origin") == "http://"+pr.In.Host {
			pr.Out.Header.Set("Origin", f.base)
		}
	}
	files := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/owned-m5-episode/evidence" {
			var data string
			err := f.app.DBPool().QueryRow(r.Context(), `SELECT jsonb_build_object(
    'observed_at',clock_timestamp(),
    'version',s.intent_version,'state',s.state,'scope',s.scope_type,
    'selected',s.selected_unit_ids,'seasons',s.seasons,'specials',s.include_specials,
    'audit',(SELECT count(*) FROM audit_log WHERE target_id=s.id::text AND action LIKE 'subscription.control_%'),
    'tasks',(SELECT count(*) FROM download_task),
    'jobs',(SELECT count(*) FROM job WHERE kind LIKE '%submission%'),
    'units',(SELECT jsonb_agg(jsonb_build_object('key',u.unit_key,'id',u.id,'date',u.air_date,'retired',u.retired,'user',wi.user_skipped,'excluded',wi.scope_excluded,'skipped',wi.is_skipped,'intent',wi.intent_version,'due',wi.next_evaluate_at) ORDER BY u.unit_key) FROM wanted_item wi JOIN media_unit u ON u.id=wi.unit_id WHERE wi.subscription_id=s.id))::text FROM subscription s WHERE s.id=$1`, sub).Scan(&data)
			if err != nil {
				t.Error(err)
				http.Error(w, "owned evidence failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, data)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(bridge.Close)
	scratch := t.TempDir()
	script := filepath.Join(scratch, "episode-browser.mjs")
	if err := os.WriteFile(script, []byte(m5EpisodeTrackingChromeProgram), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 210*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M5_EPISODE_MODE="+mode, "PULSE_M5_EPISODE_BASE="+bridge.URL, "PULSE_M5_EPISODE_SESSION="+f.session, "PULSE_M5_EPISODE_SUB="+sub.String(), "PULSE_M5_EPISODE_SCRATCH="+scratch, "PULSE_M5_EPISODE_UNIT="+units["S01E01"].String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual episode Chrome %s: %v\n%s", mode, err, out)
	}
	witness := "M5_EPISODE_" + strings.ToUpper(mode) + "_PASS"
	if !strings.Contains(string(out), witness) {
		t.Fatal("missing actual browser receipt")
	}
	if p.pathCount("/3/tv/550/season/1") == 0 || p.pathCount("/3/tv/550/season/2") < 2 {
		t.Fatal("native sparse creation and retirement did not run")
	}
	if n := m5InteractiveInt(t, f, `SELECT count(*) FROM download_task`); n != 0 {
		t.Fatal("episode command created download task", n)
	}
	t.Log(strings.TrimSpace(string(out)))
}

const m5EpisodeTrackingChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {readFile,mkdir} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M5_CREATION_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M5_EPISODE_BASE,sub=process.env.PULSE_M5_EPISODE_SUB,mode=process.env.PULSE_M5_EPISODE_MODE;
const detail='/api/v1/subscriptions/'+sub,control=detail+'/control',profile=process.env.PULSE_M5_EPISODE_SCRATCH+'/chrome';
const context=await chromium.launchPersistentContext(profile,{executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,viewport:{width:1440,height:1000},args:['--remote-debugging-port=0','--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-component-update','--no-first-run']});
let releaseRead;
try {
 const port=Number((await readFile(profile+'/DevToolsActivePort','utf8')).split('\n')[0]);assert(port>0&&port!==9222);
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_EPISODE_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 await context.route('**/*',r=>new URL(r.request().url()).origin===base?r.continue():r.abort('blockedbyclient'));
 const page=await context.newPage();page.setDefaultTimeout(15000);
 const capture=async name=>{const root=process.env.PULSE_M5_EPISODE_ARTIFACT_DIR;if(!root)return;if(!root.startsWith('/data/temp/'))throw new Error('owned screenshot path required');await mkdir(root,{recursive:true});await page.screenshot({path:root+'/'+name+'.png'});};
 const errors=[],writes=[];page.on('pageerror',e=>errors.push(e.message));
 page.on('request',r=>{if(r.method()!=='GET'&&new URL(r.url()).pathname.startsWith('/api/'))writes.push({path:new URL(r.url()).pathname,method:r.method(),key:r.headers()['idempotency-key'],raw:r.postData(),body:r.postDataJSON()});});
 const button=(name,scope=page)=>scope.getByRole('button',{name,exact:true});
 const group=key=>page.getByRole('group',{name:key+' 追更状态',exact:true});
 const feedback=page.getByRole('region',{name:'逐集追更操作',exact:true});
 const evidence=async()=>{const r=await context.request.get(base+'/owned-m5-episode/evidence');assert.equal(r.status(),200,await r.text());return r.json();};
 const row=(e,key)=>e.units.find(u=>u.key===key);
 const open=async()=>{await page.goto(base+'/subscriptions?subscription='+sub);await group('S01E01').waitFor();await page.getByRole('region',{name:'当前目标状态',exact:true}).getByText(/观测时间：/).waitFor();};
 const ready=async(name,key='S01E01')=>{await button(name,group(key)).waitFor();await page.waitForFunction(({key,name})=>{const g=[...document.querySelectorAll('[role="group"]')].find(e=>e.getAttribute('aria-label')===key+' 追更状态');return [...(g?.querySelectorAll('button')||[])].some(b=>b.textContent===name&&!b.disabled);},{key,name});};
 const confirmed=async()=>{await feedback.getByRole('status').filter({hasText:'操作已确认，最新状态已更新。'}).waitFor();await ready('跳过本集').catch(async()=>await ready('恢复追更'));};
 const snapshot=e=>({state:e.state,scope:e.scope,selected:e.selected,seasons:e.seasons,specials:e.specials,other:e.units.filter(u=>u.key!=='S01E01').map(({key,user,excluded,skipped})=>({key,user,excluded,skipped}))});
 const direct=async body=>{const r=await context.request.put(base+control,{headers:{Origin:base,'Idempotency-Key':crypto.randomUUID()},data:body});assert.equal(r.status(),200,await r.text());return r.json();};
 const locked=async()=>{for(const b of await page.locator('[role="group"][aria-label$="追更状态"] button').all())assert(await b.isDisabled(),'episode write enabled while unresolved');for(const name of ['暂停订阅','归档订阅','重新检查']){const b=button(name);if(await b.count())assert(await b.isDisabled(),name+' enabled while unresolved');}};
 const journal=async()=>{const entries=await page.evaluate(()=>Object.entries(sessionStorage).filter(([k])=>k.startsWith('pulse:episode-control:v1:')));assert.equal(entries.length,1);return {storage:entries[0][0],raw:entries[0][1],record:JSON.parse(entries[0][1])};};
 await open();const initial=await evidence();assert.deepEqual(initial.units.map(u=>u.key),['S01E01','S01E03','S02E01','S02E03']);assert.equal(row(initial,'S01E03').date,null);assert.equal(row(initial,'S02E03').retired,true);
 if(mode==='states'){
  await group('S01E03').getByText('播出日期：未知',{exact:true}).waitFor();assert.equal(await group('S01E02').count(),0);
  await page.locator('.season-pill-btn').filter({hasText:'第 2 季'}).click();await group('S02E01').getByText('尚未到播出日期',{exact:true}).first().waitFor();await group('S02E03').getByText('已退休',{exact:true}).waitFor();
  assert(await button('跳过本集',group('S02E03')).isDisabled());assert(await button('重新检查本集',group('S02E03')).isDisabled());
  await group('S02E01').scrollIntoViewIfNeeded();await capture('desktop');
  await page.getByRole('checkbox',{name:'仅看未满足目标',exact:true}).check();await group('S02E03').waitFor({state:'hidden'});await group('S02E01').waitFor();await page.getByRole('checkbox',{name:'仅看未满足目标',exact:true}).uncheck();
  await page.locator('.season-pill-btn').filter({hasText:'第 1 季'}).click();await ready('跳过本集');
  // Preserve another unit's explicit user skip through both single-unit edits.
  await direct({action:'skip',unit_id:row(initial,'S01E03').id,skipped:true,expected_version:initial.version});await open();const before=await evidence();
  await button('跳过本集',group('S01E01')).click();await confirmed();let after=await evidence();assert.equal(after.version,before.version+1);assert.equal(row(after,'S01E01').user,true);assert.deepEqual(snapshot(after),snapshot(before));
  await ready('恢复追更');await button('恢复追更',group('S01E01')).click();await confirmed();after=await evidence();assert.equal(after.version,before.version+2);assert.equal(row(after,'S01E01').user,false);assert.deepEqual(snapshot(after),snapshot(before));
  await ready('重新检查本集');const prior=await evidence();await button('重新检查本集',group('S01E01')).click();await confirmed();after=await evidence();assert.equal(after.version,prior.version);assert.deepEqual(snapshot(after),snapshot(prior));assert.equal(after.audit,prior.audit+1);assert(new Date(row(after,'S01E01').due)>=new Date(prior.observed_at)&&new Date(row(after,'S01E01').due)<=new Date(after.observed_at), 'selected deadline must be within actual database observation window');for(const u of prior.units.filter(u=>u.key!=='S01E01'))assert.equal(row(after,u.key).due,u.due);
  const command=writes.at(-1);assert.equal(command.method,'PUT');assert.deepEqual(command.body,{expected_version:prior.version,unit_id:row(prior,'S01E01').id,action:'recheck'});
  await direct({action:'scope',scope_type:'selected',seasons:[],include_specials:false,selected_unit_ids:[row(after,'S01E01').id],expected_version:after.version});await open();await page.locator('.season-pill-btn').filter({hasText:'第 2 季'}).click();await group('S02E01').getByText('范围外',{exact:true}).waitFor();assert(await button('跳过本集',group('S02E01')).isDisabled());assert(await button('重新检查本集',group('S02E01')).isDisabled());
  await page.setViewportSize({width:375,height:900});await group('S02E01').scrollIntoViewIfNeeded();assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'375px document overflow');assert(await page.getByRole('dialog').evaluate(e=>e.scrollWidth<=e.clientWidth+1),'375px dialog overflow');
  await capture('mobile');
  after=await evidence();await direct({action:'state',state:'paused',expected_version:after.version});await open();assert(await button('重新检查本集',group('S01E01')).isDisabled());assert(await button('跳过本集',group('S01E01')).isDisabled(),'paused episode skip must be disabled');
 } else {
  let drop=true,lostReceipt,replayReceipt;let hold=false,fail=false,entered;let readGate;
  const fault=async route=>{if(drop){drop=false;const r=await route.fetch();assert.equal(r.status(),200,await r.text());lostReceipt=await r.text();await route.abort('failed');return;}const r=await route.fetch();replayReceipt=await r.text();await route.fulfill({response:r});};
  await page.route('**'+control,fault);await ready('跳过本集');await button('跳过本集',group('S01E01')).click();await button('重试同一逐集命令',feedback).waitFor();await page.waitForFunction(()=>[...document.querySelectorAll('button')].some(b=>b.textContent==='重试同一逐集命令'&&!b.disabled));
  const saved=await journal();assert.equal(saved.record.key,writes.at(-1).key);assert.deepEqual(saved.record.body,writes.at(-1).body);const original=writes.at(-1),committed=await evidence();assert.equal(committed.version,initial.version+1);await locked();
  await button('刷新逐集状态',feedback).click();await feedback.getByRole('status').filter({hasText:'原请求尚未确认'}).waitFor();assert.equal((await journal()).raw,saved.raw);await page.reload();await group('S01E01').waitFor();assert.equal((await journal()).raw,saved.raw);assert.equal(writes.length,1,'reload must never resend');await locked();
  const readFault=async route=>{if(hold){entered?.();await readGate;}if(fail){await route.abort('failed');return;}await route.continue();};
  await page.route('**'+detail,readFault);hold=true;fail=true;readGate=new Promise(r=>releaseRead=r);const waiting=new Promise(r=>entered=r);
  await button('重试同一逐集命令',feedback).click();await waiting;assert.equal(writes.at(-1).key,original.key);assert.equal(writes.at(-1).raw,original.raw);assert.equal(replayReceipt,lostReceipt,'stable original receipt bytes');await locked();releaseRead();hold=false;
  await feedback.getByRole('alert').filter({hasText:'订阅详情读取失败'}).waitFor();await locked();assert.equal(await button('重试同一逐集命令',feedback).count(),0);assert.equal(await page.evaluate(key=>sessionStorage.getItem(key),saved.storage),null);
  let after=await evidence();assert.equal(after.version,committed.version);assert.equal(after.audit,committed.audit);fail=false;await button('刷新逐集状态',feedback).click();await confirmed();await ready('恢复追更');
  await page.unroute('**'+control,fault);await page.unroute('**'+detail,readFault);
  // Real concurrent control advances the version after the UI's GET snapshot.
  await direct({action:'skip',unit_id:row(after,'S01E03').id,skipped:true,expected_version:after.version});
  const conflict=page.waitForResponse(r=>new URL(r.url()).pathname===control&&r.status()===409);await button('恢复追更',group('S01E01')).click();await conflict;await feedback.getByRole('alert').filter({hasText:'原请求已保留'}).waitFor();const stale=await journal();assert.equal(stale.record.body.expected_version,after.version);assert.equal(stale.record.body.skipped,false);await locked();assert(await button('重试同一逐集命令',feedback).isDisabled());
  await button('刷新逐集状态',feedback).click();await feedback.getByRole('status').filter({hasText:'原请求尚未确认'}).waitFor();assert.equal((await journal()).raw,stale.raw);await locked();after=await evidence();assert.equal(row(after,'S01E01').user,true,'409 must not overwrite');assert.equal(after.version,committed.version+1);
  const countBeforeResolution=writes.length;await button('重新读取并结束冲突命令',feedback).click();await feedback.getByRole('status').filter({hasText:'冲突命令已结束'}).waitFor();await ready('恢复追更');assert.equal(writes.length,countBeforeResolution,'ending conflict must not resend');assert.equal(await page.evaluate(()=>Object.keys(sessionStorage).filter(k=>k.startsWith('pulse:episode-control:v1:')).length),0);const resolved=await evidence();assert.equal(resolved.version,after.version);assert.equal(resolved.audit,after.audit);assert.equal(row(resolved,'S01E01').user,true);
 }
 const end=await evidence();assert.equal(end.tasks,0);assert.equal(end.jobs,0);assert(writes.every(r=>r.path===control&&r.method==='PUT'),'no direct Add, acquisition or new task request');assert.deepEqual(errors,[]);
 console.log('M5_EPISODE_'+mode.toUpperCase()+'_PASS '+JSON.stringify({actualChrome:true,realCookieAppPG18_6:true,nativeSparseTMDB:true,ephemeralCDP:port,uiWrites:writes.length,version:end.version,tasks:end.tasks,submissionJobs:end.jobs}));
} catch(error) {console.error('M5_EPISODE_DOM '+(await context.pages().at(-1)?.locator('body').innerText())?.slice(-12000));throw error;}
finally {releaseRead?.();await context.close();console.log('M5_EPISODE_CHROME_CLEANUP_VERIFIED');}
`
