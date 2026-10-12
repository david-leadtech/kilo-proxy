import {test, expect, startProxy} from './fixture.mjs';
import {readFile, writeFile} from 'node:fs/promises';

const library=()=>({schemaVersion:1,defaultModel:'openai/gpt-fixture',models:[
 {id:'openai/gpt-fixture',displayName:'My GPT model',contextPreset:'custom',contextWindow:128000},
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
async function records(gateway) {try{return JSON.parse(await readFile(gateway.launchRecords,'utf8'));}catch(error){if(error.code==='ENOENT')return [];throw error;}}
async function setControl(gateway,changes={}) {let prior={};try{prior=JSON.parse(await readFile(gateway.launchControl,'utf8'));}catch(error){if(error.code!=='ENOENT')throw error;}await writeFile(gateway.launchControl,JSON.stringify({...prior,t3Models:true,...changes}));}
async function setupT3(request,gateway) {await setControl(gateway);await saveLibrary(request,gateway,library());}

test('T3 Code prepares chosen accounts before desktop launch and guards a second opening',async({page,gateway,request},testInfo)=>{
 await setupT3(request,gateway);
 await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();
 await expect(page.locator('#t3-code-summary')).toHaveText('Kilo Proxy · Codex: 1 · Kilo Proxy · Claude: 3');
 await expect(page.locator('#t3-code-models li')).toHaveText(['Kilo Proxy · Codex · My GPT model · openai/gpt-fixture','Kilo Proxy · Claude · Claude Opus 5.5 · anthropic/claude-opus-5.5','Kilo Proxy · Claude · Claude Sonnet 5.5 · anthropic/claude-sonnet-5.5','Kilo Proxy · Claude · Claude Fable 5.1 · anthropic/claude-fable-5.1']);
 await expect(page.locator('#t3-code-agents li')).toHaveCount(4);
 await expect(page.locator('#t3-code-agents li')).toHaveText([
  'Codex · Existing Codex CLI login',
  'Kilo Proxy · Codex · Models through the proxy',
  'Claude · Existing Claude Code login',
  'Kilo Proxy · Claude · Models through the proxy',
 ]);
 await expect(page.locator('#t3-code-intro')).toContainText('Green KP badges on T3’s provider rail and composer');
 await expect(page.locator('#t3-code-agents')).toContainText('Existing Codex CLI login');
 await expect(page.locator('#t3-code-agents')).toContainText('Existing Claude Code login');
 await expect(page.locator('#client-launch-directory-field')).toBeHidden();
 await expect(page.locator('#client-launch-custom')).toBeHidden();
 await page.locator('#t3-code-settings-title').click();
 await expect(page.locator('#t3-code-account-codex')).toBeChecked();
 await expect(page.locator('#t3-code-account-claude')).toBeChecked();
 await expect(page.locator('#t3-code-codex-models')).toHaveValue('official');
 await expect(page.locator('#t3-code-claude-models')).toHaveValue('official');
 await expect(page.locator('#t3-code-opencode-version-field')).toBeHidden();
 let prepares=0;
 page.on('request',request=>{if(request.method()==='POST'&&new URL(request.url()).pathname==='/api/clients/t3-code')prepares++;});
 await expect(page.locator('#client-launch')).toBeEnabled();
 await page.locator('#client-launch').click();
 await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 expect(prepares).toBe(1);
 await expect(page.locator('#client-launch')).toBeEnabled();
 await page.locator('#client-launch').click();
 await expect(page.locator('#client-launch-status')).toContainText('before opening it again');
 expect((await records(gateway)).length).toBe(1);
 expect(prepares).toBe(2);
 expect((await records(gateway)).every(record=>record.client==='t3-code'&&record.kind==='desktop')).toBe(true);
 const profile=await getAPI(request,gateway,'clients/t3-code');
 expect(profile.prepared).toBe(true);
 expect(profile.version).toBe('0.0.45');
 expect(profile.library).toEqual((await getAPI(request,gateway,'model-library')).library);
 await page.locator('#t3-code-options').click();
 await expect(page.locator('#t3-code-prepared')).toContainText('Your saved T3 accounts and models are prepared.');
 await expect(page.locator('#t3-code-compatibility')).toContainText('new chat to switch accounts');
 await expect(page.locator('#t3-code-compatibility')).toContainText('native reasoning selector');
 await expect(page.locator('#t3-code-compatibility')).toContainText('Check the selected account and model before continuing');
 await expect(page.locator('#t3-code-privacy')).toContainText('regular T3 Code workspace and chats stay separate');
 await expect(page.locator('#t3-code-requirements')).toContainText('not Desktop app logins');
 await expect(page.locator('#t3-code-install')).toHaveAttribute('href','https://github.com/pingdotgg/t3code/releases');
 await expect(page.locator('#t3-code-helper')).not.toContainText('synthetic-kilo-personal-key');
 await expect(page.locator('#t3-code-helper')).not.toContainText(profile.profileDir);
 await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-code-en-wide.png')});
 await page.locator('#language').selectOption('es');
 await page.setViewportSize({width:390,height:844});
 await expect(page.locator('#t3-code-agents')).toContainText('Sesión existente de Codex CLI');
 await expect(page.locator('#t3-code-agents li').nth(1)).toContainText('Kilo Proxy · Codex');
 await expect(page.locator('#t3-code-agents li').nth(3)).toContainText('Kilo Proxy · Claude');
 await expect(page.locator('#t3-code-intro')).toContainText('marcas KP verdes');
 await expect(page.locator('#t3-code-compatibility')).toContainText('Inicia un chat nuevo para cambiar de cuenta');
 await expect(page.locator('#t3-code-compatibility')).toContainText('Revisa la cuenta y el modelo elegidos antes de continuar');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-code-es-mobile.png')});
 expect(await page.evaluate(()=>window.__copied)).toEqual([]);
});

test('T3 Code explains missing models, connection and installation without launching',async({page,gateway,request})=>{
 await setControl(gateway);
 await page.locator('#tab-t3-code').click();
 await expect(page.locator('#client-launch')).toBeDisabled();
 await expect(page.locator('#client-launch-status')).toContainText('Connect your selected provider first.');
 await startProxy(page,gateway);
 await expect(page.locator('#client-launch-status')).toContainText('Add and save models');
 await setupT3(request,gateway);
 await setControl(gateway,{unavailable:'t3-code'});
 await page.locator('#client-launch-refresh').click();
 await expect(page.locator('#client-launch')).toBeDisabled();
 await expect(page.locator('#client-launch-status')).toContainText('Synthetic application unavailable');
 await page.locator('#t3-code-options').click();
 await expect(page.locator('#t3-code-install')).toBeVisible();
 expect(await records(gateway)).toEqual([]);
 await setControl(gateway,{unavailable:'',t3CodeRunning:false});
 await page.locator('#client-launch-refresh').click();
 await expect(page.locator('#client-launch')).toBeEnabled();
});

test('T3 Code stops stale preparation and refreshes an externally changed library',async({page,gateway,request})=>{
 await setupT3(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await expect(page.locator('#client-launch')).toBeEnabled();
 let release,entered=false;const gate=new Promise(resolve=>{release=resolve;});
 await page.route('**/api/clients/t3-code',async route=>{if(route.request().method()==='POST'){entered=true;await gate;}await route.continue();});
 try {
  await page.locator('#client-launch').click();await expect.poll(()=>entered).toBe(true);
  const changed=library();changed.defaultModel='vendor/one';await saveLibrary(request,gateway,changed);release();
  await expect(page.locator('#client-launch-status')).toContainText('shared models changed while preparing');
  expect(await records(gateway)).toEqual([]);
  expect((await getAPI(request,gateway,'model-library')).library.defaultModel).toBe('vendor/one');
  await expect(page.locator('#t3-code-summary')).toContainText('Kilo Proxy · Claude: 3');
  await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();
  await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 }finally{release();}
});

test('T3 Code refuses changing a running private workspace and recovers after quit',async({page,gateway,request})=>{
 await setupT3(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await page.locator('#t3-code-options').click();
 await expect(page.locator('#t3-code-prepare')).toBeEnabled();
 await setControl(gateway,{t3CodeRunning:true});
 await page.locator('#t3-code-prepare').click();
 await expect(page.locator('#t3-code-status')).toContainText(/(Quit|Close).*T3 Code/i);
 expect(await records(gateway)).toEqual([]);
 await setControl(gateway,{unavailable:'',t3CodeRunning:false});
 await page.locator('#t3-code-prepare').click();
 await expect(page.locator('#t3-code-prepared')).toContainText('Your saved T3 accounts and models are prepared.');
 expect(await records(gateway)).toEqual([]);
});

test('T3 nightly prepares its actual version and explains V2 handoff in EN/ES',async({page,gateway,request},testInfo)=>{
 const version='0.0.46-nightly.20261003.2610';
 await setControl(gateway,{t3CodeVersion:version});
 await setupT3(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await page.locator('#t3-code-options').click();
 await expect(page.locator('#t3-code-requirements')).toContainText('Detected: '+version);
 await expect(page.locator('#t3-code-compatibility')).toContainText('change agents between turns');
 await expect(page.locator('#t3-code-compatibility')).toContainText('attachments are not transferred');
 await expect(page.locator('#t3-code-compatibility')).toContainText('native reasoning selector');
 await page.locator('#t3-code-prepare').click();
 await expect(page.locator('#t3-code-prepared')).toContainText('Your saved T3 accounts and models are prepared.');
 const profile=await getAPI(request,gateway,'clients/t3-code');
 expect(profile.prepared).toBe(true);expect(profile.version).toBe(version);
 await page.locator('#client-launch').click();
 await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-nightly-en.png')});
 await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});
 await expect(page.locator('#t3-code-requirements')).toContainText('Detectado: '+version);
 await expect(page.locator('#t3-code-compatibility')).toContainText('cambiar de agente entre turnos');
 await expect(page.locator('#t3-code-compatibility')).toContainText('Revisa la cuenta y el modelo elegidos antes de continuar un chat antiguo');
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-nightly-es-mobile.png')});
});

test('T3 settings save account and model choices, recover a failed write, and require preparation',async({page,gateway,request},testInfo)=>{
 await setupT3(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await page.locator('#t3-code-settings-title').click();
 await expect(page.locator('#t3-code-account-codex')).toBeEnabled();
 let writes=0,release;const gate=new Promise(resolve=>{release=resolve;});
 await page.route('**/api/clients/t3-code/preferences',async route=>{
  if(route.request().method()==='POST'&&++writes===1){await gate;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:{message:'Synthetic settings unavailable',type:'kilo_local_error'}})});return;}
  await route.continue();
 });
 try {
  await page.locator('#t3-code-account-codex').uncheck();
  await expect.poll(()=>writes).toBe(1);
  await expect(page.locator('#t3-code-account-claude')).toBeDisabled();
  await expect(page.locator('#client-launch')).toBeDisabled();
  release();
  await expect(page.locator('#t3-code-settings-status')).toContainText('Synthetic settings unavailable');
  await expect(page.locator('#t3-code-account-codex')).toBeChecked();
  await expect(page.locator('#t3-code-settings-retry')).toBeVisible();
  await page.locator('#t3-code-settings-retry').click();
  await expect(page.locator('#t3-code-account-codex')).not.toBeChecked();
  await expect(page.locator('#t3-code-account-claude')).toBeEnabled();
  expect(writes).toBe(2);
  await expect(page.locator('#t3-code-agents li')).toHaveCount(3);
  await expect(page.locator('#t3-code-settings-status')).toContainText('Close T3 Code · Kilo and prepare again');
  await page.locator('#t3-code-codex-models').selectOption('shared');
  await expect(page.locator('#t3-code-summary')).toContainText('Kilo Proxy · Codex: 3');
  await expect(page.locator('#t3-code-claude-models')).toBeEnabled();
  const saved=await getAPI(request,gateway,'clients/t3-code/preferences');
  expect(saved.preferences.codex).toBe(false);expect(saved.preferences.claude).toBe(true);
  expect(saved.preferences.codexModels).toBe('shared');expect(saved.preferences.claudeModels).toBe('official');
  await page.locator('#t3-code-options').click();await page.locator('#t3-code-prepare').click();
  await expect(page.locator('#t3-code-prepared')).toHaveText('Your saved T3 accounts and models are prepared.');
  const profile=await getAPI(request,gateway,'clients/t3-code');expect(profile.preferences).toEqual(saved.preferences);
  expect(profile.effectiveClaudeModels.map(model=>model.id)).toEqual(['anthropic/claude-opus-5.5','anthropic/claude-sonnet-5.5','anthropic/claude-fable-5.1']);
  expect(await records(gateway)).toEqual([]);
  await expect(page.locator('#toast')).toBeHidden();
  await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-settings-en.png')});
  await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});
  await expect(page.locator('#t3-code-settings-title')).toHaveText('Ajustes de T3 Code');
  await expect(page.locator('#t3-code-codex-models-label')).toHaveText('Modelos de Codex Kilo');
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
  await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-settings-es-mobile.png')});
 }finally{release();}
});

