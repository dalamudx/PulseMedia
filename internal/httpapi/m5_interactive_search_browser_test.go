package httpapi_test

import (
	"context"
	"encoding/json"
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

	"github.com/google/uuid"
)

// Run through scripts/verify_m5_interactive_search.sh. All APIs are the real App;
// the owned bridge serves built assets and coordinates local protocol/PG evidence.
func TestM5InteractiveSearchActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M5_INTERACTIVE_BROWSER") != "1" {
		t.Skip("explicit private Chrome fixture required")
	}
	assets, repo := os.Getenv("PULSE_M5_INDEXER_ASSETS"), os.Getenv("PULSE_M5_INDEXER_REPO")
	if !filepath.IsAbs(repo) || !strings.HasPrefix(assets, "/data/temp/") {
		t.Fatal("owned assets/repository required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f := newM5IndexerAcceptance(t)
	slow := m5InteractiveNative(t, f, "browser-slow", 2, true, nil)
	fast := m5InteractiveNative(t, f, "browser-fast", 3, false, slow.entered)
	late := m5InteractiveNative(t, f, "browser-cancelled", 2, true, nil)
	m5InteractiveSQL(t, f, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, uuid.MustParse(late.id))
	media := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO media(id,title,media_type,year) VALUES($1,'Interactive Movie','movie',2026)`, media)
	m5InteractiveSQL(t, f, `INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,'tmdb.movie','550')`, uuid.New(), media)
	other := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'browser-other',password_hash,'admin',true FROM app_user WHERE username='m5-native-admin'`, other)
	otherSession := m5InteractiveLogin(t, f, "browser-other")
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/owned-m5/control" {
			switch r.URL.Query().Get("phase") {
			case "progress":
				if !slow.blocked.Load() || slow.cancelled.Load() {
					http.Error(w, "slow source no longer held", http.StatusConflict)
					return
				}
				slow.unblock()
			case "cancel-setup":
				_, err := f.app.DBPool().Exec(r.Context(), `UPDATE indexer_lifecycle SET enabled=(indexer_id=$1)`, uuid.MustParse(late.id))
				if err != nil {
					http.Error(w, "fixture update failed", http.StatusInternalServerError)
					return
				}
			case "cancel-entered":
				if !late.blocked.Load() {
					http.Error(w, "cancel source not entered", http.StatusConflict)
					return
				}
			case "cancel-release":
				late.unblock()
			case "counts":
				id, err := uuid.Parse(r.URL.Query().Get("id"))
				if err != nil {
					http.Error(w, "invalid ID", http.StatusBadRequest)
					return
				}
				var searches, sources, jobs int
				err = f.app.DBPool().QueryRow(r.Context(), `SELECT (SELECT count(*) FROM interactive_search WHERE id=$1),(SELECT count(*) FROM interactive_search_source WHERE search_id=$1),(SELECT count(*) FROM job WHERE kind='interactive_search_source' AND payload->>'search_id'=$1::text)`, id).Scan(&searches, &sources, &jobs)
				if err != nil {
					http.Error(w, "fixture read failed", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]int{"searches": searches, "sources": sources, "jobs": jobs})
				return
			default:
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(204)
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
	script := filepath.Join(scratch, "interactive-browser.mjs")
	if err := os.WriteFile(script, []byte(m5InteractiveChromeProgram), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M5_BROWSER_BASE="+server.URL, "PULSE_M5_BROWSER_SESSION="+f.session, "PULSE_M5_BROWSER_OTHER="+otherSession, "PULSE_M5_BROWSER_SCRATCH="+scratch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual Chrome acceptance: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "M5_INTERACTIVE_ACTUAL_CHROME_PASS") {
		t.Fatal("missing browser receipt")
	}
	m5InteractiveQuiesce(t, f)
	if late.calls.Load() != 1 {
		t.Errorf("cancel native calls=%d, want 1", late.calls.Load())
	}
	if m5InteractiveInt(t, f, `SELECT count(*) FROM release_candidate WHERE indexer_id=$1`, uuid.MustParse(late.id)) != 0 {
		t.Error("late candidate persisted after cancel")
	}
	if fast.calls.Load() < 1 {
		t.Error("native fast source never called")
	}
	t.Log(strings.TrimSpace(string(out)))
}

