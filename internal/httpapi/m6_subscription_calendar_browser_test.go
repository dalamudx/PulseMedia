package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Run only through the private PG18.6 runner. Fixtures publish trusted media via
// the real Cookie API; SQL prepares mutable observations, never frozen history.
func TestM6SubscriptionCalendarActualChromeHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M6_CALENDAR_BROWSER") != "1" {
		t.Skip("set PULSE_M6_CALENDAR_BROWSER=1 with the owned runner")
	}
	assets, repo, shots := os.Getenv("PULSE_M6_CALENDAR_ASSETS"), os.Getenv("PULSE_M6_CALENDAR_REPO"), os.Getenv("PULSE_M6_CALENDAR_SCREENSHOTS")
	for _, path := range []string{assets, shots, os.Getenv("TMPDIR")} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Clean(path), "/data/temp/") {
			t.Fatal("task-owned assets, screenshots and temp required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, p := newM5CreationAcceptance(t)
	f.session = m5CreationLogin(t, f, "creation-admin")
	p.set("/3/movie/550", m5CreationJSON(map[string]any{"id": 550, "title": "Calendar native movie", "original_title": "Calendar native movie", "release_date": "2024-02-29", "overview": "Owned native movie metadata"}))
	p.set("/3/movie/550/credits", `{"id":550,"cast":[]}`)
	p.set("/3/movie/551", m5CreationJSON(map[string]any{"id": 551, "title": "Member private movie", "original_title": "Member private movie", "release_date": "2024-03-01", "overview": "Owned native member movie metadata"}))
	p.set("/3/movie/551/credits", `{"id":551,"cast":[]}`)
	movie := m5CreationPost(t, f, "calendar-movie", map[string]any{"tmdb_id": 550, "media_type": "movie"})
	_ = m5CreationID(t, movie, "id")
	subs := map[string]uuid.UUID{}
	for i, name := range []string{"Calendar sparse television", "Calendar paused television", "Calendar archived television"} {
		id := 550 + i
		m5CreationTV(p, id, i == 0, false, false)
		count := 2
		if i == 0 {
			count = 3
		}
		seasons := []map[string]any{{"season_number": 1, "episode_count": count}, {"season_number": 2, "episode_count": 2}, {"season_number": 3, "episode_count": 0}}
		if i == 0 {
			seasons[2]["episode_count"] = 1
		}
		p.set(fmt.Sprintf("/3/tv/%d", id), m5CreationJSON(map[string]any{"id": id, "name": name, "original_name": name, "first_air_date": "2024-02-29", "overview": "Owned sparse season dates, never inferred from counts", "seasons": seasons}))
		p.set(fmt.Sprintf("/3/tv/%d/credits", id), m5CreationJSON(map[string]any{"id": id, "cast": []any{}}))
		episode := func(sn, en int, date any) map[string]any {
			return map[string]any{"id": id*10000 + sn*100 + en, "season_number": sn, "episode_number": en, "air_date": date}
		}
		season1 := []map[string]any{episode(1, 1, "2024-02-29"), episode(1, 3, nil)}
		if i == 0 {
			season1 = append(season1, episode(1, 5, "2024-03-01"))
		}
		p.set(fmt.Sprintf("/3/tv/%d/season/1", id), m5CreationJSON(map[string]any{"season_number": 1, "episodes": season1}))
		p.set(fmt.Sprintf("/3/tv/%d/season/2", id), m5CreationJSON(map[string]any{"season_number": 2, "episodes": []map[string]any{episode(2, 1, "2024-02-28"), episode(2, 3, nil)}}))
		created := m5CreationPost(t, f, fmt.Sprintf("calendar-tv-%d", id), map[string]any{"tmdb_id": id, "media_type": "tv", "scope_type": "all"})
		subs[name] = m5CreationID(t, created, "id")
	}
	active := subs["Calendar sparse television"]
	// Only mutable Wanted provenance and retirement flags are prepared. No satisfied
	// projection, identity, season proof, receipt, review or authority is rewritten.
	m5InteractiveSQL(t, f, `UPDATE wanted_item w SET user_skipped=true FROM media_unit u WHERE w.unit_id=u.id AND w.subscription_id=$1 AND u.unit_key='S01E03'`, active)
	m5InteractiveSQL(t, f, `UPDATE wanted_item w SET scope_excluded=true FROM media_unit u WHERE w.unit_id=u.id AND w.subscription_id=$1 AND u.unit_key='S02E01'`, active)
	m5InteractiveSQL(t, f, `UPDATE media_unit u SET retired=true FROM subscription s WHERE s.id=$1 AND u.media_id=s.media_id AND u.unit_key='S02E03'`, active)
	m5InteractiveSQL(t, f, `UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1`, subs["Calendar paused television"])
	m5InteractiveSQL(t, f, `UPDATE subscription SET state='archived',intent_version=intent_version+1 WHERE id=$1`, subs["Calendar archived television"])
	member := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'calendar-member',password_hash,'member',true FROM app_user WHERE username='creation-admin'`, member)
	memberSession := m5CreationLogin(t, f, "calendar-member")
	memberCreated := f.request(t, "POST", m5CreationPath, "calendar-member-movie", map[string]any{"tmdb_id": 551, "media_type": "movie"}, memberSession)
	m5IndexerStatus(t, memberCreated, 201)
	m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day'`)
	if m5InteractiveInt(t, f, `SELECT count(*) FROM wanted_item WHERE is_satisfied`) != 0 {
		t.Fatal("initial real projection must be unsatisfied")
	}
	for _, table := range []string{"download_task", "episode_candidate_review", "download_client", "indexer"} {
		if m5InteractiveInt(t, f, "SELECT count(*) FROM "+table) != 0 {
			t.Fatal("unexpected execution fixture", table)
		}
	}
	// App.Run owns background evaluation; begin reads only after its real projections settle.
	until := time.Now().Add(20 * time.Second)
	const pending = `SELECT
	 (SELECT count(*) FROM job WHERE state='running') +
	 (SELECT count(*) FROM wanted_item w JOIN subscription s ON s.id=w.subscription_id
	  JOIN wanted_projection_validity p ON p.id=w.id
	  WHERE s.state='active' AND NOT w.is_skipped
	   AND (NOT p.inputs_current OR w.projection_valid_until<=clock_timestamp()
	    OR w.next_evaluate_at IS NULL OR w.next_evaluate_at<=clock_timestamp()))`
	for m5InteractiveInt(t, f, pending) != 0 {
		if time.Now().After(until) {
			t.Fatal("initial App jobs and Wanted projections did not settle")
		}
		time.Sleep(100 * time.Millisecond)
	}
	before := m6CalendarBusinessSnapshot(t, f)
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
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(assets, "index.html"))
	}))
	t.Cleanup(bridge.Close)
	scratch := t.TempDir()
	script := filepath.Join(scratch, "calendar-browser.mjs")
	if err = os.WriteFile(script, []byte(m6SubscriptionCalendarChromeProgram), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 110*time.Second)
	defer cancel()
	cdp, stopChrome := startFileMappingBrowser(t, ctx)
	if strings.HasSuffix(cdp, ":9222") {
		stopChrome()
		t.Fatal("shared Chrome port rejected")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stopChrome()
			client := &http.Client{Timeout: time.Second}
			response, e := client.Get(cdp + "/json/version")
			if e == nil {
				_ = response.Body.Close()
				t.Error("owned Chrome still accepts CDP after cleanup")
			} else {
				t.Log("M6_SUBSCRIPTION_CALENDAR_CHROME_CLEANUP_VERIFIED")
			}
			client.CloseIdleConnections()
		})
	}
	t.Cleanup(stop)
	command := exec.CommandContext(ctx, "node", script)
	command.WaitDelay = 3 * time.Second
	command.Env = append(os.Environ(), "PULSE_M6_CALENDAR_BASE="+bridge.URL, "PULSE_M6_CALENDAR_SESSION="+f.session, "PULSE_M6_CALENDAR_MEMBER_SESSION="+memberSession, "PULSE_M6_CALENDAR_CDP="+cdp)
	var output bytes.Buffer
	command.Stdout = io.MultiWriter(&output, os.Stdout)
	command.Stderr = command.Stdout
	err = command.Run()
	stop()
	if err != nil {
		t.Fatalf("actual calendar Chrome: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "M6_SUBSCRIPTION_CALENDAR_BROWSER_PASS ") {
		t.Fatal("missing actual browser receipt")
	}
	if after := m6CalendarBusinessSnapshot(t, f); after != before {
		var left, right map[string]json.RawMessage
		if json.Unmarshal([]byte(before), &left) == nil && json.Unmarshal([]byte(after), &right) == nil {
			for table, original := range left {
				if bytes.Equal(original, right[table]) {
					continue
				}
				var oldRows, newRows []map[string]json.RawMessage
				if json.Unmarshal(original, &oldRows) == nil && json.Unmarshal(right[table], &newRows) == nil {
					fields := map[string]bool{}
					for i := range oldRows {
						if i >= len(newRows) {
							break
						}
						for field, value := range oldRows[i] {
							if !bytes.Equal(value, newRows[i][field]) {
								fields[field] = true
							}
						}
					}
					names := make([]string, 0, len(fields))
					for field := range fields {
						names = append(names, field)
					}
					sort.Strings(names)
					t.Logf("snapshot changed table=%s rows=%d/%d fields=%v", table, len(oldRows), len(newRows), names)
				} else {
					t.Logf("snapshot changed table=%s", table)
				}
			}
		}
		t.Fatal("read-only calendar/card navigation mutated business facts or created work")
	}
	for _, path := range []string{"/3/movie/550", "/3/movie/551", "/3/tv/550", "/3/tv/550/season/1", "/3/tv/550/season/2", "/3/tv/551", "/3/tv/552"} {
		if p.pathCount(path) == 0 {
			t.Error("native metadata route not exercised", path)
		}
	}
	t.Log("M6_SUBSCRIPTION_CALENDAR_APP_COOKIE_PG_PASS business_mutations=0 new_jobs=0 reviews=0 tasks=0 Add=0 real_progress=0/3 synthetic_satisfied=model_only")
	t.Log("M6_SUBSCRIPTION_CALENDAR_SCREENSHOTS_READY " + shots)
}

