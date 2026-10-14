import {test, expect, startProxy} from './fixture.mjs';
import {readFile, writeFile} from 'node:fs/promises';

const library=()=>({schemaVersion:1,defaultModel:'anthropic/claude-sonnet-4.6',models:[
 {id:'vendor/one',displayName:'My coding model',contextPreset:'custom',contextWindow:128000,reasoningEffort:'high',reasoningCustom:true,reasoningLevels:['low','high']},
 {id:'anthropic/claude-sonnet-4.6',displayName:'My default model',contextPreset:'custom',contextWindow:128000,reasoningCustom:true,reasoningLevels:[]},
]});
const headers=gateway=>({Authorization:'Bearer '+gateway.token});
async function getAPI(request,gateway,path) {
 const response=await request.get(new URL('/api/'+path,gateway.url).href,{headers:headers(gateway)});
 expect(response.ok(),await response.text()).toBe(true);return response.json();
}
async function saveLibrary(request,gateway,value) {
 const current=await getAPI(request,gateway,'model-library');
 const response=await request.put(new URL('/api/model-library',gateway.url).href,{headers:headers(gateway),data:{revision:current.revision,library:value}});
 expect(response.ok(),await response.text()).toBe(true);
}
const preferences=()=>({version:1,codex:true,claude:true,kiloCodex:true,kiloClaude:true,codexModels:'shared',claudeModels:'shared',providers:[],openCode2:false,openCodeConnection:'native'});
async function savePreferences(request,gateway,value=preferences()) {
 const response=await request.post(new URL('/api/clients/synara/preferences',gateway.url).href,{headers:headers(gateway),data:value});expect(response.ok(),await response.text()).toBe(true);return response.json();
}
async function records(gateway) {try{return JSON.parse(await readFile(gateway.launchRecords,'utf8'));}catch(error){if(error.code==='ENOENT')return [];throw error;}}

