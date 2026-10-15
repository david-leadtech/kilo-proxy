import {test,expect,startProxy} from './fixture.mjs';
import {copyFile,cp,readFile,writeFile} from 'node:fs/promises';
import path from 'node:path';
const headers=gateway=>({Authorization:'Bearer '+gateway.token});
async function getAPI(request,gateway,endpoint){const r=await request.get(new URL('/api/'+endpoint,gateway.url).href,{headers:headers(gateway)});expect(r.ok(),await r.text()).toBe(true);return r.json();}
async function records(gateway){try{return JSON.parse(await readFile(gateway.launchRecords,'utf8'));}catch(error){if(error.code==='ENOENT')return [];throw error;}}
async function openSettings(page){await page.locator('#cli-preferences > details > summary').click();await expect(page.locator('#cli-path-codex-cli')).toBeEnabled();}
async function chooseCodexCLI(page){await page.locator('#tab-codex-cli').click();await page.locator('#model-picker input[name="model-choice"][value="vendor/one"]').check();}

test('shared CLI paths persist custom installations and reach the selected process without changing Codex Desktop',async({page,gateway,request},testInfo)=>{
 await writeFile(gateway.launchControl,JSON.stringify({claudeVersion:'2.1.288'}));await startProxy(page,gateway);await openSettings(page);
 const codex=path.join(gateway.root,'my codex cli'),claude=path.join(gateway.root,'my claude code'),muse=path.join(gateway.root,'my muse code');await copyFile(gateway.codexExecutable,codex);await copyFile(gateway.claudeExecutable,claude);await copyFile(gateway.codexExecutable,muse);
 const desktop=(await getAPI(request,gateway,'clients/launch')).clients.codex.path;
 await page.locator('#cli-path-codex-cli').fill(codex);await page.locator('#cli-path-save-codex-cli').click();await expect(page.locator('#cli-path-codex-cli')).toBeEnabled();
 await page.locator('#cli-path-claude').fill(claude);await page.locator('#cli-path-save-claude').click();await expect(page.locator('#cli-path-claude')).toBeEnabled();
 await page.locator('#cli-path-muse').fill(muse);await page.locator('#cli-path-save-muse').click();await expect(page.locator('#cli-path-muse')).toBeEnabled();
 expect(await getAPI(request,gateway,'clients/cli/preferences')).toEqual({version:1,codexExecutable:codex,claudeExecutable:claude,museExecutable:muse,synaraExecutable:'',omnigentExecutable:''});await expect(page.locator('#cli-path-resolved-muse')).toContainText(muse);await expect(page.locator('#cli-path-resolved-codex-cli')).toContainText(codex);await expect(page.locator('#cli-path-resolved-claude')).toContainText(claude);
 await chooseCodexCLI(page);await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();await expect.poll(async()=>(await records(gateway)).length).toBe(1);expect((await records(gateway))[0]).toMatchObject({client:'codex-cli',executable:codex});expect((await getAPI(request,gateway,'clients/launch')).clients.codex.path).toBe(desktop);
 await expect(page.locator('#toast')).toBeHidden();await page.locator('#cli-preferences').screenshot({path:testInfo.outputPath('cli-paths-en-wide.png')});
 await page.reload();await openSettings(page);await expect(page.locator('#cli-path-codex-cli')).toHaveValue(codex);await expect(page.locator('#cli-path-claude')).toHaveValue(claude);await expect(page.locator('#cli-path-muse')).toHaveValue(muse);
 await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});await expect(page.locator('#cli-preferences-title')).toHaveText('Instalaciones de aplicaciones y CLI');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.locator('#cli-preferences').screenshot({path:testInfo.outputPath('cli-paths-es-mobile.png')});
 await page.locator('#cli-path-auto-codex-cli').click();await expect(page.locator('#cli-path-codex-cli')).toHaveValue('');expect(await getAPI(request,gateway,'clients/cli/preferences')).toEqual({version:1,codexExecutable:'',claudeExecutable:claude,museExecutable:muse,synaraExecutable:'',omnigentExecutable:''});
 await page.locator('#cli-path-auto-muse').click();await expect(page.locator('#cli-path-muse')).toHaveValue('');expect(await getAPI(request,gateway,'clients/cli/preferences')).toEqual({version:1,codexExecutable:'',claudeExecutable:claude,museExecutable:'',synaraExecutable:'',omnigentExecutable:''});
});

