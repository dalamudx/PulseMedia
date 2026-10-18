package httpapi_test

import (
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
	"testing"
	"time"
)

// The browser owns no catalog or business facts: only native TMDB HTTP responses
// and a static asset bridge. Every creation goes through the real App and Cookie.
func TestM5SubscriptionCreationActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M5_SUBSCRIPTION_CREATION_BROWSER") != "1" || os.Getenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE") != "1" {
		t.Skip("explicit private PostgreSQL and Chrome opt-ins required")
	}
	assets, repo, shots := os.Getenv("PULSE_M5_CREATION_ASSETS"), os.Getenv("PULSE_M5_CREATION_REPO"), os.Getenv("PULSE_M5_CREATION_SCREENSHOTS")
	for _, path := range []string{assets, shots} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(path, "/data/temp/") {
			t.Fatal("task-owned assets/screenshots required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	f, provider := newM5CreationAcceptance(t)
	f.session = m5CreationLogin(t, f, "creation-admin")
	m5CreationBrowserCatalog(t, provider)
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
	files := http.FileServer(http.Dir(assets))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/owned-m5-creation/evidence" {
			// Read-only observations after actual UI commands, never success-by-fixture.
			var graph string
			err := f.app.DBPool().QueryRow(r.Context(), `SELECT jsonb_build_object(
    'media',(SELECT count(*) FROM media),
    'subscriptions',(SELECT count(*) FROM subscription),
    'wanted',(SELECT count(*) FROM wanted_item),
    'receipts',(SELECT count(*) FROM idempotency_record WHERE idempotency_key='subscription.creation:' || $1),
    'identities',(SELECT coalesce(jsonb_agg(jsonb_build_object('namespace',namespace,'external_id',external_id,'media_id',media_id) ORDER BY namespace,external_id),'[]') FROM media_identity),
    'units',(SELECT coalesce(jsonb_agg(jsonb_build_object('key',u.unit_key,'media_id',u.media_id,'air_date',u.air_date,'episode_id',i.external_id,'series_id',i.tmdb_series_id) ORDER BY u.media_id,u.unit_key),'[]') FROM media_unit u LEFT JOIN media_unit_identity i ON i.unit_id=u.id),
    'wanted_graph',(SELECT coalesce(jsonb_agg(jsonb_build_object('sub',w.subscription_id,'key',u.unit_key,'skipped',w.is_skipped,'excluded',w.scope_excluded) ORDER BY w.subscription_id,u.unit_key),'[]') FROM wanted_item w JOIN media_unit u ON u.id=w.unit_id))::text`, r.URL.Query().Get("key")).Scan(&graph)
			if err != nil {
				t.Error(err)
				http.Error(w, "owned read-only evidence failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			provider.mu.Lock()
			paths := make(map[string]int, len(provider.calls))
			for path, count := range provider.calls {
				paths[path] = count
			}
			provider.mu.Unlock()
			if err := json.NewEncoder(w).Encode(map[string]any{"provider_calls": provider.count(), "provider_paths": paths, "graph": json.RawMessage(graph)}); err != nil {
				t.Error(err)
			}
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
	script := filepath.Join(scratch, "creation-browser.mjs")
	if err := os.WriteFile(script, []byte(m5CreationChromeProgram), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M5_CREATION_BASE="+bridge.URL, "PULSE_M5_CREATION_SESSION="+f.session, "PULSE_M5_CREATION_SCRATCH="+scratch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual creation Chrome: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "M5_SUBSCRIPTION_CREATION_ACTUAL_CHROME_PASS") {
		t.Fatal("missing actual Chrome receipt")
	}
	for _, path := range []string{"/3/search/movie", "/3/search/tv", "/3/movie/550", "/3/tv/550", "/3/tv/550/season/1", "/3/tv/551/season/1", "/3/trending/all/day"} {
		if provider.pathCount(path) == 0 {
			t.Errorf("native provider route never executed: %s", path)
		}
	}
	t.Log(strings.TrimSpace(string(output)))
}

func m5CreationBrowserCatalog(t *testing.T, p *m5CreationProtocol) {
	t.Helper()
	movies := []map[string]any{}
	for _, item := range []struct {
		id    int
		title string
	}{{550, "Native trusted movie"}, {551, "Native recovery movie"}, {552, "Native retry movie"}} {
		details := map[string]any{"id": item.id, "media_type": "movie", "title": item.title, "original_title": item.title, "release_date": "1999-10-15", "overview": "Complete owned native metadata", "native_fixture_padding": strings.Repeat("x", 3000), "vote_average": 8.1, "genres": []map[string]string{{"name": "剧情"}}}
		movies = append(movies, details)
		p.set(fmt.Sprintf("/3/movie/%d", item.id), m5CreationJSON(details))
	}
	tvs := []map[string]any{}
	for _, id := range []int{550, 551} {
		m5CreationTV(p, id, false, false, false)
		p.set(fmt.Sprintf("/3/tv/%d/season/1", id), m5CreationJSON(map[string]any{"season_number": 1, "episodes": []map[string]any{{"id": id*10000 + 101, "season_number": 1, "episode_number": 1, "air_date": "2020-01-01"}, {"id": id*10000 + 103, "season_number": 1, "episode_number": 3, "air_date": nil}}}))
		title := "Native sparse television"
		if id == 551 {
			title = "Native selected television"
		}
		details := map[string]any{"id": id, "media_type": "tv", "name": title, "original_name": title, "first_air_date": "2020-01-01", "overview": "Complete owned sparse season metadata", "native_fixture_padding": strings.Repeat("x", 4200), "number_of_seasons": 3, "number_of_episodes": 4, "seasons": []map[string]any{{"season_number": 1, "episode_count": 2, "name": "第 1 季"}, {"season_number": 2, "episode_count": 2, "name": "第 2 季"}, {"season_number": 3, "episode_count": 0, "name": "第 3 季"}}}
		p.set(fmt.Sprintf("/3/tv/%d", id), m5CreationJSON(details))
		tvs = append(tvs, details)
	}
	movieSearch, tvSearch := m5CreationJSON(map[string]any{"page": 1, "total_pages": 1, "total_results": len(movies), "results": movies}), m5CreationJSON(map[string]any{"page": 1, "total_pages": 1, "total_results": len(tvs), "results": tvs})
	if len(movieSearch) < 8192 || len(tvSearch) < 8192 {
		t.Fatal("native search fixture must exercise >8KiB JSON responses")
	}
	p.set("/3/search/movie", movieSearch)
	p.set("/3/search/tv", tvSearch)
	all := append(append([]map[string]any{}, movies...), tvs...)
	feed := m5CreationJSON(map[string]any{"page": 1, "total_pages": 1, "results": all})
	for _, path := range []string{"/3/trending/all/day", "/3/search/multi", "/3/movie/popular", "/3/tv/popular", "/3/movie/top_rated"} {
		p.set(path, feed)
	}
}

const m5CreationChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {readFile,stat} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M5_CREATION_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M5_CREATION_BASE, shots=process.env.PULSE_M5_CREATION_SCREENSHOTS;
const profile=process.env.PULSE_M5_CREATION_SCRATCH+'/chrome-profile';
const context=await chromium.launchPersistentContext(profile,{executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,viewport:{width:1440,height:1000},args:['--remote-debugging-port=0','--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-component-update','--no-first-run']});
try {
 const cdpPort=Number((await readFile(profile+'/DevToolsActivePort','utf8')).split('\n')[0]);
 assert(cdpPort>0&&cdpPort!==9222,'Chrome must own an ephemeral CDP port');
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_CREATION_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 await context.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort('blockedbyclient'));
 const page=await context.newPage();page.setDefaultTimeout(15000);
 const errors=[],posts=[],lookups=[];let hold=null,listHold=null,dropNext=false,lost=null,blockReceipts=false,abortBeforeCommit=false;
 page.on('pageerror',error=>errors.push(error.message));
 page.on('request',request=>{const p=new URL(request.url()).pathname;if(p==='/api/v1/subscriptions'&&request.method()==='POST')posts.push({key:request.headers()['idempotency-key'],raw:request.postData(),body:request.postDataJSON()});if(p.startsWith('/api/v1/subscription-commands/'))lookups.push({path:p,method:request.method()});});
 await page.route('**/api/v1/subscription-commands/*',async route=>{if(blockReceipts)await route.fulfill({status:503,contentType:'application/problem+json',body:JSON.stringify({code:'OWNED_RECEIPT_TRANSPORT',message:'receipt transport unavailable'})});else await route.continue();});
 await page.route('**/api/v1/subscriptions',async route=>{
  if(route.request().method()!=='POST'){if(listHold)await listHold;await route.continue();return;}
  if(abortBeforeCommit){abortBeforeCommit=false;await route.abort('failed');return;}
  if(hold)await hold;
  if(dropNext){dropNext=false;const response=await route.fetch();assert.equal(response.status(),201);lost=await response.json();await route.abort('failed');return;}
  await route.continue();
 });
 const button=name=>page.getByRole('button',{name,exact:true});
 const response=(status)=>{const wait=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscriptions'&&r.request().method()==='POST'&&r.status()===status);void wait.catch(()=>{});return wait;};
 const evidence=async key=>{const r=await context.request.get(base+'/owned-m5-creation/evidence?key='+encodeURIComponent(key||''));assert.equal(r.status(),200);return r.json();};
 const overflow=async()=>assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'horizontal overflow at '+(await page.viewportSize()).width);
 const screenshot=async name=>{await overflow();const path=shots+'/'+name+'.png';await page.screenshot({path,fullPage:true});assert((await stat(path)).size>1000);};
 const pending=async()=>{
  const storage=await page.evaluate(()=>({session:{...sessionStorage},local:{...localStorage}}));
  const values=Object.entries(storage.session).filter(([key])=>key.startsWith('pulse:subscription-create:v1:'));
  assert.equal(values.length,1);const record=JSON.parse(values[0][1]);
  assert.deepEqual(Object.keys(record).sort(),['body','key','userId','version']);
  const allowed=new Set(['media_id','tmdb_id','media_type','root_id','quality_policy_id','indexer_ids','downloader_id','scope_type','seasons','episodes','selected_unit_ids','include_specials','expected_intent_version']);
  for(const key of Object.keys(record.body))assert(allowed.has(key),'unapproved persisted intent field '+key);
  assert.equal(record.key,posts.at(-1).key);assert.deepEqual(record.body,posts.at(-1).body);
  const raw=JSON.stringify(storage);for(const secret of ['Native trusted','Native recovery','Native retry','Native sparse','Native selected','1999','owned-creation-tmdb-key','http://','https://','password','query'])assert(!raw.includes(secret),'storage leaked '+secret);
  return record;
 };
 const search=async(type,title)=>{
  await page.goto(base+'/discover');
  await page.locator('button.type-btn').filter({hasText:type==='tv'?/^电视剧$/:/^电影$/}).click();
  const query=page.getByPlaceholder('输入电影或电视剧名称',{exact:false});await query.fill(title);
  const read=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/search/movies'&&r.status()===200);
  await query.press('Enter');await read;
  const card=page.locator('.media-card-wrap').filter({has:page.locator('.caption-title',{hasText:title})});
  await card.waitFor();await card.click();
  const modal=page.getByRole('dialog',{name:title+'详情',exact:true});await modal.waitFor();
  await modal.getByLabel('目标媒体根',{exact:true}).waitFor();
  await page.waitForFunction(()=>{const b=document.querySelector('.detail-actions .btn-primary');return b&&!b.disabled;});
  assert(await modal.getByLabel('目标媒体根',{exact:true}).inputValue(),'same-type default root must be selected');
  return modal;
 };
 const submit=modal=>modal.locator('.detail-actions .btn-primary');
 const accepted=async(modal,title)=>{const wait=response(201);await submit(modal).click();const r=await wait;const data=await r.json();await page.waitForURL(base+'/subscriptions');await page.getByText(title,{exact:true}).first().waitFor();return data;};
 await page.goto(base+'/discover');
 const auth=await context.request.get(base+'/api/v1/auth/me');assert.equal(auth.status(),200);
 // A deliberately held real POST proves busy/progress precede the 201 receipt.
 let modal=await search('movie','Native trusted movie');let release;
 hold=new Promise(resolve=>{release=resolve;});const movieResponse=response(201);await submit(modal).click();
 await page.getByText('正在提交订阅，等待确认结果。',{exact:true}).waitFor();
 assert(await page.locator('.media-card-wrap').filter({has:page.locator('.caption-title',{hasText:'Native trusted movie'})}).getByTitle('一键追更订阅',{exact:true}).isDisabled());assert.equal(new URL(page.url()).pathname,'/discover');
 assert.equal(await page.getByText('《Native trusted movie》订阅成功',{exact:true}).count(),0);
 assert.equal(await page.locator('.media-card-wrap').count(),3,'catalog rows retained while creating');
 await pending();await screenshot('creation-desktop-busy');release();hold=null;
 const movie=await (await movieResponse).json();await page.waitForURL(base+'/subscriptions');await page.getByText('Native trusted movie',{exact:true}).first().waitFor();
 assert.equal(posts[0].body.tmdb_id,550);assert.equal(posts[0].body.media_type,'movie');assert(posts[0].key);assert.equal(movie.root_id,posts[0].body.root_id);
 modal=await search('tv','Native sparse television');
 await modal.getByRole('button',{name:'🎯 自定义指定季与选集监控',exact:true}).click();
 await modal.getByRole('button',{name:'✕ 清空',exact:true}).click();
 await page.setViewportSize({width:375,height:812});await screenshot('creation-375-season');
 const tv=await accepted(modal,'Native sparse television');const tvPost=posts.at(-1);
 assert.equal(tvPost.body.tmdb_id,550);assert.equal(tvPost.body.media_type,'tv');assert.equal(tvPost.body.scope_type,'all');assert.deepEqual(tvPost.body.seasons,[1]);assert(!tvPost.body.episodes?.length);assert.notEqual(tv.media_id,movie.media_id);
 let observed=await evidence(tvPost.key);assert.equal(observed.graph.receipts,1);
 const tvUnits=observed.graph.units.filter(u=>u.media_id===tv.media_id);
 assert.deepEqual(tvUnits.map(u=>u.key).sort(),['S01E01','S01E03']);
 for(const unit of tvUnits){const [,s,e]=/^S(\d+)E(\d+)$/.exec(unit.key);assert.equal(unit.episode_id,5500000+Number(s)*100+Number(e));assert.equal(unit.series_id,550);}
 assert.equal(tvUnits.find(u=>u.key==='S01E03').air_date,null);assert(tvUnits.find(u=>u.key==='S01E01').air_date.startsWith('2020-01-01'));
 const wanted=observed.graph.wanted_graph.filter(w=>w.sub===tv.id);assert.equal(wanted.length,2);assert.deepEqual(wanted.filter(w=>!w.excluded&&!w.skipped).map(w=>w.key).sort(),['S01E01','S01E03']);
 await overflow();await screenshot('creation-375-subscriptions');await page.setViewportSize({width:1440,height:1000});
 // Dashboard uses its actual discover feed and the same production creation hook.
 await page.goto(base+'/');const dashboardCard=page.locator('.wall-card-modern').filter({has:page.locator('.wall-card-title',{hasText:'Native selected television'})});await dashboardCard.click();
 modal=page.getByRole('dialog',{name:'Native selected television详情',exact:true});await modal.getByRole('button',{name:'🎯 自定义指定季与选集监控',exact:true}).click();await modal.getByRole('button',{name:'✕ 清空',exact:true}).click();await modal.locator('.ep-item-chip').filter({has:page.getByText('第 1 集',{exact:true})}).click();
 const selected=await accepted(modal,'Native selected television');assert.equal(posts.at(-1).body.scope_type,'selected');assert.deepEqual(posts.at(-1).body.episodes,['S01E01']);assert.equal(posts.at(-1).body.media_type,'tv');assert(selected.id);
 // Lose one real committed 201; receipt reads remain unavailable until reload.
 modal=await search('movie','Native recovery movie');dropNext=true;blockReceipts=true;const beforeLost=posts.length;await submit(modal).click();
 await page.getByText('订阅结果未确认，已保留原请求。请先读取原命令结果。',{exact:true}).waitFor();assert(lost);assert.equal(posts.length,beforeLost+1);
 const original=await pending();await button('读取原订阅结果').click();await page.getByText('订阅结果未确认，已保留原请求。请先读取原命令结果。',{exact:true}).waitFor();await pending();
 const beforeRecovery=await evidence(original.key);assert.equal(beforeRecovery.graph.receipts,1);
 // Suppress only unrelated background feed refresh during the recovery observation.
 const feedPattern=url=>url.pathname==='/api/v1/discover';const feedFault=route=>route.abort('failed');await page.route(feedPattern,feedFault);
 let releaseList;listHold=new Promise(resolve=>{releaseList=resolve;});blockReceipts=false;const recovered=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscription-commands/'+original.key&&r.request().method()==='GET'&&r.status()===200);
 await page.reload();assert.deepEqual(await (await recovered).json(),lost);await page.waitForURL(base+'/subscriptions');
 assert.equal(posts.length,beforeLost+1);assert.deepEqual(await evidence(original.key),beforeRecovery);listHold=null;releaseList();await page.getByText('Native recovery movie',{exact:true}).first().waitFor();
 assert.equal(await page.evaluate(()=>Object.keys(sessionStorage).filter(k=>k.startsWith('pulse:subscription-create:v1:')).length),0);
 await page.unroute(feedPattern,feedFault);
 // A POST lost before publication gives a real 404. Reload never retries it.
 modal=await search('movie','Native retry movie');abortBeforeCommit=true;const beforeUnknown=posts.length;await submit(modal).click();await page.getByText('订阅结果未确认，已保留原请求。请先读取原命令结果。',{exact:true}).waitFor();const unknown=await pending();
 const unknownRead=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscription-commands/'+unknown.key&&r.status()===404);await page.reload();await unknownRead;await page.getByText('尚未查到原命令回执，发布结果仍未确认。可再次读取，或明确重试原请求。',{exact:true}).waitFor();assert.equal(posts.length,beforeUnknown+1);await pending();
 const explicit=response(201);await button('重试原订阅请求').click();await explicit;await page.waitForURL(base+'/subscriptions');assert.equal(posts.length,beforeUnknown+2);assert.equal(posts.at(-1).key,unknown.key);assert.equal(posts.at(-1).raw,posts[beforeUnknown].raw);assert.equal((await evidence(unknown.key)).graph.receipts,1);
 // A real stale intent (no expected version) fails 409 and keeps the original.
 modal=await search('tv','Native sparse television');const conflict=response(409);await submit(modal).click();await conflict;await page.getByText('原命令存在键冲突或意图版本已变化。请读取原结果并核对订阅当前版本，原请求已保留。',{exact:true}).waitFor();const conflictRecord=await pending();const conflictPosts=posts.length;
 await page.reload();await page.getByText('尚未查到原命令回执，发布结果仍未确认。可再次读取，或明确重试原请求。',{exact:true}).waitFor();assert.equal(posts.length,conflictPosts);assert.equal((await pending()).key,conflictRecord.key);
 await screenshot('creation-desktop-failure');assert.deepEqual(errors,[]);assert(lookups.every(r=>r.method==='GET'));
 console.log('M5_SUBSCRIPTION_CREATION_ACTUAL_CHROME_PASS '+JSON.stringify({realChrome:true,ephemeralCDP:true,realApp:true,realPG:true,actualCookie:true,nativeSearchOver8KiB:true,nativeMovieAndTV550:true,desktop:true,mobile375:true,defaultRoot:true,busyBeforeReceipt:true,catalogRetained:true,seasonOnlyAllScope:true,sparseProviderEpisodeIDs:true,nullableAirDates:true,realWanted:true,dashboardSelectedEpisode:true,lostCommitted201:true,reloadPureOriginalGET:true,noRecoveryPOST:true,oneOriginalReceipt:true,noMetadataOnRecovery:true,whitelistedPendingOnly:true,unknown404NoAutoPOST:true,explicitExactRetry:true,real409PreservesCommand:true,screenshots:4,tvAutomaticAcquisitionAccepted:false}));
} catch(error) {
 const page=context.pages().at(-1);if(page){console.error('BROWSER_FAILURE_DOM '+JSON.stringify({path:new URL(page.url()).pathname,text:(await page.locator('body').innerText()).slice(-14000)}));await page.screenshot({path:shots+'/creation-failure.png',fullPage:true});}
 throw error;
} finally {await context.close();}
`
