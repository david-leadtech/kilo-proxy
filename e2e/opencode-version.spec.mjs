import {test,expect,startProxy} from './fixture.mjs';
import {readFile,writeFile} from 'node:fs/promises';

const headers=gateway=>({Authorization:'Bearer '+gateway.token});
async function getAPI(request,gateway,path) {const response=await request.get(new URL('/api/'+path,gateway.url).href,{headers:headers(gateway)});expect(response.ok(),await response.text()).toBe(true);return response.json();}
async function records(gateway) {try{return JSON.parse(await readFile(gateway.launchRecords,'utf8'));}catch(error){if(error.code==='ENOENT')return [];throw error;}}
async function setup(request,gateway,control={openCode2:true}) {
 await writeFile(gateway.launchControl,JSON.stringify(control));
 const current=await getAPI(request,gateway,'model-library');
 const library={schemaVersion:1,defaultModel:'vendor/one',models:[{id:'vendor/one',displayName:'Shared coding model',contextPreset:'custom',contextWindow:64000},{id:'anthropic/claude-sonnet-4.6',displayName:'Shared Claude',contextPreset:'custom',contextWindow:128000}]};
 const response=await request.put(new URL('/api/model-library',gateway.url).href,{headers:headers(gateway),data:{revision:current.revision,library}});expect(response.ok(),await response.text()).toBe(true);
 return library;
}

test('OpenCode persists one selected version and launches 2 in a separate profile while keeping 1',async({page,gateway,request},testInfo)=>{
 const library=await setup(request,gateway);await startProxy(page,gateway);await page.locator('#tab-opencode').click();
 await expect(page.locator('#opencode-version')).toHaveValue('1');await expect(page.locator('#opencode-version')).toBeEnabled();
 await page.locator('[data-editor-id="vendor/one"]').check();await page.locator('#editor-save').click();
 await expect(page.locator('#editor-status')).toContainText('Configuration saved');
 const first=await getAPI(request,gateway,'editors/opencode/profile'),original=await readFile(first.configPath,'utf8');
 await page.locator('#opencode-version').selectOption('2');
 await expect(page.locator('#opencode2-helper')).toBeVisible();await expect(page.locator('#editor-helper')).toBeHidden();
 await expect(page.locator('#opencode2-summary')).toHaveText('2 saved shared models');
 await expect(page.locator('#opencode2-models li')).toHaveText(['Shared coding model · vendor/one','Shared Claude · anthropic/claude-sonnet-4.6']);
 await expect(page.locator('#opencode2-install')).toHaveAttribute('href','https://dev.opencode.ai/v2/docs/');
 await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();
 await expect.poll(async()=>(await records(gateway)).length).toBe(1);expect((await records(gateway))[0].client).toBe('opencode2');
 const second=await getAPI(request,gateway,'clients/opencode2');expect(second.prepared).toBe(true);expect(second.library).toEqual((await getAPI(request,gateway,'model-library')).library);expect(second.configPath).not.toBe(first.configPath);
 expect(await readFile(first.configPath,'utf8')).toBe(original);
 await page.locator('#opencode-version-helper').screenshot({path:testInfo.outputPath('opencode-version-en-wide.png')});
 await page.reload();await page.locator('#tab-opencode').click();await expect(page.locator('#opencode-version')).toHaveValue('2');await expect(page.locator('#opencode2-helper')).toBeVisible();
 await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});
 await expect(page.locator('#opencode-version-help')).toContainText('Se conservan las cuentas y chats de OpenCode 1');
 await expect(page.locator('#opencode2-models li')).toHaveCount(library.models.length);expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 await page.locator('#opencode-version-helper').screenshot({path:testInfo.outputPath('opencode-version-es-mobile.png')});
 await page.locator('#opencode2-helper').screenshot({path:testInfo.outputPath('opencode2-es-mobile.png')});
 await page.locator('#opencode-version').selectOption('1');await expect(page.locator('#editor-helper')).toBeVisible();await expect(page.locator('#opencode2-helper')).toBeHidden();
 await page.locator('#editor-load').click();await expect(page.locator('[data-editor-id="vendor/one"]')).toBeChecked();
 expect((await getAPI(request,gateway,'clients/opencode/preferences')).major).toBe(1);expect(await readFile(first.configPath,'utf8')).toBe(original);
});

