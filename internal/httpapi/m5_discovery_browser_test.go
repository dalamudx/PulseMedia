package httpapi_test

import (
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Run only after Main's RUNTIME_READY, through scripts/verify_m5_discovery.sh.
// The bridge serves assets; every business request reaches the Cookie-authenticated
// App. Source/metadata/downloader fixtures speak their actual native protocols.
func TestM5DiscoveryCatalogSubscriptionActualChromeHTTPPG(t *testing.T) {
	m5DiscoveryBrowser(t, "catalog")
}
func TestM5DiscoveryTVHandoffActualChromeHTTPPG(t *testing.T)  { m5DiscoveryBrowser(t, "tv") }
func TestM5DiscoveryMoviePanelActualChromeHTTPPG(t *testing.T) { m5DiscoveryBrowser(t, "movie") }

func m5DiscoveryBrowser(t *testing.T, mode string) {
	t.Helper()
	if os.Getenv("PULSE_M5_DISCOVERY_BROWSER") != "1" {
		t.Skip("owned PG18.6 and actual Chrome runner required")
	}
	assets, repo, shots := os.Getenv("PULSE_M5_CREATION_ASSETS"), os.Getenv("PULSE_M5_CREATION_REPO"), os.Getenv("PULSE_M5_DISCOVERY_SCREENSHOTS")
	for _, path := range []string{assets, shots} {
		if !filepath.IsAbs(path) || !strings.HasPrefix(path, "/data/temp/") {
			t.Fatal("owned assets/screenshots required")
		}
	}
	if !filepath.IsAbs(repo) {
		t.Fatal("absolute repository required")
	}
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal(err)
	}
	h := &m5AutomaticTVAcceptance{addEntered: make(chan struct{}), releaseAdd: make(chan struct{})}
	h.raw, h.hash, h.names, h.payloads = m5AutomaticTVTorrent()
	for _, body := range h.payloads {
		h.total += int64(len(body))
	}
	h.unblockAdd()
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveQB(t, w, r) }))
	t.Cleanup(qb.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/torrent" {
			h.fetches.Add(1)
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(h.raw)
			return
		}
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api" || q.Get("apikey") != "owned-discovery-source" {
			t.Error("native source configuration changed")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if q.Get("t") == "caps" {
			_, _ = io.WriteString(w, `<caps><limits default="50" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q"/><tv-search available="yes" supportedParams="q,season,ep"/></searching></caps>`)
			return
		}
		// Automatic TV and health searches expose no resources. Manual interaction is
		// a separate source observation, never an automatic execution authority.
		title := q.Get("q")
		if (q.Get("t") != "search" && (q.Get("t") != "movie" || q.Get("limit") != "50")) || (title != m5AutomaticTVTitle && title != "Native trusted movie" && title != "Native selected television") {
			_, _ = io.WriteString(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="0"/></channel></rss>`)
			return
		}
		h.searches.Add(1)
		names := []string{m5AutomaticTVRelease}
		if title == "Native trusted movie" {
			names = []string{"Native.trusted.movie.1999.1080p.BluRay.x264"}
		}
		_, _ = fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="%d"/>`, len(names))
		for i, name := range names {
			_, _ = fmt.Fprintf(w, `<item><title>%s</title><guid>owned-discovery-%d-%s</guid><enclosure url="http://%s/torrent" length="%d" type="application/x-bittorrent"/><torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="42"/><torznab:attr name="peers" value="1"/></item>`, name, i, title, r.Host, h.total, h.hash)
		}
		_, _ = io.WriteString(w, `</channel></rss>`)
	}))
	t.Cleanup(source.Close)
	f, p := newM5CreationAcceptance(t)
	h.f, h.metadata = f, p
	f.session = m5CreationLogin(t, f, "creation-admin")
	m5CreationBrowserCatalog(t, p)
	p.set("/3/tv/550/season/1", `{"season_number":1,"episodes":[{"id":5500101,"season_number":1,"episode_number":1,"air_date":"2020-01-01"},{"id":5500103,"season_number":1,"episode_number":3,"air_date":"2020-01-15"}]}`)
	// Reuse the trusted season/provider identities; only native display/search
	// responses change. The single release matches the real subscribed series.
	p.mu.Lock()
	var tv map[string]any
	tvErr := json.Unmarshal([]byte(p.bodies["/3/tv/550"]), &tv)
	p.mu.Unlock()
	if tvErr != nil {
		t.Fatal(tvErr)
	}
	tv["name"], tv["original_name"], tv["genres"] = m5AutomaticTVTitle, m5AutomaticTVTitle, []map[string]string{{"name": "剧情"}}
	p.set("/3/tv/550", m5CreationJSON(tv))
	p.mu.Lock()
	var movie, other map[string]any
	_ = json.Unmarshal([]byte(p.bodies["/3/movie/550"]), &movie)
	_ = json.Unmarshal([]byte(p.bodies["/3/tv/551"]), &other)
	p.mu.Unlock()
	movie["genres"] = []map[string]string{{"name": "剧情"}}
	movie["overview"] = "动作 科幻 are words in this overview, not genres"
	movie["genre_ids"] = []int{18}
	tv["genre_ids"] = []int{18}
	other["genre_ids"] = []int{28}
	movie["media_type"], tv["media_type"], other["media_type"] = "movie", "tv", "tv"
	movie["overview"] = "动作 科幻 are words in this overview, not genres"
	p.set("/3/movie/550", m5CreationJSON(movie))
	movieFeed := m5CreationJSON(map[string]any{"page": 1, "total_pages": 1, "results": []any{movie}})
	tvFeed := m5CreationJSON(map[string]any{"page": 1, "total_pages": 1, "results": []any{tv, other}})
	allFeed := m5CreationJSON(map[string]any{"page": 1, "total_pages": 1, "results": []any{movie, tv, other}})
	restore := func() {
		p.set("/3/search/movie", movieFeed)
		p.set("/3/search/tv", tvFeed)
		p.set("/3/search/multi", allFeed)
	}
	restore()
	for path, body := range map[string]string{"/3/trending/all/day": allFeed, "/3/movie/popular": movieFeed, "/3/tv/popular": tvFeed, "/3/movie/top_rated": movieFeed} {
		p.set(path, body)
	}
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT id,path FROM library_root WHERE media_type='tv' AND health='online' AND baseline_completed`).Scan(&h.root, &h.rootPath); err != nil {
		t.Fatal(err)
	}
	indexer := f.request(t, "POST", "/api/v1/indexers", "owned-discovery-source", map[string]any{"name": "Owned discovery source", "protocol": "torznab", "url": source.URL, "api_key": "owned-discovery-source", "categories": []int{5000}}, f.session)
	m5IndexerStatus(t, indexer, 201)
	h.indexer = m5CreationID(t, indexer, "id")
	probe := f.request(t, "POST", "/api/v1/indexers/"+h.indexer.String()+"/test", "", nil, f.session)
	m5IndexerStatus(t, probe, 200)
	if m5IndexerObject(t, probe)["alive"] != true {
		t.Fatal("native movie health probe failed")
	}
	client := f.request(t, "POST", "/api/v1/download-clients", "", map[string]any{"name": "Owned discovery qB", "client_type": "qbittorrent", "url": qb.URL, "username": "owned-tv", "password": "owned-tv-client-key", "remote_path_map": map[string]string{}}, f.session)
	m5IndexerStatus(t, client, 201)
	h.client = m5CreationID(t, client, "id")
	otherID := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'discovery-foreign',password_hash,'admin',true FROM app_user WHERE username='creation-admin'`, otherID)
	foreign := m5CreationLogin(t, f, "discovery-foreign")
	var sub, second, foreignSub uuid.UUID
	if mode == "tv" {
		m5InteractiveSQL(t, f, `UPDATE indexer_lifecycle SET enabled=false WHERE indexer_id=$1`, h.indexer)
		body := map[string]any{"tmdb_id": 550, "media_type": "tv", "root_id": h.root, "downloader_id": h.client, "indexer_ids": []uuid.UUID{}, "quality_policy_id": "default", "scope_type": "all"}
		receipt := m5CreationPost(t, f, "discovery-own-tv", body)
		sub = m5CreationID(t, receipt, "id")
		foreignReceipt := f.request(t, "POST", m5CreationPath, "discovery-foreign-tv", body, foreign)
		m5IndexerStatus(t, foreignReceipt, 201)
		foreignSub = m5CreationID(t, foreignReceipt, "id")
		body["tmdb_id"] = 551
		m5IndexerStatus(t, f.request(t, "POST", m5CreationPath, "discovery-foreign-other", body, foreign), 201)
		// Keep this manual-acceptance fixture's automatic evaluation outside the run.
		// Configure the real source before any review; no execution graph is inserted.
		m5InteractiveSQL(t, f, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '1 day'`)
		m5InteractiveSQL(t, f, `UPDATE subscription SET indexer_ids=$1::jsonb WHERE id=$2`, m5CreationJSON([]uuid.UUID{h.indexer}), sub)
		m5InteractiveSQL(t, f, `UPDATE indexer_lifecycle SET enabled=true WHERE indexer_id=$1`, h.indexer)
		// An offline second-root intent is a picker counterexample only. It has no
		// scan baseline, Wanted, review, execution authority or download task.
		root := uuid.New()
		second = uuid.New()
		m5InteractiveSQL(t, f, `INSERT INTO library_root(id,name,path,media_type,health) VALUES($1,'Owned offline second TV root',$2,'tv','offline')`, root, filepath.Join(t.TempDir(), "offline"))
		m5InteractiveSQL(t, f, `INSERT INTO subscription(id,user_id,media_id,root_id,quality_policy_id,quality_policy_revision,indexer_ids,downloader_id,state,intent_version,scope_type) SELECT $1,user_id,media_id,$2,quality_policy_id,quality_policy_revision,indexer_ids,downloader_id,'active',1,scope_type FROM subscription WHERE id=$3`, second, root, sub)
	}
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
	var mu sync.Mutex
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/owned-m5-discovery/source-ready" {
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			for {
				var ready bool
				err := f.app.DBPool().QueryRow(ctx, `SELECT next_query_at<=clock_timestamp() AND (rate_limit_until IS NULL OR rate_limit_until<=clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM job WHERE kind='automatic_tv_source' AND state IN ('queued','running')) FROM indexer WHERE id=$1`, h.indexer).Scan(&ready)
				if err != nil {
					http.Error(w, "owned budget observation unavailable", http.StatusServiceUnavailable)
					return
				}
				if ready {
					w.WriteHeader(204)
					return
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					http.Error(w, "owned budget deadline", http.StatusServiceUnavailable)
					return
				case <-timer.C:
				}
			}
		}
		if r.URL.Path == "/owned-m5-discovery/catalog" {
			mu.Lock()
			defer mu.Unlock()
			switch r.URL.Query().Get("mode") {
			case "empty":
				p.set("/3/search/multi", `{"page":1,"total_pages":1,"results":[]}`)
			case "fail":
				p.set("/3/search/multi", `{"results":null}`)
			default:
				restore()
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/owned-m5-discovery/evidence" {
			var data string
			err := f.app.DBPool().QueryRow(r.Context(), `SELECT jsonb_build_object('reviews',(SELECT count(*) FROM episode_candidate_review),'tasks',(SELECT count(*) FROM download_task),'jobs',(SELECT count(*) FROM job WHERE kind='episode_download_submission'),'subscriptions',(SELECT count(*) FROM subscription),'operations',(SELECT coalesce(jsonb_agg(operation_id),'[]') FROM download_task))::text`).Scan(&data)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
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
	script := filepath.Join(scratch, "discovery-browser.mjs")
	if err := os.WriteFile(script, []byte(m5DiscoveryChromeProgram), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 260*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "PULSE_M5_DISCOVERY_MODE="+mode, "PULSE_M5_DISCOVERY_BASE="+bridge.URL, "PULSE_M5_DISCOVERY_SESSION="+f.session, "PULSE_M5_DISCOVERY_FOREIGN="+foreign, "PULSE_M5_DISCOVERY_SUB="+sub.String(), "PULSE_M5_DISCOVERY_SECOND="+second.String(), "PULSE_M5_DISCOVERY_FOREIGN_SUB="+foreignSub.String(), "PULSE_M5_DISCOVERY_SCRATCH="+scratch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var diagnostics string
		readErr := f.app.DBPool().QueryRow(t.Context(), `SELECT jsonb_build_object('next_query_at',i.next_query_at,'observed_at',clock_timestamp(),'health',i.health,'health_reason',i.health_reason,'health_failures',i.health_failures,'queries',(SELECT coalesce(jsonb_agg(jsonb_build_object('kind',q.query_type,'state',q.state,'count',q.results_count)),'[]') FROM indexer_query q WHERE q.indexer_id=i.id),'automatic_jobs',(SELECT count(*) FROM job WHERE kind='automatic_tv_source'))::text FROM indexer i WHERE id=$1`, h.indexer).Scan(&diagnostics)
		t.Fatalf("actual discovery Chrome %s: %v\n%s\nowned read-only diagnostics=%s readErr=%v", mode, err, out, diagnostics, readErr)
	}
	if !strings.Contains(string(out), "M5_DISCOVERY_"+strings.ToUpper(mode)+"_PASS") {
		t.Fatal("missing actual browser receipt")
	}
	if mode == "tv" {
		m5AutomaticTVWait(t, 30*time.Second, "manual native Add", func(context.Context) (bool, error) { return h.adds.Load() == 1, nil })
		if h.fetches.Load() != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM episode_candidate_review`) != 1 || m5InteractiveInt(t, f, `SELECT count(*) FROM download_task`) != 1 {
			t.Fatal("manual review/task/torrent not unique", h.fetches.Load(), h.adds.Load())
		}
	} else if h.adds.Load() != 0 {
		t.Fatal("catalog/movie entry created unconfirmed download")
	}
	t.Log(strings.TrimSpace(string(out)))
}