test('Synara prepares the four saved accounts before desktop launch and guards a second opening',async({page,gateway,request},testInfo)=>{
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);
 await startProxy(page,gateway);
 await page.locator('#tab-synara').click();
 await expect(page.locator('#synara-summary')).toContainText('Kilo Proxy · Codex: 2');
 await expect(page.locator('#synara-models li')).toHaveText(['Kilo Proxy · Codex · My default model · '+library().defaultModel,'Kilo Proxy · Codex · My coding model · vendor/one','Kilo Proxy · Claude · My default model · '+library().defaultModel,'Kilo Proxy · Claude · My coding model · vendor/one']);
 await expect(page.locator('#synara-agents li')).toHaveCount(4);
 await expect(page.locator('#synara-agents li')).toHaveText([
  'Codex · Existing Codex CLI login',
  'Kilo Proxy · Codex · Models through the proxy',
  'Claude · Existing Claude Code login',
  'Kilo Proxy · Claude · Models through the proxy',
 ]);
 await expect(page.locator('#synara-intro')).toContainText('Kilo Proxy names and green account indicators');
 await expect(page.locator('#synara-agents')).toContainText('Existing Codex CLI login');
 await expect(page.locator('#synara-agents')).toContainText('Existing Claude Code login');
 await expect(page.locator('#client-launch-directory-field')).toBeHidden();
 await expect(page.locator('#client-launch-custom')).toBeHidden();
 await page.locator('#synara-settings-title').click();
 await expect(page.locator('#synara-account-codex')).toBeChecked();await expect(page.locator('#synara-account-claude')).toBeChecked();
 await expect(page.locator('#synara-codex-models')).toHaveValue('shared');await expect(page.locator('#synara-claude-models')).toHaveValue('shared');
 await expect(page.locator('#synara-helper')).not.toContainText('· Normal');await expect(page.locator('#synara-provider-muse')).toHaveCount(0);
 let prepares=0;
 page.on('request',request=>{if(request.method()==='POST'&&new URL(request.url()).pathname==='/api/clients/synara')prepares++;});
 await expect(page.locator('#client-launch')).toBeEnabled();
 await page.locator('#client-launch').click();
 await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 expect(prepares).toBe(1);
 await expect(page.locator('#client-launch')).toBeEnabled();
 await page.locator('#client-launch').click();
 await expect(page.locator('#client-launch-status')).toContainText('before opening it again');
 expect((await records(gateway)).length).toBe(1);
 expect(prepares).toBe(2);
 expect((await records(gateway)).every(record=>record.client==='synara'&&record.kind==='desktop')).toBe(true);
 const profile=await getAPI(request,gateway,'clients/synara');
 expect(profile.prepared).toBe(true);
 expect(profile.version).toBe('1.0.0-beta.1');
 expect(profile.library).toEqual((await getAPI(request,gateway,'model-library')).library);
 await page.locator('#synara-options').click();
 await expect(page.locator('#synara-prepared')).toContainText('saved Synara accounts and models are prepared');
 await expect(page.locator('#synara-compatibility')).toContainText('new chat to switch between native and Kilo accounts');
 await expect(page.locator('#synara-compatibility')).toContainText('exact gateway model ID');
 await expect(page.locator('#synara-compatibility')).toContainText('family and version match one prepared model');
 await expect(page.locator('#synara-compatibility')).toContainText('reasoning defaults come from Kilo Models');
 await expect(page.locator('#synara-compatibility')).not.toContainText('hides');
 await expect(page.locator('#synara-compatibility')).toContainText('check the selected model before continuing');
 await expect(page.locator('#synara-privacy')).toContainText('regular Synara workspace and chats stay separate');
 await expect(page.locator('#synara-requirements')).toContainText('Supports official Synara stable and Beta releases. Preparation checks the installed contract and CLIs required by your enabled accounts.');
 await expect(page.locator('#synara-requirements')).toContainText('Detected: '+profile.version);
 await expect(page.locator('#synara-requirements')).toContainText('not Desktop app logins');
 await expect(page.locator('#synara-install')).toHaveAttribute('href','https://github.com/Emanuele-web04/synara/releases');
 await expect(page.locator('#synara-helper')).not.toContainText('synthetic-kilo-personal-key');
 await expect(page.locator('#synara-helper')).not.toContainText(profile.profileDir);
 await expect(page.locator('#toast')).toBeHidden();
 await page.locator('#synara-helper').screenshot({path:testInfo.outputPath('synara-en-wide.png')});
 await page.locator('#language').selectOption('es');
 await page.setViewportSize({width:390,height:844});
 await expect(page.locator('#synara-agents')).toContainText('Sesión existente de Codex CLI');
 await expect(page.locator('#synara-agents li').nth(1)).toContainText('Kilo Proxy · Codex');
 await expect(page.locator('#synara-agents li').nth(3)).toContainText('Kilo Proxy · Claude');
 await expect(page.locator('#synara-intro')).toContainText('indicadores verdes');
 await expect(page.locator('#synara-requirements')).toContainText('Admite versiones oficiales estables y Beta de Synara. La preparación comprueba el contrato instalado y los CLI que requieren las cuentas activadas.');
 await expect(page.locator('#synara-requirements')).toContainText('Detectado: '+profile.version);
 await expect(page.locator('#synara-compatibility')).toContainText('inicia uno nuevo para cambiar entre cuentas nativas y Kilo');
 await expect(page.locator('#synara-compatibility')).toContainText('revisa el modelo elegido antes de continuar');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 await page.locator('#synara-helper').screenshot({path:testInfo.outputPath('synara-es-mobile.png')});
 expect(await page.evaluate(()=>window.__copied)).toEqual([]);
});