test('T3 refuses externally changed account settings before writing a profile',async({page,gateway,request})=>{
 await setupT3(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await page.locator('#t3-code-options').click();
 await expect(page.locator('#t3-code-prepare')).toBeEnabled();
 const saved=await getAPI(request,gateway,'clients/t3-code/preferences');
 const update=await request.post(new URL('/api/clients/t3-code/preferences',gateway.url).href,{headers:headers(gateway),data:{...saved.preferences,claude:false}});
 expect(update.ok(),await update.text()).toBe(true);
 let prepares=0;page.on('request',request=>{if(request.method()==='POST'&&new URL(request.url()).pathname==='/api/clients/t3-code')prepares++;});
 await page.locator('#t3-code-prepare').click();
 await expect(page.locator('#t3-code-status')).toContainText('T3 Code settings changed. Review them and open again.');
 expect(prepares).toBe(0);expect(await records(gateway)).toEqual([]);
 await expect(page.locator('#t3-code-agents')).not.toContainText('Existing Claude Code login');
 await page.locator('#t3-code-prepare').click();await expect(page.locator('#t3-code-prepared')).toHaveText('Your saved T3 accounts and models are prepared.');
 expect(prepares).toBe(1);
});

test('T3 exposes only native supported extras and one OpenCode version per account',async({page,gateway,request},testInfo)=>{
 await setControl(gateway,{t3Capabilities:true});await setupT3(request,gateway);await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await page.locator('#t3-code-settings-title').click();
 await expect(page.locator('#t3-code-provider-options label')).toHaveText(['Cursor','Grok','OpenCode','Antigravity','Muse Code','Pi']);
 await expect(page.locator('#t3-code-opencode-version-field')).toBeVisible();
 await expect(page.locator('#t3-code-opencode-version')).toBeDisabled();
 await page.locator('#t3-code-provider-cursor').check();await expect(page.locator('#t3-code-provider-opencode')).toBeEnabled();
 await page.locator('#t3-code-provider-opencode').check();await expect(page.locator('#t3-code-opencode-version')).toBeEnabled();
 await page.locator('#t3-code-opencode-version').selectOption('2');await expect(page.locator('#t3-code-provider-opencode')).toBeEnabled();
 const second=await getAPI(request,gateway,'clients/t3-code/preferences');
 expect(second.preferences.providers).toEqual(['cursor','opencode']);expect(second.preferences.openCode2).toBe(true);
 await expect(page.locator('#t3-code-agents')).toContainText('OpenCode 2');
 await page.locator('#t3-code-opencode-version').selectOption('1');await expect(page.locator('#t3-code-provider-opencode')).toBeEnabled();
 expect((await getAPI(request,gateway,'clients/t3-code/preferences')).preferences.openCode2).toBe(false);
 await page.locator('#t3-code-provider-opencode').uncheck();await expect(page.locator('#t3-code-provider-cursor')).toBeEnabled();
 const disabled=await getAPI(request,gateway,'clients/t3-code/preferences');expect(disabled.preferences.providers).toEqual(['cursor']);expect(disabled.preferences.openCode2).toBe(false);
 await expect(page.locator('#t3-code-opencode-version')).toBeDisabled();
 expect(await records(gateway)).toEqual([]);
 await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-settings-extra-providers.png')});
});

test('T3 keeps saved unavailable choices visible so they can be disabled after changing builds',async({page,gateway,request},testInfo)=>{
 await setControl(gateway,{t3Capabilities:true});await setupT3(request,gateway);
 const original=await getAPI(request,gateway,'clients/t3-code/preferences');
 const selected=await request.post(new URL('/api/clients/t3-code/preferences',gateway.url).href,{headers:headers(gateway),data:{...original.preferences,providers:['cursor','opencode'],openCode2:true}});expect(selected.ok(),await selected.text()).toBe(true);
 await setControl(gateway,{t3Capabilities:false,t3Models:true});await startProxy(page,gateway);
 await page.locator('#tab-t3-code').click();await page.locator('#t3-code-settings-title').click();
 await expect(page.locator('#t3-code-provider-options label')).toHaveText(['Cursor · unavailable in this build','OpenCode · unavailable in this build']);
 await expect(page.locator('#t3-code-provider-cursor')).toBeChecked();await expect(page.locator('#t3-code-provider-cursor')).toBeEnabled();
 await expect(page.locator('#t3-code-opencode-version')).toHaveValue('2');await expect(page.locator('#t3-code-opencode-version-2')).toHaveJSProperty('disabled',true);
 await expect(page.locator('#toast')).toBeHidden();await page.locator('#t3-code-helper').screenshot({path:testInfo.outputPath('t3-settings-unavailable-en.png')});
 await page.locator('#t3-code-provider-cursor').click();await expect(page.locator('#t3-code-provider-cursor')).toHaveCount(0);
 await page.locator('#t3-code-opencode-version').selectOption('1');await expect(page.locator('#t3-code-opencode-version-field')).toBeHidden();
 await page.locator('#t3-code-provider-opencode').click();await expect(page.locator('#t3-code-provider-options label')).toHaveCount(0);
 const cleared=await getAPI(request,gateway,'clients/t3-code/preferences');expect(cleared.preferences.providers).toEqual([]);expect(cleared.preferences.openCode2).toBe(false);expect(await records(gateway)).toEqual([]);
});