func m6CalendarBusinessSnapshot(t *testing.T, f *m5IndexerAcceptance) string {
	t.Helper()
	// Runtime session touches, recurring-job leases and outbox delivery attempts are
	// not business mutations. All job identities/payloads and event facts are fenced.
	const query = `SELECT jsonb_build_object(
 'media',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM media x),
 'identity',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY media_id,namespace),'[]') FROM media_identity x),
 'unit',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM media_unit x),
 'unit_identity',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY unit_id),'[]') FROM media_unit_identity x),
 'season',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY media_id,media_version,season_number),'[]') FROM tv_season_metadata_snapshot x),
 'subscription',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM subscription x),
 'wanted',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM wanted_item x),
 'receipt',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM idempotency_record x),
 'audit',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM audit_log x),
 'task',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM download_task x),
 'review',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM episode_candidate_review x),
 'reservation',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM unit_reservation x),
 'requirement',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]') FROM download_requirement x),
 'job',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,kind,payload) ORDER BY id),'[]') FROM job),
 'event',(SELECT coalesce(jsonb_agg(jsonb_build_array(id,event_type,payload) ORDER BY id),'[]') FROM outbox_event))::text`
	var snapshot string
	if err := f.app.DBPool().QueryRow(t.Context(), query).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

const m6SubscriptionCalendarChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {readFile,stat} from 'node:fs/promises';
const env=process.env,load=createRequire(env.PULSE_M6_CALENDAR_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=env.PULSE_M6_CALENDAR_BASE,shots=env.PULSE_M6_CALENDAR_SCREENSHOTS;
assert.notEqual(new URL(env.PULSE_M6_CALENDAR_CDP).port,'9222');
const browser=await chromium.connectOverCDP(env.PULSE_M6_CALENDAR_CDP);
const context=await browser.newContext({viewport:{width:1280,height:900},timezoneId:'UTC',reducedMotion:'reduce',serviceWorkers:'block'});
try {
 const cookie=async value=>context.addCookies([{name:'pulsemedia_session',value,url:base,httpOnly:true,sameSite:'Strict'}]);
 await cookie(env.PULSE_M6_CALENDAR_SESSION);
 await context.route('**/*',route=>new URL(route.request().url()).origin===base?route.continue():route.abort('blockedbyclient'));
 const page=await context.newPage();page.setDefaultTimeout(8000);
 // Change only the browser presentation clock. Cookie, PG, wanted and server
 // timestamps remain real; optional anchors keep future maintenance inexpensive.
 const today=env.PULSE_M6_CALENDAR_DISPLAY_DATE||'2024-02-29';
 assert(/^\d{4}-\d{2}-\d{2}$/.test(today));
 await page.clock.setFixedTime(new Date(today+'T12:00:00Z'));
 const errors=[],writes=[],reads=[];
 page.on('pageerror',error=>errors.push(error.message));
 page.on('request',request=>{const url=new URL(request.url());if(url.pathname.startsWith('/api/')){if(request.method()==='GET')reads.push(url.pathname);else writes.push({path:url.pathname,method:request.method()});}});
 const get=async(path,request=context.request)=>{const response=await request.get(base+path);assert.equal(response.status(),200,'real Cookie GET '+path);return response.json();};
 const all=(await get('/api/v1/subscriptions')).items;
 assert.equal(all.length,5);
 const active=all.find(s=>s.title==='Calendar sparse television'),paused=all.find(s=>s.title==='Calendar paused television'),archived=all.find(s=>s.title==='Calendar archived television');
 assert(active&&paused&&archived);assert.equal(paused.state,'paused');assert.equal(archived.state,'archived');
 assert.equal(active.media_type,'tv');
 const wanted=active.wanted_items;
 assert.deepEqual(wanted.map(w=>w.unit_key).sort(),['S01E01','S01E03','S01E05','S02E01','S02E03','S03E07']);
 assert.equal(wanted.find(w=>w.unit_key==='S01E01').air_date,'2024-02-29');
 assert.equal(wanted.find(w=>w.unit_key==='S01E03').air_date??null,null);
 assert(wanted.find(w=>w.unit_key==='S01E03').is_skipped);
 assert(wanted.find(w=>w.unit_key==='S02E01').scope_excluded);
 assert(wanted.find(w=>w.unit_key==='S02E03').retired);
 const effective=wanted.filter(w=>!w.is_skipped&&!w.scope_excluded&&!w.retired);
 assert.equal(effective.length,3);assert(effective.every(w=>!w.is_satisfied));
 // Pure production model exercises positive progress without writing a trusted
 // satisfied projection or claiming that this browser performed an import.
 const ts=load('typescript');
 const source=await readFile(env.PULSE_M6_CALENDAR_REPO+'/web/src/features/configuration/subscriptionCalendar.ts','utf8');
 const compiled=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ESNext,target:ts.ScriptTarget.ES2022}}).outputText;
 const model=await import('data:text/javascript;base64,'+Buffer.from(compiled).toString('base64'));
 assert.deepEqual(model.subscriptionProgress(active),{mediaType:'tv',total:3,satisfied:0,percent:0});
 const synthetic={...active,wanted_items:wanted.map(w=>({...w,is_satisfied:w.unit_id===effective[0].unit_id}))};
 assert.deepEqual(model.subscriptionProgress(synthetic),{mediaType:'tv',total:3,satisfied:1,percent:33});
 const button=(name,scope=page)=>scope.getByRole('button',{name,exact:true});
 const cards=()=>page.locator('.sub-poster-card');
 const calendar=()=>page.locator('.subscription-calendar');
 const card=title=>cards().filter({has:button('查看'+title+'订阅详情')});
 const tab=async name=>button(name).click();
 const frame=()=>page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 const shot=async name=>{const path=shots+'/'+name+'.png';await page.screenshot({path,fullPage:true,animations:'disabled'});assert((await stat(path)).size>1000);};
 const fits=async selector=>{
  await frame();const geometry=await page.evaluate(selector=>({width:innerWidth,scroll:document.documentElement.scrollWidth,bad:[...document.querySelectorAll(selector)].filter(el=>el.getClientRects().length).map(el=>{const r=el.getBoundingClientRect();return {label:el.getAttribute('aria-label')||el.className,left:r.left,right:r.right,scroll:el.scrollWidth,client:el.clientWidth};}).filter(r=>r.left< -1||r.right>innerWidth+1||r.scroll>r.client+1)}),selector);
  assert(geometry.scroll<=geometry.width+1,'page horizontal overflow '+JSON.stringify(geometry));assert.deepEqual(geometry.bad,[],'component overflow '+JSON.stringify(geometry));
 };
 async function openExisting(trigger,sub){
  const observed=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscriptions/'+sub.id+'/status'&&r.status()===200);void observed.catch(()=>{});
  await trigger.focus();await trigger.press('Enter');
  const dialog=page.getByRole('dialog',{name:sub.title,exact:true});await dialog.waitFor();await observed;
  assert.equal(await page.locator('.media-detail-modal').count(),0,'existing subscription detail, not media creation');
  await button('关闭',dialog).last().click();await dialog.waitFor({state:'hidden'});
 }
 await page.goto(base+'/subscriptions');await cards().first().waitFor();assert.equal(await cards().count(),5);
 await tab('电影订阅 (2)');assert.equal(await cards().count(),2);assert.equal(await card(active.title).count(),0);
 await tab('电视剧追更 (3)');assert.equal(await cards().count(),3);
 assert((await card(active.title).innerText()).includes('0%'));
 const bar=card(active.title).getByRole('progressbar',{name:'追更投影进度',exact:true});assert.equal(await bar.getAttribute('aria-valuenow'),'0');
 assert((await card(active.title).innerText()).includes('已选 3 集'),'real denominator excludes skipped/scope-excluded/retired');
 assert((await card(paused.title).innerText()).includes('已暂停'));
 const search=page.getByPlaceholder('搜索已订阅影片...');await search.fill('sparse');assert.equal(await cards().count(),1);
 await openExisting(button('查看'+active.title+'订阅详情'),active);await search.fill('');
 for(const width of [1280,768,375,320]){await page.setViewportSize({width,height:900});await fits('.subscriptions-container,.media-control-bar,.tabs-bar,.sub-poster-card,.card-caption,.caption-title,.caption-sub-row,.caption-progress-tag');await shot('cards-'+width);}
 await page.setViewportSize({width:1280,height:900});await tab('追剧日历');await calendar().waitFor();
 const press=async name=>{await button(name,calendar()).click();await frame();};
 assert.equal(await button('月',calendar()).getAttribute('aria-pressed'),'true');
 // Navigate from the configurable displayed Today to the fixed native leap date.
 let offset=(Number(today.slice(0,4))-2024)*12+Number(today.slice(5,7))-2;
 assert(Math.abs(offset)<=36,'display-date anchor must be within 36 months of native fixture');
 while(offset>0){await press('上个月');offset--;}
 while(offset<0){await press('下个月');offset++;}
 await calendar().getByRole('heading',{name:'追剧日历 · 2024年2月',exact:true}).waitFor();
 const leap=calendar().getByRole('region',{name:'2024-02-29 周四',exact:true});await leap.waitFor();
 await leap.getByRole('button',{name:active.title+' S01E01 2024-02-29 待满足',exact:true}).waitFor();
 await leap.getByRole('button',{name:paused.title+' S01E01 2024-02-29 已暂停 待满足',exact:true}).waitFor();
 const unknown=calendar().locator('.calendar-unknown');await unknown.getByRole('button',{name:active.title+' S01E03 播出日期未知 已跳过',exact:true}).waitFor();
 assert(!(await unknown.innerText()).match(/S01E02|获取|下载|补缺/),'skipped sparse unit is labelled without acquisition advice');
 const labels=await calendar().locator('.calendar-episode').evaluateAll(nodes=>nodes.map(n=>n.getAttribute('aria-label')));
 assert(!labels.some(label=>label.includes(archived.title)||label.startsWith(active.title+' S02E01 ')||label.startsWith(active.title+' S02E03 ')),'archived/scope-excluded/retired never appear');
 assert(!labels.some(label=>label.includes('S01E02')),'no synthesized E02');
 await openExisting(leap.getByRole('button',{name:active.title+' S01E01 2024-02-29 待满足',exact:true}),active);
 await press('下个月');await calendar().getByRole('heading',{name:'追剧日历 · 2024年3月',exact:true}).waitFor();
 await calendar().getByRole('button',{name:active.title+' S01E05 2024-03-01 待满足',exact:true}).waitFor();
 await press('下个月');await calendar().getByRole('status').filter({hasText:'当前日期范围内暂无已排期剧集。'}).waitFor();
 assert(await unknown.isVisible(),'empty known month preserves unknown dates');
 await press('上个月');await press('上个月');
 for(const width of [1280,768,375,320]){
  await page.setViewportSize({width,height:900});
  await fits('.subscription-calendar,.calendar-header,.calendar-controls,.calendar-day,.calendar-episode,.calendar-unknown');await shot('month-'+width);
 }
 await press('周');assert.equal(await button('周',calendar()).getAttribute('aria-pressed'),'true');assert.equal(await calendar().locator('.calendar-day').count(),7);
 // A configurable Today may retain a different day after month navigation.
 // Move the actual displayed week until both native boundary dates are present.
 for(let step=0;step<5;step++){
  const dates=await calendar().locator('.calendar-day h3 time').evaluateAll(nodes=>nodes.map(n=>n.getAttribute('datetime')));
  if(dates.includes('2024-02-29'))break;
  await press(dates.at(-1)<'2024-02-29'?'下一周':'上一周');
 }
 await calendar().getByRole('region',{name:'2024-02-29 周四',exact:true}).waitFor();
 await calendar().getByRole('region',{name:'2024-03-01 周五',exact:true}).waitFor();
 const range=await calendar().locator('.calendar-range').innerText();
 await press('下一周');assert.notEqual(await calendar().locator('.calendar-range').innerText(),range);await press('上一周');assert.equal(await calendar().locator('.calendar-range').innerText(),range);
 for(const width of [1280,768,375,320]){await page.setViewportSize({width,height:900});await fits('.subscription-calendar,.calendar-header,.calendar-controls,.calendar-day,.calendar-episode,.calendar-unknown');await shot('week-'+width);}
 await press('今天');await calendar().locator('time[aria-current="date"][datetime="'+today+'"]').waitFor();
 await press('月');await press('今天');assert.equal(await calendar().locator('time[aria-current="date"]').getAttribute('datetime'),today);
 await search.fill('paused');assert((await calendar().innerText()).includes(paused.title));assert(!(await calendar().innerText()).includes(active.title));await search.fill('');
 // Native member scope and in-place AuthContext recheck; no synthetic user/API.
 await cookie(env.PULSE_M6_CALENDAR_MEMBER_SESSION);
 const memberList=await get('/api/v1/subscriptions');assert.equal(memberList.items.length,1);assert.equal(memberList.items[0].title,'Member private movie');
 const denied=await context.request.get(base+'/api/v1/subscriptions/'+active.id);assert.equal(denied.status(),403);
 await page.evaluate(()=>window.__m6CalendarAuth.checkStatus());
 await page.waitForFunction(()=>!document.body.textContent.includes('Calendar sparse television')&&!document.body.textContent.includes('Calendar paused television'));
 await tab('全部订阅 (1)');await card('Member private movie').waitFor();assert.equal(await cards().count(),1);
 assert.deepEqual(writes,[],'read-only selection/filter/navigation must issue no commands, searches, reviews or Add');assert.deepEqual(errors,[]);
 console.log('M6_SUBSCRIPTION_CALENDAR_BROWSER_PASS '+JSON.stringify({actualChrome:true,realCookieAppPG18_6:true,widths:[1280,768,375,320],nativeDates:true,sparseUnknown:true,leapBoundary:true,paused:true,excludedRetiredArchived:true,searchDetail:true,memberFence:true,businessWrites:0,realProgress:'0/3',positiveProgress:'pure model 1/3 only; no imported file or satisfied projection fabricated'}));
} finally {await context.close();await browser.close();}
`