test('Synara explains missing models, connection and installation without launching',async({page,gateway,request})=>{
 await page.locator('#tab-synara').click();
 await expect(page.locator('#client-launch')).toBeDisabled();
 await expect(page.locator('#client-launch-status')).toContainText('Connect your selected provider first.');
 await startProxy(page,gateway);
 await expect(page.locator('#client-launch-status')).toContainText('Add and save models');
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);
 await writeFile(gateway.launchControl,JSON.stringify({unavailable:'synara'}));
 await page.locator('#client-launch-refresh').click();
 await expect(page.locator('#client-launch')).toBeDisabled();
 await expect(page.locator('#client-launch-status')).toContainText('Synthetic application unavailable');
 await page.locator('#synara-options').click();
 await expect(page.locator('#synara-install')).toBeVisible();
 expect(await records(gateway)).toEqual([]);
 await writeFile(gateway.launchControl,'{}');
 await page.locator('#client-launch-refresh').click();
 await expect(page.locator('#client-launch')).toBeEnabled();
});

test('Synara stops stale preparation and refreshes an externally changed library',async({page,gateway,request})=>{
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-synara').click();await expect(page.locator('#client-launch')).toBeEnabled();
 let release,entered=false;const gate=new Promise(resolve=>{release=resolve;});
 await page.route('**/api/clients/synara',async route=>{if(route.request().method()==='POST'){entered=true;await gate;}await route.continue();});
 try {
  await page.locator('#client-launch').click();await expect.poll(()=>entered).toBe(true);
  const changed=library();changed.defaultModel='vendor/one';await saveLibrary(request,gateway,changed);release();
  await expect(page.locator('#client-launch-status')).toContainText('shared models changed while preparing');
  expect(await records(gateway)).toEqual([]);
  await expect.poll(async()=>(await getAPI(request,gateway,'model-library')).library.defaultModel).toBe('vendor/one');
  await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();
  await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 }finally{release();}
});

test('Synara refuses changing a running private workspace and recovers after quit',async({page,gateway,request})=>{
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-synara').click();await page.locator('#synara-options').click();
 await expect(page.locator('#synara-prepare')).toBeEnabled();
 await writeFile(gateway.launchControl,JSON.stringify({synaraRunning:true}));
 await page.locator('#synara-prepare').click();
 await expect(page.locator('#synara-status')).toContainText(/(Quit|Close).*Synara/i);
 expect(await records(gateway)).toEqual([]);
 await writeFile(gateway.launchControl,'{}');
 await page.locator('#synara-prepare').click();
 await expect(page.locator('#synara-prepared')).toContainText('saved Synara accounts and models are prepared');
 expect(await records(gateway)).toEqual([]);
});


test('Synara account controls save exact policy in its panel and own accounts do not require Kilo Models or gateway',async({page,gateway,request},testInfo)=>{
 const own={...preferences(),claude:false,kiloCodex:false,kiloClaude:false};await savePreferences(request,gateway,own);
 await page.locator('#tab-synara').click();await page.locator('#synara-settings-title').click();
 await expect(page.locator('#synara-account-codex')).toBeChecked();await expect(page.locator('#synara-account-kiloCodex')).not.toBeChecked();
 await expect(page.locator('#client-launch')).toBeEnabled();await expect(page.locator('#synara-prepare')).toBeEnabled();
 await page.locator('#client-launch').click();await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 const prepared=await getAPI(request,gateway,'clients/synara');expect(prepared.preferences).toEqual(own);expect(prepared.library.models).toEqual([]);
 await page.locator('#synara-account-kiloCodex').check();await expect.poll(async()=>(await getAPI(request,gateway,'clients/synara/preferences')).preferences.kiloCodex).toBe(true);
 await expect(page.locator('#client-launch')).toBeDisabled();await expect(page.locator('#client-launch-status')).toContainText('Connect your selected provider');
 expect((await records(gateway)).length).toBe(1);
 await page.setViewportSize({width:1120,height:1000});await page.locator('#synara-settings').screenshot({path:testInfo.outputPath('synara-accounts-en-1120.png')});
 await page.locator('#language').selectOption('es');await page.setViewportSize({width:780,height:1000});
 await expect(page.locator('#synara-settings-title')).toHaveText('Cuentas y modelos');await expect(page.locator('#synara-settings-help')).toContainText('se conservan tus chats');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);await page.locator('#synara-settings').screenshot({path:testInfo.outputPath('synara-accounts-es-780.png')});
});

