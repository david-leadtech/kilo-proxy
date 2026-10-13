import {test,expect,startProxy} from './fixture.mjs';
import {copyFile,readFile,writeFile} from 'node:fs/promises';
import path from 'node:path';
const headers=gateway=>({Authorization:'Bearer '+gateway.token});
async function getAPI(request,gateway,endpoint){const r=await request.get(new URL('/api/'+endpoint,gateway.url).href,{headers:headers(gateway)});expect(r.ok(),await r.text()).toBe(true);return r.json();}
async function records(gateway){try{return JSON.parse(await readFile(gateway.launchRecords,'utf8'));}catch(error){if(error.code==='ENOENT')return [];throw error;}}
async function openSettings(page){await page.locator('#cli-preferences > details > summary').click();await expect(page.locator('#cli-path-codex-cli')).toBeEnabled();}
async function chooseCodexCLI(page){await page.locator('#tab-codex-cli').click();await page.locator('#model-picker input[name="model-choice"][value="vendor/one"]').check();}

test('shared CLI paths persist custom installations and reach the selected process without changing Codex Desktop',async({page,gateway,request},testInfo)=>{
 await writeFile(gateway.launchControl,JSON.stringify({claudeVersion:'2.1.288'}));await startProxy(page,gateway);await openSettings(page);
 const codex=path.join(gateway.root,'my codex cli'),claude=path.join(gateway.root,'my claude code');await copyFile(gateway.codexExecutable,codex);await copyFile(gateway.claudeExecutable,claude);
 const desktop=(await getAPI(request,gateway,'clients/launch')).clients.codex.path;
 await page.locator('#cli-path-codex-cli').fill(codex);await page.locator('#cli-path-save-codex-cli').click();await expect(page.locator('#cli-path-codex-cli')).toBeEnabled();
 await page.locator('#cli-path-claude').fill(claude);await page.locator('#cli-path-save-claude').click();await expect(page.locator('#cli-path-claude')).toBeEnabled();
 expect(await getAPI(request,gateway,'clients/cli/preferences')).toEqual({version:1,codexExecutable:codex,claudeExecutable:claude});await expect(page.locator('#cli-path-resolved-codex-cli')).toContainText(codex);await expect(page.locator('#cli-path-resolved-claude')).toContainText(claude);
 await chooseCodexCLI(page);await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();await expect.poll(async()=>(await records(gateway)).length).toBe(1);expect((await records(gateway))[0]).toMatchObject({client:'codex-cli',executable:codex});expect((await getAPI(request,gateway,'clients/launch')).clients.codex.path).toBe(desktop);
 await expect(page.locator('#toast')).toBeHidden();await page.locator('#cli-preferences').screenshot({path:testInfo.outputPath('cli-paths-en-wide.png')});
 await page.reload();await openSettings(page);await expect(page.locator('#cli-path-codex-cli')).toHaveValue(codex);await expect(page.locator('#cli-path-claude')).toHaveValue(claude);
 await page.locator('#language').selectOption('es');await page.setViewportSize({width:390,height:844});await expect(page.locator('#cli-preferences-title')).toHaveText('Instalaciones de las CLI');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.locator('#cli-preferences').screenshot({path:testInfo.outputPath('cli-paths-es-mobile.png')});
 await page.locator('#cli-path-auto-codex-cli').click();await expect(page.locator('#cli-path-codex-cli')).toHaveValue('');expect(await getAPI(request,gateway,'clients/cli/preferences')).toEqual({version:1,codexExecutable:'',claudeExecutable:claude});
});

test('CLI path save keeps the exact failed request pending and prevents preparation until retry',async({page,gateway,request})=>{
 await startProxy(page,gateway);await chooseCodexCLI(page);await openSettings(page);const custom=path.join(gateway.root,'codex custom');await copyFile(gateway.codexExecutable,custom);
 let fail=true,release;const gate=new Promise(resolve=>release=resolve),writes=[];
 await page.route('**/api/clients/cli/preferences',async route=>{if(route.request().method()!=='POST')return route.continue();writes.push(route.request().postDataJSON());if(fail){await gate;await route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:{message:'Synthetic persistence unavailable'}})});}else await route.continue();});
 try{await page.locator('#cli-path-codex-cli').fill(custom);await expect(page.locator('#client-launch')).toBeDisabled();await page.locator('#cli-path-save-codex-cli').click();await expect(page.locator('#cli-path-claude')).toBeDisabled();expect((await getAPI(request,gateway,'clients/cli/preferences')).codexExecutable).toBe('');release();await expect(page.locator('#cli-preferences-status')).toContainText('Synthetic persistence unavailable');await expect(page.locator('#cli-preferences-retry')).toBeVisible();expect(await records(gateway)).toEqual([]);fail=false;await page.locator('#cli-preferences-retry').click();await expect(page.locator('#cli-path-codex-cli')).toBeEnabled();await expect(page.locator('#client-launch')).toBeEnabled();expect(writes).toEqual([{version:1,codexExecutable:custom,claudeExecutable:''},{version:1,codexExecutable:custom,claudeExecutable:''}]);}finally{release();}
});

test('changing a selected CLI externally during profile preparation blocks success and launch',async({page,gateway,request})=>{
 await startProxy(page,gateway);await chooseCodexCLI(page);await openSettings(page);const custom=path.join(gateway.root,'changed codex');await copyFile(gateway.codexExecutable,custom);
 let release,entered=false;const gate=new Promise(resolve=>release=resolve);
 await page.route('**/api/codex-cli/catalog',async route=>{if(route.request().method()!=='POST')return route.continue();entered=true;await gate;await route.continue();});
 try{await expect(page.locator('#client-launch')).toBeEnabled();await page.locator('#client-launch').click();await expect.poll(()=>entered).toBe(true);const saved=await request.post(new URL('/api/clients/cli/preferences',gateway.url).href,{headers:headers(gateway),data:{version:1,codexExecutable:custom}});expect(saved.ok(),await saved.text()).toBe(true);release();await expect(page.locator('#client-launch-status')).toContainText('CLI executable settings changed');await expect(page.locator('#cli-path-codex-cli')).toHaveValue(custom);expect(await records(gateway)).toEqual([]);await expect(page.locator('#codex-setup-status')).not.toContainText('Profile ready');}finally{release();}
});