test('CLI path save keeps the exact failed request pending and prevents preparation until retry',async({page,gateway,request})=>{
 await startProxy(page,gateway);await chooseCodexCLI(page);await openSettings(page);const custom=path.join(gateway.root,'codex custom');await copyFile(gateway.codexExecutable,custom);
 let fail=true,release;const gate=new Promise(resolve=>release=resolve),writes=[];
 await page.route('**/api/clients/cli/preferences',async route=>{if(route.request().method()!=='POST')return route.continue();writes.push(route.request().postDataJSON());if(fail){await gate;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:{message:'Synthetic persistence unavailable'}})});}else await route.continue();});
 try{await page.locator('#cli-path-codex-cli').fill(custom);await expect(page.locator('#client-launch')).toBeDisabled();await page.locator('#cli-path-save-codex-cli').click();await expect(page.locator('#cli-path-claude')).toBeDisabled();expect((await getAPI(request,gateway,'clients/cli/preferences')).codexExecutable).toBe('');release();await expect(page.locator('#cli-preferences-status')).toContainText('Synthetic persistence unavailable');await expect(page.locator('#cli-preferences-retry')).toBeVisible();expect(await records(gateway)).toEqual([]);fail=false;await page.locator('#cli-preferences-retry').click();await expect(page.locator('#cli-path-codex-cli')).toBeEnabled();await expect(page.locator('#client-launch')).toBeEnabled();expect(writes).toEqual([{version:1,codexExecutable:custom,claudeExecutable:'',museExecutable:'',synaraExecutable:'',omnigentExecutable:''},{version:1,codexExecutable:custom,claudeExecutable:'',museExecutable:'',synaraExecutable:'',omnigentExecutable:''}]);}finally{release();}
});

test('changing a selected CLI externally during profile preparation blocks success and launch',async({page,gateway,request})=>{
 await startProxy(page,gateway);await chooseCodexCLI(page);await openSettings(page);const custom=path.join(gateway.root,'changed codex');await copyFile(gateway.codexExecutable,custom);
 let release,entered=false;const gate=new Promise(resolve=>release=resolve);
 await page.route('**/api/codex-cli/catalog',async route=>{if(route.request().method()!=='POST')return route.continue();entered=true;await gate;await route.continue();});
 try{await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();await expect.poll(()=>entered).toBe(true);const saved=await request.post(new URL('/api/clients/cli/preferences',gateway.url).href,{headers:headers(gateway),data:{version:1,codexExecutable:custom}});expect(saved.ok(),await saved.text()).toBe(true);release();await expect(page.locator('#client-launch-status')).toContainText('CLI executable settings changed');await expect(page.locator('#cli-path-codex-cli')).toHaveValue(custom);expect(await records(gateway)).toEqual([]);await expect(page.locator('#codex-setup-status')).not.toContainText('Profile ready');}finally{release();}
});

test('Synara application selection persists separately from CLI paths and supports explicit automatic reset',async({page,gateway,request},testInfo)=>{
 await startProxy(page,gateway);await openSettings(page);
 const original=(await getAPI(request,gateway,'clients/launch')).clients.synara.path,custom=path.join(gateway.root,'Custom Synara.app');await cp(original,custom,{recursive:true});
 await page.locator('#cli-path-synara').fill(custom);await page.locator('#cli-path-save-synara').click();await expect(page.locator('#cli-path-synara')).toBeEnabled();
 expect((await getAPI(request,gateway,'clients/cli/preferences')).synaraExecutable).toBe(custom);expect((await getAPI(request,gateway,'clients/launch')).clients.synara.path).toBe(custom);
 await expect(page.locator('#cli-path-resolved-synara')).toContainText(custom);await page.reload();await openSettings(page);await expect(page.locator('#cli-path-synara')).toHaveValue(custom);
 await page.locator('#tab-synara').click();await expect(page.locator('#client-installation')).toBeVisible();await expect(page.locator('#client-installation-status')).toHaveText('Installed');await page.locator('#synara-options').click();await expect(page.locator('#synara-linux')).toContainText('package candidate, not a prepared installation');await expect(page.locator('#synara-linux')).toContainText('keeps the sandbox enabled');
 await page.locator('#cli-preferences').screenshot({path:testInfo.outputPath('synara-path-settings-en.png')});await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});await expect(page.locator('#cli-preferences-title')).toHaveText('Instalaciones de aplicaciones y CLI');await expect(page.locator('#cli-preferences-synara-help')).toContainText('no verifica su origen, versión ni canal');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.locator('#cli-preferences').screenshot({path:testInfo.outputPath('synara-path-settings-es-mobile.png')});
 await page.locator('#cli-path-auto-synara').click();await expect(page.locator('#cli-path-synara')).toHaveValue('');expect((await getAPI(request,gateway,'clients/cli/preferences')).synaraExecutable).toBe('');expect((await getAPI(request,gateway,'clients/launch')).clients.synara.path).toBe(original);expect(await records(gateway)).toEqual([]);
});
