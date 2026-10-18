import assert from 'node:assert/strict';
import {chromium} from 'playwright-core';
import {join} from 'node:path';

const base=process.env.PULSE_ENDPOINT_UI_BASE;
const session=process.env.PULSE_ENDPOINT_UI_SESSION;
const cdp=process.env.PULSE_ENDPOINT_UI_CDP;
const targets=JSON.parse(process.env.PULSE_ENDPOINT_UI_TARGETS??'[]');
assert(base&&session&&cdp&&targets.length===3,'owned endpoint fixture required');
const browser=await chromium.connectOverCDP(cdp);
const context=await browser.newContext({viewport:{width:1280,height:900}});
try {
 await context.addCookies([{name:'pulsemedia_session',value:session,url:base,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();
 page.setDefaultTimeout(15000);
 const errors=[];
 page.on('pageerror',e=>errors.push(String(e)));
 for(const target of targets){
  const api=base+'/api/v1/'+target.path;
  const initial=page.waitForResponse(response=>response.url()===api&&response.request().method()==='GET');
  await page.goto(base+(target.path==='indexers'?'/sites':'/settings'));
  const response=await initial;
  assert.equal(response.status(),200);
  const body=await response.json();
  assert(!JSON.stringify(body).includes('fixture-query-secret'),'endpoint response leaked secret query');
  assert(!JSON.stringify(body).includes('legacy-user'),'endpoint response leaked userinfo');
  const selected=body.items.find(item=>item.id===target.id);
  assert(selected,'endpoint missing');
  if(target.path!=='indexers')await page.getByRole('button',{name:target.path==='download-clients'?'下载器配置':'媒体服务器',exact:true}).click();
  const row=page.locator(target.path==='indexers'?'.indexer-row':'.integration-row').filter({has:page.getByText(target.name,{exact:true})});
  await row.getByRole('button',{name:'编辑',exact:true}).click();
  const form=target.path==='indexers'?page.locator('form').filter({has:page.locator('#site-name')}):page.locator('form:visible');
  const url=form.locator('input[type="url"]');
  assert.equal(await url.inputValue(),selected.url,'edit did not use safe endpoint projection');
  const name=target.path==='indexers'?form.locator('#site-name'):form.locator('input').first();
  await name.fill(target.name+' edited');
  const invalid=await form.evaluate(element=>Array.from(element.elements).filter(input=>input.willValidate&&!input.validity.valid).map(input=>({id:input.id,type:input.type,reason:input.validationMessage})));
  assert.deepEqual(invalid,[],`invalid ${target.path} edit fields`);
  const [request,result]=await Promise.all([
   page.waitForRequest(request=>request.url()===api+'/'+target.id&&request.method()==='PUT'),
   page.waitForResponse(response=>response.url()===api+'/'+target.id&&response.request().method()==='PUT'),
   form.getByRole('button',{name:/保存/}).click(),
  ]);
  const command=request.postDataJSON();
  assert.equal(command.url,selected.url);
  if(target.path==='indexers'){
   assert.equal(typeof selected.config_version,'number');
   assert.equal(command.expected_version,selected.config_version,'selected indexer version lost');
  }else{
   assert.equal(typeof selected.updated_at,'string');
   assert.equal(command.expected_updated_at,selected.updated_at,'selected endpoint version lost');
  }
  assert.equal(result.status(),200,`safe ${target.path} edit refused`);
  assert(!JSON.stringify(await result.json()).includes('fixture-query-secret'),'updated endpoint leaked original secret');
  await page.getByText(target.name+' edited',{exact:true}).first().waitFor();
  const shot=process.env.PULSE_DASHBOARD_ARTIFACT_DIR;
  if(shot){
   await page.setViewportSize({width:390,height:844});
   await page.evaluate(()=>Promise.all(document.getAnimations().filter(a=>a.effect?.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{}))));
   const layout=await page.evaluate(()=>({width:innerWidth,content:document.documentElement.scrollWidth,buttons:[...document.querySelectorAll('.settings-container button:not(.nav-tab-btn)')].map(button=>({height:button.getBoundingClientRect().height,clipped:button.scrollWidth>button.clientWidth}))}));
   assert(layout.content<=layout.width+1,`endpoint page overflows viewport: ${JSON.stringify(layout)}`);
   assert(layout.buttons.every(button=>button.height<45&&!button.clipped),'endpoint actions wrap vertically or clip');
   await page.screenshot({path:join(shot,`endpoint-${target.path}-mobile.png`),fullPage:true});
   await page.setViewportSize({width:1280,height:900});
  }
 }
 assert.deepEqual(errors,[]);
 console.log(JSON.stringify({result:'ENDPOINT_SAFE_EDIT_BROWSER_PASSED',endpoints:3,versions_preserved:true,hidden_credentials_preserved:true,page_errors:0}));
}finally{
 await context.close();
 await browser.close();
}
