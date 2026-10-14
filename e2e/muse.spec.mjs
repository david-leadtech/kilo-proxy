import {test,expect,startProxy} from './fixture.mjs';

const headers=gateway=>({Authorization:'Bearer '+gateway.token});
async function configure(page,request,gateway){
 const response=await request.post(new URL('/api/clients/cli/preferences',gateway.url).href,{headers:headers(gateway),data:{version:1,museExecutable:gateway.codexExecutable}});
 expect(response.ok(),await response.text()).toBe(true);
 let preferences={version:1,connection:'kilo'},currentLibrary={schemaVersion:1,defaultModel:'',models:[]};
 const writes=[],launches=[];
 await page.route('**/api/clients/muse**',async route=>{
  const path=new URL(route.request().url()).pathname,body=route.request().method()==='POST'?route.request().postDataJSON():null;
  if(path.endsWith('/preferences')){if(body){writes.push({path,body});preferences=body;}return route.fulfill({json:{preferences}});}
  if(body){writes.push({path,body});if(body.library)currentLibrary=body.library;}
  return route.fulfill({json:{preferences,library:currentLibrary,resolvedExecutable:gateway.codexExecutable,prepared:true,kiloSupported:true,effectiveModels:currentLibrary.models.map(model=>({id:model.id,name:model.displayName||model.id}))}});
 });
 await page.route('**/api/clients/launch',async route=>{
  if(route.request().method()!=='POST')return route.continue();
  const body=route.request().postDataJSON();if(body.client!=='muse')return route.continue();launches.push(body);return route.fulfill({json:{ok:true,message:'Synthetic Muse process boundary confirmed'}});
 });
 return {writes,launches,setLibrary:value=>currentLibrary=value};
}

test('Muse gear distinguishes Kilo Models from native Meta and preserves the selected executable',async({page,request,gateway},testInfo)=>{
 const mock=await configure(page,request,gateway);await page.reload();await page.locator('#tab-muse').click();
 await expect(page.locator('#muse-connection')).toHaveValue('kilo');await expect(page.locator('#muse-account')).toHaveText('Kilo Proxy · Muse Code');await expect(page.locator('#client-launch')).toBeDisabled();await expect(page.locator('#client-launch-status')).toContainText('Connect Kilo or ChatGPT');
 await page.locator('#muse-connection').selectOption('native');await expect(page.locator('#muse-connection')).toBeEnabled();await expect(page.locator('#client-launch')).toBeEnabled();await expect(page.locator('#muse-account-help')).toContainText('Use /login in Muse');await expect(page.locator('#muse-account-help')).toContainText('Kilo login does not sign in to Meta');
 await page.locator('#client-launch').click();await expect.poll(()=>mock.launches.length).toBe(1);expect(mock.launches[0]).toMatchObject({client:'muse',museConnection:'native',museExecutable:gateway.codexExecutable});expect(mock.writes.find(row=>row.path==='/api/clients/muse').body).not.toHaveProperty('library');
 await startProxy(page,gateway);
 const library={schemaVersion:1,defaultModel:'vendor/one',models:[{id:'vendor/one',displayName:'Exact shared model',contextPreset:'custom',contextWindow:128000}]};
 const existing=await request.get(new URL('/api/model-library',gateway.url).href,{headers:headers(gateway)});const saved=await existing.json();const written=await request.put(new URL('/api/model-library',gateway.url).href,{headers:headers(gateway),data:{revision:saved.revision,library}});expect(written.ok(),await written.text()).toBe(true);
 const currentResponse=await request.get(new URL('/api/model-library',gateway.url).href,{headers:headers(gateway)});const current=await currentResponse.json();mock.setLibrary(current.library);await page.locator('#muse-connection').selectOption('kilo');await expect(page.locator('#muse-connection')).toBeEnabled();await page.locator('#muse-reload').click();await expect(page.locator('#client-launch')).toBeEnabled();await expect(page.locator('#muse-models')).toContainText('Exact shared model · vendor/one');
 await page.locator('#client-launch').click();await expect.poll(()=>mock.launches.length).toBe(2);expect(mock.launches[1]).toMatchObject({client:'muse',museConnection:'kilo',museExecutable:gateway.codexExecutable});expect(mock.writes.filter(row=>row.path==='/api/clients/muse').at(-1).body.library).toEqual(current.library);
 await expect(page.locator('#toast')).toBeHidden();
 for(const [language,width,height] of [['en',1180,1000],['es',780,1000]]){
  await page.locator('#language').selectOption(language);await page.setViewportSize({width,height});await expect(page.locator('#muse-terminal-help')).toContainText(language==='en'?'Keep Kilo Proxy open':'Mantén Kilo Proxy abierto');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.locator('#muse-helper').screenshot({path:testInfo.outputPath(`muse-kilo-${language}-${width}.png`)});
 }
});

test('Muse connection save stays pending and stale choices cannot reach launch',async({page,request,gateway})=>{
 await configure(page,request,gateway);await page.reload();await page.locator('#tab-muse').click();
 let release;const held=new Promise(resolve=>release=resolve);
 await page.route('**/api/clients/muse/preferences',async route=>{
  if(route.request().method()!=='POST')return route.fallback();await held;return route.fulfill({status:503,json:{error:{message:'Synthetic connection save unavailable'}}});
 });
 try{await page.locator('#muse-connection').selectOption('native');await expect(page.locator('#muse-connection')).toBeDisabled();await expect(page.locator('#client-launch')).toBeDisabled();release();await expect(page.locator('#muse-retry')).toBeVisible();await expect(page.locator('#muse-status')).toContainText('Synthetic connection save unavailable');await expect(page.locator('#client-launch')).toBeDisabled();}finally{release();}
});