const m5InteractiveChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {stat} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M5_INDEXER_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M5_BROWSER_BASE;
const browser=await chromium.launch({executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,args:['--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-component-update','--no-first-run']});
try {
 const context=await browser.newContext({viewport:{width:1440,height:1000}});
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_BROWSER_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 await context.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort('blockedbyclient'));
 const page=await context.newPage();page.setDefaultTimeout(15000);
 const errors=[],posts=[],legacy=[],lookups=[];
 page.on('pageerror',e=>errors.push(e.message));
 page.on('request',r=>{const u=new URL(r.url());if(u.pathname==='/api/v1/release-searches'&&r.method()==='POST')posts.push({key:r.headers()['idempotency-key'],body:r.postDataJSON()});if(u.pathname==='/api/v1/releases/search')legacy.push(u.pathname);if(u.pathname.startsWith('/api/v1/release-search-commands/'))lookups.push(u.pathname+u.search);});
 const button=name=>page.getByRole('button',{name,exact:true});
 const actions=()=>button('选择获取目标');
 const control=async(phase,id='')=>{const r=await context.request.get(base+'/owned-m5/control?phase='+phase+'&id='+id);assert(r.ok(),'owned checkpoint '+phase+' status '+r.status());return r;};
 const storage=async()=>{const s=await page.evaluate(()=>JSON.stringify({session:{...sessionStorage},local:{...localStorage}}));for(const secret of ['Interactive Movie','m5-secret','m5-download-only-secret','torznab','apikey','passkey'])assert(!s.includes(secret),'query/secret in storage');return s;};
 async function openSearch(){await button('关闭资源检索').click();await page.locator('.media-card-wrap').hover();await page.getByTitle('检索站点种子',{exact:true}).click();}
 await page.goto(base+'/discover');
 const query=page.getByPlaceholder('输入电影或电视剧名称 (例如 绝命毒师, Breaking Bad, 权力的游戏, 我不是药神)...');
 await query.fill('Interactive Movie');await query.press('Enter');
 await page.locator('.media-card-wrap').waitFor();
 await page.locator('.media-card-wrap').hover();
 const created=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/release-searches'&&r.request().method()==='POST');
 await page.getByTitle('检索站点种子',{exact:true}).click();
 const initial=await created;assert.equal(initial.status(),201);const receipt=await initial.json();assert(posts[0].key);assert.deepEqual(posts[0].body,{query:'Interactive Movie'});
 // A 3-row first batch fits in the native 2-second per-page budget.
 await page.waitForFunction(()=>document.querySelectorAll('.release-item').length===3);
 await page.getByText('首批资源已返回，其余站点仍在检索…',{exact:true}).waitFor();
 await page.getByText(/browser-slow：正在检索/).waitFor();
 await control('progress');
 await page.waitForFunction(()=>document.querySelectorAll('.release-item').length===5);
 await page.getByText('检索已完成。',{exact:true}).waitFor();
 const snapshot=await (await context.request.get(base+'/api/v1/release-searches/'+receipt.id)).json();
 assert.equal(snapshot.sort_order,'quality_desc_seeders_desc_id_asc');assert.equal(snapshot.statistics_mode,'latest_persisted');
 for(let i=1;i<snapshot.results.length;i++){const a=snapshot.results[i-1],b=snapshot.results[i];assert(a.score>b.score||(a.score===b.score&&(a.seeders>b.seeders||(a.seeders===b.seeders&&a.id<b.id))));}
 assert.notEqual(snapshot.results[0].seeders,9999);
 assert.deepEqual(await page.locator('.rel-title').allTextContents(),snapshot.results.map(r=>r.title));
 await page.getByText(/按品质、做种数及资源编号排序 · 品质策略 default 版本/).waitFor();
 assert(await actions().first().isEnabled());await storage();
 // Real DOM entry into the existing controlled movie panel; no generic download.
 await actions().first().click();
 const panel=page.getByRole('region',{name:'显式电影获取目标'});
 await panel.waitFor();await page.getByLabel('电影目标根',{exact:true}).waitFor();await page.getByLabel('获取下载器',{exact:true}).waitFor();await page.getByLabel('获取质量策略',{exact:true}).waitFor();
 await button('预览电影获取').waitFor();assert(await button('确认创建持久获取').isDisabled());
 await button('预览电影获取').click();await page.getByRole('alert').filter({hasText:'请明确选择电影目标根和下载器。'}).waitFor();
 await page.setViewportSize({width:375,height:812});
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'375px horizontal overflow');
 await button('刷新搜索观察').scrollIntoViewIfNeeded();assert(await button('刷新搜索观察').isEnabled());
 const shot=process.env.PULSE_M5_BROWSER_SCRATCH+'/interactive-375.png';await page.screenshot({path:shot});assert((await stat(shot)).size>1000);
 await page.setViewportSize({width:1440,height:1000});
 // Read transport failure preserves rows and closes acquisition until explicit retry.
 let readFailure=503;
 const readFault=async route=>{if(route.request().method()==='GET'&&readFailure)await route.fulfill({status:readFailure,contentType:'application/problem+json',body:JSON.stringify({code:'OWNED_READ_FAILURE',message:'read unavailable'})});else await route.continue();};
 await page.route('**/api/v1/release-searches/*',readFault);
 await button('刷新搜索观察').click();await page.getByRole('alert').filter({hasText:'当前观察读取失败'}).waitFor();assert.equal(await actions().count(),5);assert(await actions().first().isDisabled());
 readFailure=0;await button('刷新搜索观察').click();await page.waitForFunction(()=>!document.querySelector('.release-item button')?.disabled);await page.unroute('**/api/v1/release-searches/*',readFault);
 // Foreign administrator Cookie has no access to actor-owned receipts/snapshots.
 const foreign=await browser.newContext();await foreign.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_BROWSER_OTHER,url:base,httpOnly:true,sameSite:'Strict'}]);
 for(const path of ['/api/v1/release-searches/'+receipt.id,'/api/v1/release-search-commands/'+posts[0].key+'?action=create'])assert([403,404].includes((await foreign.request.get(base+path)).status()));
 const forbiddenCancel=await foreign.request.post(base+'/api/v1/release-searches/'+receipt.id+'/cancel',{headers:{Origin:base,'Idempotency-Key':'foreign-browser-cancel'}});assert([403,404].includes(forbiddenCancel.status()));await foreign.close();
 // Lose only the response after real native App/PG commit, then reload.
 let lost;
 const drop=async route=>{if(route.request().method()!=='POST'){await route.continue();return;}const r=await route.fetch();assert.equal(r.status(),201);lost=await r.json();await route.abort('failed');};
 await page.route('**/api/v1/release-searches',drop);const before=posts.length;await openSearch();
 await page.getByText('原搜索命令待确认；恢复只读取原回执，不会重新访问站点。',{exact:true}).first().waitFor();assert(lost);assert.equal(posts.length,before+1);const originalKey=posts.at(-1).key;await storage();
 const counts=await (await control('counts',lost.id)).json();assert.deepEqual(counts,{searches:1,sources:2,jobs:2});
 await page.unroute('**/api/v1/release-searches',drop);
 const recovered=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/release-search-commands/'+originalKey&&r.status()===200);await page.reload();assert.deepEqual(await (await recovered).json(),lost);
 await button('恢复资源搜索观察').click();await page.locator('.release-item').first().waitFor();assert.equal(posts.length,before+1);assert(await actions().first().isDisabled());await page.getByText('恢复的搜索可继续观察。请选择原电影重新发起搜索后，再确认获取目标。',{exact:true}).waitFor();await storage();assert.deepEqual(await (await control('counts',lost.id)).json(),counts);
 // 403 and 401 both erase visible candidate state. Each reload restores the
 // original real receipt before applying the next observation fault.
 for(const status of [403,401]){
  readFailure=status;await page.route('**/api/v1/release-searches/*',readFault);await button('刷新搜索观察').click();if(status===401)await page.getByText('会话已失效，请重新登录。',{exact:true}).waitFor();else await page.getByRole('alert').filter({hasText:'授权已失效'}).waitFor();assert.equal(await page.locator('.release-item').count(),0);
  await page.unroute('**/api/v1/release-searches/*',readFault);readFailure=0;await page.goto(base+'/discover');await button('恢复资源搜索观察').click();await page.locator('.release-item').first().waitFor();
 }
 // A fresh title command starts the one held native source; cancel before its
 // response is released and verify both the DOM and PG reject late candidates.
 await control('cancel-setup');
 await button('关闭资源检索').click();await query.fill('Interactive Movie');await query.press('Enter');await page.locator('.media-card-wrap').waitFor();await page.locator('.media-card-wrap').hover();
 const cancellingCreate=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/release-searches'&&r.request().method()==='POST');await page.getByTitle('检索站点种子',{exact:true}).click();const cancelledID=(await (await cancellingCreate).json()).id;
 await page.getByText(/browser-cancelled：正在检索/).waitFor();await control('cancel-entered');
 const cancelResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/release-searches/'+cancelledID+'/cancel'&&r.status()===200);await button('取消原搜索需求').click();await cancelResponse;await control('cancel-release');
 await page.getByText('搜索需求已取消；已开始的站点请求可能仍在结束中。',{exact:true}).first().waitFor();await button('刷新搜索观察').click();assert.equal(await page.locator('.release-item').count(),0);
 const final=await (await context.request.get(base+'/api/v1/release-searches/'+cancelledID)).json();assert.equal(final.state,'cancelled');assert.deepEqual(final.results,[]);
 assert.equal(legacy.length,0);assert(lookups.some(x=>x.includes(originalKey)));assert.deepEqual(errors,[]);
 console.log('M5_INTERACTIVE_ACTUAL_CHROME_PASS '+JSON.stringify({actualChrome:true,realApp:true,realPG:true,nativeTorznab:true,explicitStableKey:true,legacyGETZero:true,fastDOMBeforeHeldSlow:true,policyQualityOrdering:true,mobile375:true,screenshotCapturedAndDisposable:true,readFailureRetainsAndDisables:true,refreshRestores:true,lostPOSTReloadSameReceipt:true,noPOSTReplay:true,oneDBSearchSourceSet:true,noQueryOrSecretStorage:true,auth401403Clear:true,foreignOwnerDenied:true,cancelNoLateCandidate:true,movieAcquisitionPanel:true,downloadCommandAccepted:false,acquisitionGap:'No configured movie root/downloader or metadata details provider in this isolated search fixture; public acquisition not submitted.'}));
 await context.close();
} finally {await browser.close();}
`