const m5DiscoveryChromeProgram = `
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {readFile,stat} from 'node:fs/promises';
const load=createRequire(process.env.PULSE_M5_CREATION_REPO+'/web/package.json');
const {chromium}=load('playwright-core');
const base=process.env.PULSE_M5_DISCOVERY_BASE,mode=process.env.PULSE_M5_DISCOVERY_MODE,sub=process.env.PULSE_M5_DISCOVERY_SUB;
const profile=process.env.PULSE_M5_DISCOVERY_SCRATCH+'/chrome';
const context=await chromium.launchPersistentContext(profile,{executablePath:process.env.PULSE_M5_CHROME||'/usr/bin/google-chrome',headless:true,viewport:{width:1440,height:1000},args:['--remote-debugging-port=0','--no-sandbox','--disable-dev-shm-usage','--disable-background-networking','--disable-component-update','--no-first-run']});
try {
 const port=Number((await readFile(profile+'/DevToolsActivePort','utf8')).split('\n')[0]);assert(port>0&&port!==9222);
 await context.addCookies([{name:'pulsemedia_session',value:process.env.PULSE_M5_DISCOVERY_SESSION,url:base,httpOnly:true,sameSite:'Strict'}]);
 await context.route('**/*',r=>new URL(r.request().url()).origin===base?r.continue():r.abort('blockedbyclient'));
 const page=await context.newPage();page.setDefaultTimeout(12000);
 const errors=[],requests=[],urls=[];
 page.on('pageerror',e=>errors.push(e.message));
 page.on('request',r=>{const u=new URL(r.url());if(u.pathname.startsWith('/api/'))requests.push({path:u.pathname,method:r.method(),body:r.postData(),key:r.headers()['idempotency-key']});});
 page.on('framenavigated',frame=>{if(frame===page.mainFrame())urls.push(frame.url());});
 const button=(name,scope=page)=>scope.getByRole('button',{name,exact:true});
 const card=title=>page.locator('.media-card-wrap').filter({has:page.locator('.caption-title',{hasText:title})});
 const evidence=async()=>{const r=await context.request.get(base+'/owned-m5-discovery/evidence');assert.equal(r.status(),200);return r.json();};
 const writeCount=()=>requests.filter(r=>r.method==='POST'&&/episode-(candidate-reviews|acquisitions)|tv-coverage-reviews/.test(r.path)).length;
 async function search(type,title,year='',expectedStatus=200){
  await page.goto(base+'/discover');await page.locator('button.type-btn').filter({hasText:type==='tv'?/^电视剧$/:type==='movie'?/^电影$/:/^全部$/}).click();
  await page.getByRole('spinbutton',{name:'上映或首播年份'}).fill(year);
  await page.getByRole('textbox',{name:'影视名称'}).fill(title);
  const response=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/search/movies'&&r.request().method()==='GET');
  await page.getByRole('textbox',{name:'影视名称'}).press('Enter');const r=await response;assert.equal(r.status(),expectedStatus);return r.json();
 }
 async function openResources(type,title){await search(type,title);await card(title).waitFor();await card(title).hover();const ready=await context.request.get(base+'/owned-m5-discovery/source-ready',{timeout:50000});assert.equal(ready.status(),204,'real native budget readiness');await card(title).getByTitle('检索站点种子',{exact:true}).click();try{await page.getByText('检索已完成。',{exact:true}).first().waitFor({timeout:35000});await page.locator('.release-item').first().waitFor();}catch(error){console.error('OWNED_DISCOVERY_SEARCH_DOM '+(await page.locator('body').innerText()).slice(-9000));throw error;}}
 async function privacy(candidate){
  const raw=await page.evaluate(()=>JSON.stringify({session:{...sessionStorage},local:{...localStorage},history:history.state}));
  for(const secret of ['Native Automatic Series','Native.Automatic.Series','owned-discovery-source','owned-tv-client-key','query','enclosure'])assert(!raw.includes(secret),'query/source in browser persistence: '+secret);
  if(candidate)assert(!raw.includes(candidate),'ephemeral candidate persisted before explicit command');
  for(const u of urls){const parsed=new URL(u);assert(!parsed.searchParams.has('candidate'));assert(!parsed.searchParams.has('query'));assert(!u.includes('Native.'));}
 }
 async function shot(name,width){await page.setViewportSize({width,height:width===375?812:1000});if(width===375){const backdrop=page.locator('.mobile-drawer-backdrop');if(await backdrop.isVisible())await backdrop.click({position:{x:350,y:150}});await page.waitForFunction(()=>document.querySelector('.sidebar').getBoundingClientRect().right<=1);}assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),'horizontal overflow');const path=process.env.PULSE_M5_DISCOVERY_SCREENSHOTS+'/'+name+'.png';await page.screenshot({path,fullPage:true});assert((await stat(path)).size>1000);}
 async function picker(title='Native Automatic Series') {await openResources('tv',title);const row=page.locator('.release-item').filter({hasText:'S01E03.'});await button('选择获取目标',row).click();await button('前往订阅审核此资源').click();return page.getByRole('dialog').filter({has:page.getByText('选择本剧的订阅目标。',{exact:false})});}
 async function enter(row){const detail=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscriptions/'+sub&&r.request().method()==='GET');const status=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscriptions/'+sub+'/status');await row.getByLabel('订阅目标根',{exact:true}).selectOption(sub);await button('进入原订阅审核',row).click();assert.equal((await detail).status(),200);assert.equal((await status).status(),200);await page.waitForURL(base+'/subscriptions?subscription='+sub);await page.getByRole('region',{name:'真实资源单集审核'}).waitFor();}
 if(mode==='catalog'){
  await page.goto(base+'/discover');await card('Native trusted movie').waitFor();assert.equal(await button('豆瓣高分').count(),0);await button('TMDB 高分').click();await card('Native trusted movie').waitFor();
  assert(requests.some(r=>r.path==='/api/v1/discover'));
  await button('动作').click();await page.getByText('当前筛选没有影视',{exact:true}).waitFor();assert.equal(await card('Native trusted movie').count(),0,'overview must not grant genre membership');await button('剧情').click();await card('Native trusted movie').waitFor();
  await search('movie','Native trusted movie','1999');await card('Native trusted movie').waitFor();
  let release,entered,settled;const held=new Promise(r=>{release=r;}),started=new Promise(r=>{entered=r;}),oldDone=new Promise(r=>{settled=r;});
  let holdOnce=true;const hold=async route=>{if(!holdOnce){await route.continue();return;}holdOnce=false;const r=await route.fetch();entered();await held;try{await route.fulfill({response:r});}catch(error){assert.match(error.message,/Route is already handled|Invalid InterceptionId/);}finally{settled();}};
  await page.route('**/api/v1/search/movies?*',hold);await page.getByRole('textbox',{name:'影视名称'}).fill('rapid-old');await page.getByRole('textbox',{name:'影视名称'}).press('Enter');await started;
  await page.getByRole('spinbutton',{name:'上映或首播年份'}).fill('2020');const latest=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/search/movies'&&new URL(r.url()).searchParams.get('type')==='tv');await page.locator('button.type-btn').filter({hasText:/^电视剧$/}).click();await latest;await card('Native Automatic Series').waitFor();release();await oldDone;await page.unroute('**/api/v1/search/movies?*',hold);assert.equal(await card('Native trusted movie').count(),0);
  await context.request.get(base+'/owned-m5-discovery/catalog?mode=fail');await search('all','owned-malformed','',503);await page.getByRole('alert').filter({hasText:/搜索/}).waitFor();assert.equal(await page.getByText('当前筛选没有影视',{exact:true}).count(),0);
  await context.request.get(base+'/owned-m5-discovery/catalog?mode=empty');await search('all','owned-empty');await page.getByText('当前筛选没有影视',{exact:true}).waitFor();await context.request.get(base+'/owned-m5-discovery/catalog?mode=restore');
  await search('all','same-numeric-identity');await card('Native trusted movie').waitFor();await card('Native Automatic Series').waitFor();
  await card('Native trusted movie').click();const modal=page.getByRole('dialog',{name:'Native trusted movie详情',exact:true});await modal.waitFor();await modal.getByLabel('目标媒体根',{exact:true}).waitFor();
  let releasePost,postEntered;const postHold=new Promise(r=>{releasePost=r;}),postStarted=new Promise(r=>{postEntered=r;});
  const holdPost=async route=>{if(route.request().method()!=='POST'){await route.continue();return;}postEntered();await postHold;await route.continue();};await page.route('**/api/v1/subscriptions',holdPost);
  const created=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscriptions'&&r.request().method()==='POST',{timeout:45000});void created.catch(()=>{});await modal.locator('.detail-actions .btn-primary').click();await postStarted;
  assert(await card('Native trusted movie').getByTitle('一键追更订阅',{exact:true}).isDisabled());assert(!(await card('Native Automatic Series').getByTitle('一键追更订阅',{exact:true}).isDisabled()),'movie 550 must not mark TV 550 busy');
  await shot('discovery-desktop',1440);await shot('discovery-375',375);releasePost();const movie=await created;assert.equal(movie.status(),201);const movieReceipt=await movie.json();await page.waitForURL(base+'/subscriptions?subscription='+movieReceipt.id);await page.unroute('**/api/v1/subscriptions',holdPost);
  await search('tv','Native Automatic Series');await card('Native Automatic Series').click();const tvModal=page.getByRole('dialog',{name:'Native Automatic Series详情',exact:true});await tvModal.getByLabel('目标媒体根',{exact:true}).waitFor();const tvCreated=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/subscriptions'&&r.request().method()==='POST');await tvModal.locator('.detail-actions .btn-primary').click();const tvResponse=await tvCreated;assert.equal(tvResponse.status(),201);const tvReceipt=await tvResponse.json();assert.notEqual(movieReceipt.media_id,tvReceipt.media_id);await page.waitForURL(base+'/subscriptions?subscription='+tvReceipt.id);assert.equal((await evidence()).tasks,0);
 } else if(mode==='movie') {
  await openResources('movie','Native trusted movie');await button('选择获取目标').first().click();const panel=page.getByRole('region',{name:'显式电影获取目标'});await panel.waitFor();await panel.getByLabel('电影目标根',{exact:true}).waitFor();await panel.getByLabel('获取下载器',{exact:true}).waitFor();assert(await button('确认创建持久获取',panel).isDisabled());await button('预览电影获取',panel).click();await panel.getByRole('alert').filter({hasText:'请明确选择电影目标根和下载器。'}).waitFor();
  let failure=503;const fault=async route=>route.request().method()==='GET'&&failure?route.fulfill({status:failure,contentType:'application/problem+json',body:'{"code":"OWNED_READ_FAILURE"}'}):route.continue();await page.route('**/api/v1/release-searches/*',fault);await button('刷新搜索观察').click();await page.getByRole('alert').filter({hasText:'当前观察读取失败'}).waitFor();assert.equal(await page.locator('.release-item').count(),1);assert(await button('选择获取目标').isDisabled());failure=0;await button('刷新搜索观察').click();await page.waitForFunction(()=>!document.querySelector('.release-item button')?.disabled);
  for(const status of [403,401]){failure=status;await button('刷新搜索观察').click();await page.waitForFunction(()=>document.querySelectorAll('.release-item').length===0);failure=0;await page.unroute('**/api/v1/release-searches/*',fault);if(status===403){await openResources('movie','Native trusted movie');await page.route('**/api/v1/release-searches/*',fault);}}
  assert.equal((await evidence()).tasks,0);await privacy();
 } else {
  const before=await evidence();let row=await picker('Native selected television');await row.getByText('没有当前账户下与本剧 TMDB 身份一致的订阅。请先订阅本剧，再重新选择资源进入审核。',{exact:true}).waitFor();assert(await button('进入原订阅审核',row).isDisabled());assert.equal(requests.filter(r=>r.path==='/api/v1/subscriptions'&&r.method==='POST').length,0);await button('关闭资源检索').click();
  row=await picker();const select=row.getByLabel('订阅目标根',{exact:true});try{await select.waitFor();}catch(error){console.error('OWNED_DISCOVERY_PICKER_DOM '+await row.innerText());throw error;}assert.equal(await select.locator('option').count(),3);assert.equal(await select.inputValue(),'');assert(await button('进入原订阅审核',row).isDisabled());assert.equal(await select.locator('option[value="'+process.env.PULSE_M5_DISCOVERY_FOREIGN_SUB+'"]').count(),0);assert.equal(await select.locator('option[value="'+process.env.PULSE_M5_DISCOVERY_SECOND+'"]').count(),1);
  // Read the actual persisted UUID from the public search receipt, not a fixture ID.
  const searchRequests=requests.filter(r=>r.path.startsWith('/api/v1/release-searches/')&&r.method==='GET');const searchPath=searchRequests.at(-1).path;const observed=await (await context.request.get(base+searchPath)).json();const candidate=observed.results.find(r=>r.title.includes('S01E03.')).id;
  await privacy(candidate);assert.equal(writeCount(),0);await enter(row);const panel=page.getByRole('region',{name:'真实资源单集审核'});await panel.getByRole('status').filter({hasText:'已选择发现页候选 '+candidate}).waitFor();assert.equal(writeCount(),0,'handoff must not explain/review/confirm');assert.equal(requests.filter(r=>r.path.endsWith('/explain-release')).length,0);await privacy(candidate);
  await page.reload();await button('查看编号映射').click();await panel.waitFor();assert.equal(await panel.getByLabel('待审核站点资源',{exact:true}).count(),0,'reload must lose ephemeral candidate');
  row=await picker();await enter(row);await panel.getByRole('status').filter({hasText:'已选择发现页候选 '+candidate}).waitFor();
  const coverage=page.getByRole('region',{name:'完整季覆盖审阅与获取'});await button('读取完整季覆盖',coverage).click();await coverage.getByRole('status').filter({hasText:'尚不能确认完整覆盖'}).waitFor();assert(await button('确认保存完整覆盖审阅',coverage).isDisabled());assert.equal((await evidence()).tasks,0);
  await button('读取当前候选证据',panel).click();await panel.getByText(/S01E03 · 单元版本/).waitFor();await panel.getByRole('checkbox',{name:'已核对当前资源确属此作品及单集，确认只保存审核，不创建任务',exact:true}).check();
  const reviewPath='/api/v1/subscriptions/'+sub+'/episode-candidate-reviews';let lost=true,key,body;
  const drop=async route=>{if(lost){lost=false;key=route.request().headers()['idempotency-key'];body=route.request().postData();const r=await route.fetch();assert.equal(r.status(),200);await route.abort('failed');}else{assert.equal(route.request().headers()['idempotency-key'],key);assert.equal(route.request().postData(),body);await route.continue();}};
  await page.route('**'+reviewPath,drop);await button('保存当前单集审核',panel).click();await panel.getByRole('alert').filter({hasText:/未确认|保留原候选/}).waitFor();const pending=await page.evaluate(id=>sessionStorage.getItem('pulse:episode-review:'+id),sub);assert(pending);
  // A new ephemeral candidate must never replace an unresolved original command.
  row=await picker();await enter(row);assert.equal(await page.evaluate(id=>sessionStorage.getItem('pulse:episode-review:'+id),sub),pending);await button('重试同一审核命令',panel).click();await panel.getByRole('status').filter({hasText:'尚未创建下载或整理任务'}).waitFor();await page.unroute('**'+reviewPath,drop);assert.equal((await evidence()).reviews,1);assert.equal((await evidence()).tasks,0);
  const acquire=page.getByRole('region',{name:'已审核标准单集获取'});await button('读取当前单集获取条件',acquire).click();await acquire.getByRole('status').filter({hasText:'当前条件允许另行确认标准单集获取'}).waitFor();
  const explainFault=route=>route.fulfill({status:503,contentType:'application/problem+json',body:'{"code":"OWNED_READ_FAILURE"}'});await page.route('**/episode-mapping/explain-release',explainFault);await button('读取当前单集获取条件',acquire).click();await acquire.getByRole('alert').filter({hasText:'获取条件读取失败'}).waitFor();assert(await button('确认获取标准单集',acquire).isDisabled());await page.unroute('**/episode-mapping/explain-release',explainFault);await button('读取当前单集获取条件',acquire).click();await acquire.getByRole('status').filter({hasText:'当前条件允许另行确认标准单集获取'}).waitFor();await acquire.getByRole('checkbox').check();
  const acquirePath='/api/v1/subscriptions/'+sub+'/episode-acquisitions';let acquireLost=true,acquireKey,acquireBody;
  const dropAcquire=async route=>{if(acquireLost){acquireLost=false;acquireKey=route.request().headers()['idempotency-key'];acquireBody=route.request().postData();const r=await route.fetch();assert.equal(r.status(),202,await r.text());await route.abort('failed');}else{assert.equal(route.request().headers()['idempotency-key'],acquireKey);assert.equal(route.request().postData(),acquireBody);await route.continue();}};
  await page.route('**'+acquirePath,dropAcquire);await button('确认获取标准单集',acquire).click();await acquire.getByRole('alert').filter({hasText:'获取回执未确认'}).waitFor();const old=await page.evaluate(id=>sessionStorage.getItem('pulse:episode-acquisition:'+id),sub);assert(old);await page.reload();await button('查看编号映射').click();await button('恢复同一单集获取命令',acquire).waitFor();assert.equal(await page.evaluate(id=>sessionStorage.getItem('pulse:episode-acquisition:'+id),sub),old);await button('读取当前单集获取条件',acquire).click();assert.equal(await page.evaluate(id=>sessionStorage.getItem('pulse:episode-acquisition:'+id),sub),old);await button('恢复同一单集获取命令',acquire).click();await acquire.getByRole('status').filter({hasText:'尚不代表下载器接受'}).waitFor();await page.unroute('**'+acquirePath,dropAcquire);
  const after=await evidence();assert.equal(after.tasks,1);assert.equal(after.jobs,1);assert.equal(after.reviews,1);assert.equal(after.subscriptions,before.subscriptions);assert.equal(new Set(after.operations).size,1);assert.equal(requests.filter(r=>r.path==='/api/v1/acquisitions/selected-movie'&&r.method==='POST').length,0);await privacy();
 }
 assert.deepEqual(errors,[]);console.log('M5_DISCOVERY_'+mode.toUpperCase()+'_PASS '+JSON.stringify({actualChrome:true,realCookieAppPG:true,nativeTMDB:true,nativeTorznab:true,ephemeralCDP:port,mode,acceptedMeansDurableOnly:true}));
} finally {await context.close();}
`
