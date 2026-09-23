import {test,expect,copied,startProxy,state} from './fixture.mjs';
import {mkdir,readFile} from 'node:fs/promises';
import path from 'node:path';

const first='vendor/one',second='anthropic/claude-sonnet-4.6';
const choose=(page,id)=>page.locator(`[data-editor-id="${id}"]`);
const name=(page,id)=>page.locator(`[data-editor-name="${id}"]`);
async function saved(request,gateway){
 const response=await request.get(new URL('/api/omp/profile',gateway.url).href,{headers:{Authorization:'Bearer '+gateway.token}});
 expect(response.ok()).toBe(true);return response.json();
}
async function records(gateway){try{return JSON.parse(await readFile(gateway.launchRecords,'utf8'));}catch(error){if(error.code==='ENOENT')return [];throw error;}}

test('Oh My Pi prepares multiple models with names, limits and supported reasoning, then reloads them',async({page,gateway,request},testInfo)=>{
 await page.locator('#tab-omp').click();
 await expect(page.locator('#editor-title')).toHaveText('Oh My Pi · select and prepare');
 await expect(page.locator('[data-editor-id]')).toHaveCount(2);
 await expect(page.locator('#editor-save')).toBeDisabled();
 await expect(page.locator('#editor-picker')).toContainText('USD / 1M');
 await choose(page,first).check();await choose(page,second).check();
 await name(page,first).fill('Short One');
 await page.locator(`[data-editor-initial="${second}"]`).click();
 const effort=page.locator(`[data-editor-effort="${first}"]`);
 await expect(effort.locator('option')).toHaveText(['Automatic','low','high']);
 await effort.selectOption('high');
 await expect(page.locator(`[data-editor-effort="${second}"]`)).toBeDisabled();
 const row=page.locator('.codex-model-entry').filter({has:choose(page,first)});
 await row.locator('details > summary').click();
 await row.getByRole('spinbutton',{name:'Context tokens: '+first,exact:true}).fill('80000');
 await row.getByRole('spinbutton',{name:'Max output tokens (0 = unspecified): '+first,exact:true}).fill('5000');
 await page.locator('#editor-save').click();
 await expect(page.locator('#editor-status')).toContainText('Configuration saved:');
 const result=await saved(request,gateway);
 expect(result.selection.initial).toBe(second);expect(result.selection.models).toHaveLength(2);
 expect(result.selection.models.find(m=>m.id===first)).toMatchObject({name:'Short One',contextWindow:80000,maxOutputTokens:5000,effort:'high',reasoningEfforts:['low','high']});
 expect(result.configPath).toBe(path.join(result.profileDir,'models.yml'));
 const config=await readFile(result.configPath,'utf8');expect(config).toContain('Short One');expect(config).toContain('openai-responses');
 const command=await copied(page,'#editor-copy');
 expect(command).toContain('PI_CODING_AGENT_DIR=');expect(command).toContain('PI_OPENAI_STATEFUL=0');expect(command).toContain('kilo-local/'+second);expect(command).not.toContain('kl_local_');
 const exported=JSON.parse(await copied(page,'#editor-export'));
 expect(exported.providers['kilo-local'].models).toHaveLength(2);
 expect(exported.providers['kilo-local'].models[0].thinking).toEqual({mode:'effort',efforts:['low','high'],defaultLevel:'high'});
 await page.reload();await page.locator('#tab-omp').click();await page.locator('#editor-load').click();
 await expect(name(page,first)).toHaveValue('Short One');
 await expect(page.locator(`[data-editor-initial="${second}"]`)).toHaveAttribute('aria-pressed','true');
 await expect(page.locator(`[data-editor-effort="${first}"]`)).toHaveValue('high');
 await expect(page.locator('#editor-copy')).toBeDisabled();
 await page.locator('#tab-opencode').click();await expect(page.locator('[data-editor-id]:checked')).toHaveCount(0);
 await page.locator('#tab-omp').click();await expect(page.locator('[data-editor-id]:checked')).toHaveCount(2);
 await page.locator('#language').selectOption('es');
 await expect(page.locator('#editor-title')).toHaveText('Oh My Pi · selecciona y prepara');
 await expect(page.locator('#editor-next')).toContainText('No hace falta /login');
 await page.setViewportSize({width:390,height:844});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
 await page.locator('#editor-helper').screenshot({path:testInfo.outputPath('oh-my-pi-es-mobile.png')});
});