test('OpenCode version save keeps a failed choice pending and retries the exact version before preparing',async({page,gateway,request})=>{
 await setup(request,gateway);await startProxy(page,gateway);await page.locator('#tab-opencode').click();await expect(page.locator('#opencode-version')).toBeEnabled();
 let release,failed=true;const gate=new Promise(resolve=>{release=resolve}),writes=[];
 await page.route('**/api/clients/opencode/preferences',async route=>{
  if(route.request().method()!=='POST')return route.continue();writes.push(route.request().postDataJSON());
  if(failed){await gate;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:{message:'Synthetic version unavailable',type:'kilo_local_error'}})});}else await route.continue();
 });
 try {
  await page.locator('#opencode-version').selectOption('2');await expect(page.locator('#opencode-version')).toBeDisabled();await expect(page.locator('#client-launch')).toBeDisabled();
  expect((await getAPI(request,gateway,'clients/opencode/preferences')).major).toBe(1);release();
  await expect(page.locator('#opencode-version-status')).toContainText('Synthetic version unavailable');await expect(page.locator('#opencode-version')).toHaveValue('1');await expect(page.locator('#opencode-version-retry')).toBeVisible();
  failed=false;await page.locator('#opencode-version-retry').click();await expect(page.locator('#opencode-version')).toHaveValue('2');await expect(page.locator('#opencode-version')).toBeEnabled();
  expect(writes).toEqual([{version:1,major:2,executable1:'',executable2:''},{version:1,major:2,executable1:'',executable2:''}]);expect(await records(gateway)).toEqual([]);
 }finally{release();}
});

test('OpenCode rejects an externally changed version and a version 1 CLI selected as 2 without launching',async({page,gateway,request})=>{
 await setup(request,gateway,{openCode2:true,openCode2Version:'1.19.0'});await startProxy(page,gateway);await page.locator('#tab-opencode').click();await expect(page.locator('#opencode-version')).toBeEnabled();
 await page.locator('#opencode-version').selectOption('2');await expect(page.locator('#opencode2-prepare')).toBeEnabled();
 await page.locator('#opencode2-prepare').click();await expect(page.locator('#notice')).toContainText('OpenCode 2.0.18');expect(await records(gateway)).toEqual([]);
 const changed=await request.post(new URL('/api/clients/opencode/preferences',gateway.url).href,{headers:headers(gateway),data:{version:1,major:1}});expect(changed.ok(),await changed.text()).toBe(true);
 let prepares=0;page.on('request',request=>{if(request.method()==='POST'&&new URL(request.url()).pathname==='/api/clients/opencode2')prepares++;});
 await page.locator('#opencode2-prepare').click();await expect(page.locator('#notice')).toContainText('version or models changed');await expect(page.locator('#opencode-version')).toHaveValue('1');expect(prepares).toBe(0);expect(await records(gateway)).toEqual([]);
});

test('OpenCode executable paths persist independently and the selected path reaches version 2',async({page,gateway,request},testInfo)=>{
 await setup(request,gateway);await startProxy(page,gateway);await page.locator('#tab-opencode').click();await expect(page.locator('#opencode-executable')).toBeEnabled();
 await page.locator('#opencode-executable').fill(gateway.openCodeExecutable);await page.locator('#opencode-executable-save').click();await expect(page.locator('#opencode-executable')).toBeEnabled();
 await page.locator('#opencode-version').selectOption('2');await expect(page.locator('#opencode-executable')).toBeEnabled();await page.locator('#opencode-executable').fill(gateway.openCode2Executable);await page.locator('#opencode-executable-save').click();await expect(page.locator('#opencode-executable')).toBeEnabled();
 expect(await getAPI(request,gateway,'clients/opencode/preferences')).toEqual({version:1,major:2,executable1:gateway.openCodeExecutable,executable2:gateway.openCode2Executable});await expect(page.locator('#opencode-executable-resolved')).toContainText(gateway.openCode2Executable);
 await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();await expect.poll(async()=>(await records(gateway)).length).toBe(1);expect((await records(gateway))[0]).toMatchObject({client:'opencode2',executable:gateway.openCode2Executable});
 await expect(page.locator('#toast')).toBeHidden();await page.locator('#opencode-version-helper').screenshot({path:testInfo.outputPath('opencode-path-en.png')});await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});await expect(page.locator('#opencode-executable-help')).toContainText('T3 Code usa las mismas rutas');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.locator('#opencode-version-helper').screenshot({path:testInfo.outputPath('opencode-path-es-mobile.png')});
 await page.locator('#opencode-executable-auto').click();await expect(page.locator('#opencode-executable')).toHaveValue('');const after=await getAPI(request,gateway,'clients/opencode/preferences');expect(after.executable1).toBe(gateway.openCodeExecutable);expect(after.executable2).toBe('');
});