test('Synara pending and failed preferences cannot prepare or open until an exact retry confirms them',async({page,gateway,request})=>{
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);await startProxy(page,gateway);await page.locator('#tab-synara').click();await page.locator('#synara-settings-title').click();
 let release,entered=false,fail=true;const gate=new Promise(resolve=>release=resolve),posts=[];
 await page.route('**/api/clients/synara/preferences',async route=>{
  if(route.request().method()!=='POST')return route.continue();posts.push(route.request().postDataJSON());entered=true;await gate;
  if(fail)return route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:{message:'Synthetic preference save unavailable'}})});return route.continue();
 });
 try{
  await page.locator('#synara-account-claude').uncheck();await expect.poll(()=>entered).toBe(true);await expect(page.locator('#client-launch')).toBeDisabled();await expect(page.locator('#synara-account-codex')).toBeDisabled();
  release();await expect(page.locator('#synara-settings-status')).toContainText('Synthetic preference save unavailable');expect((await getAPI(request,gateway,'clients/synara/preferences')).preferences.claude).toBe(true);
  await expect(page.locator('#synara-prepare')).toBeDisabled();expect(await records(gateway)).toEqual([]);
  fail=false;await page.locator('#synara-settings-retry').click();await expect.poll(async()=>(await getAPI(request,gateway,'clients/synara/preferences')).preferences.claude).toBe(false);
  expect(posts).toHaveLength(2);expect(posts[1]).toEqual(posts[0]);await expect(page.locator('#client-launch')).toBeEnabled();await expect(page.locator('#synara-agents li')).toHaveCount(3);
 }finally{release();}
});


test('Synara rejects missing Claude picker capability and permits choosing the native Claude account',async({page,gateway,request})=>{
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);await startProxy(page,gateway);
 await page.route('**/api/clients/synara/preferences',async route=>{const response=await route.fetch();const data=await response.json();delete data.claudePickerSupported;await route.fulfill({response,json:data});});
 await page.locator('#tab-synara').click();await page.locator('#synara-settings-title').click();
 await expect(page.locator('#client-launch')).toBeDisabled();await expect(page.locator('#synara-prepare')).toBeDisabled();await expect(page.locator('#client-launch-status')).toContainText('cannot restrict the Kilo Claude picker');
 await page.locator('#synara-account-kiloClaude').uncheck();await expect.poll(async()=>(await getAPI(request,gateway,'clients/synara/preferences')).preferences.kiloClaude).toBe(false);
 await expect(page.locator('#synara-account-claude')).toBeChecked();await expect(page.locator('#synara-account-kiloCodex')).toBeChecked();await expect(page.locator('#client-launch')).toBeEnabled();expect(await records(gateway)).toEqual([]);
});


test('Synara saved account recovery replaces an older installation detection before allowing opening',async({page,gateway,request})=>{
 await saveLibrary(request,gateway,library());await savePreferences(request,gateway);await startProxy(page,gateway);
 let release,entered=false,reads=0;const gate=new Promise(resolve=>release=resolve);
 await page.route('**/api/clients/launch',async route=>{
  if(route.request().method()!=='GET')return route.continue();const response=await route.fetch();const data=await response.json();
  if(++reads===1){data.clients.synara.available=false;data.clients.synara.reason='Synthetic previous account unavailable';entered=true;await gate;}
  await route.fulfill({response,json:data});
 });
 try{
  await page.locator('#tab-synara').click();await expect.poll(()=>entered).toBe(true);await page.locator('#synara-settings-title').click();await expect(page.locator('#synara-account-kiloClaude')).toBeEnabled();
  await page.locator('#synara-account-kiloClaude').uncheck();await expect.poll(async()=>(await getAPI(request,gateway,'clients/synara/preferences')).preferences.kiloClaude).toBe(false);await expect(page.locator('#client-launch')).toBeDisabled();
  release();await expect.poll(()=>reads).toBe(2);await expect(page.locator('#client-launch')).toBeEnabled();await expect(page.locator('#client-launch-status')).not.toContainText('Synthetic previous account unavailable');expect(await records(gateway)).toEqual([]);
 }finally{release();}
});