test('Oh My Pi launch automatically prepares and saves changed names before opening its terminal',async({page,gateway,request})=>{
 await startProxy(page,gateway);
 await page.locator('#start-stop').click();
 await expect(page.locator('#status-label')).toHaveText('Proxy stopped');
 await page.locator('#tab-omp').click();await choose(page,first).check();
 const folder=path.join(gateway.root,'Oh My Pi project');await mkdir(folder);
 await page.locator('#client-launch-directory').fill(folder);
 let prepares=0;page.on('request',req=>{if(req.method()==='POST'&&new URL(req.url()).pathname==='/api/omp/profile')prepares++;});
 await expect(page.locator('#client-launch')).toHaveText('Launch Oh My Pi');
 await expect(page.locator('#client-launch')).toBeEnabled();
 await page.locator('#client-launch').click();
 await expect.poll(async()=>(await records(gateway)).length).toBe(1);
 expect((await records(gateway))[0]).toMatchObject({client:'omp',directory:folder,kind:'terminal'});
 expect(prepares).toBe(1);expect((await state(request,gateway)).running).toBe(true);
 await page.locator('#client-launch').click();await expect.poll(async()=>(await records(gateway)).length).toBe(2);expect(prepares).toBe(1);
 await name(page,first).fill('Updated before launch');
 await page.locator('#client-launch').click();await expect.poll(async()=>(await records(gateway)).length).toBe(3);expect(prepares).toBe(2);
 expect((await saved(request,gateway)).selection.models[0].name).toBe('Updated before launch');
 await expect(page.locator('#client-launch-status')).not.toHaveClass(/error/);
});

test('Oh My Pi keeps limit editors open when a state poll follows a reasoning change',async({page,gateway,request})=>{
 await page.locator('#tab-omp').click();await choose(page,first).check();
 // Hold the next real background poll until the user has changed reasoning
 // and opened the limits. That poll must redraw the modified model selection.
 let release,arrived;
 const gate=new Promise(resolve=>{release=resolve;}),polling=new Promise(resolve=>{arrived=resolve;});
 let held=false;
 const isState=response=>new URL(response.url()).pathname==='/api/state';
 await page.route('**/api/state',async route=>{
  if(!held){held=true;arrived();await gate;}
  await route.continue();
 });
 try{
  await polling;
  await page.locator(`[data-editor-effort="${first}"]`).selectOption('high');
  const row=page.locator('.codex-model-entry').filter({has:choose(page,first)});
  await row.locator('details > summary').click();
  await expect(row.locator('details')).toHaveAttribute('open','');
  const refreshed=page.waitForResponse(isState);release();await refreshed;
  // The app schedules its next poll only after consuming and rendering the
  // previous response. Waiting for it avoids racing the first render itself.
  await page.waitForResponse(isState);
  await expect(row.locator('details')).toHaveAttribute('open','');
  await row.getByRole('spinbutton',{name:'Context tokens: '+first,exact:true}).fill('80000');
  await row.getByRole('spinbutton',{name:'Max output tokens (0 = unspecified): '+first,exact:true}).fill('5000');
  await page.locator('#editor-save').click();
  await expect(page.locator('#editor-status')).toContainText('Configuration saved:');
  expect((await saved(request,gateway)).selection.models[0]).toMatchObject({contextWindow:80000,maxOutputTokens:5000,effort:'high'});
 }finally{release();}
});
