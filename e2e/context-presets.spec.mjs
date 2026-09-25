import {test,expect,copied} from './fixture.mjs';
import {readFile} from 'node:fs/promises';
import path from 'node:path';

const manual='manual/context-test';
const card=(page,id)=>page.locator('.codex-model-entry').filter({has:page.locator(`[data-editor-id="${id}"]`)});

test('context presets limit OpenCode, preserve custom values on reload and explain unknown maxima',async({page,gateway})=>{
 await page.locator('#tab-opencode').click();
 await page.locator('#editor-manual-title').click();
 await page.locator('#editor-id').fill(manual);await page.locator('#editor-add').click();
 const row=card(page,manual);
 await expect(row.getByRole('button',{name:'Recommended · 272K',exact:true})).toHaveAttribute('aria-pressed','true');
 await expect(row.getByRole('button',{name:'Maximum',exact:true})).toBeDisabled();
 await expect(row.locator('.context-policy-summary')).toContainText('Working: 272,000');
 await row.getByRole('button',{name:'Low · 128K',exact:true}).click();
 await page.locator('#editor-save').click();await expect(page.locator('#editor-status')).toContainText('Configuration saved:');
 let exported=JSON.parse(await copied(page,'#editor-export'));
 expect(exported.provider['kilo-local'].models[manual].limit).toEqual({context:128000,output:8192});
 await row.getByRole('button',{name:'Custom',exact:true}).click();
 await row.getByRole('spinbutton',{name:'Context tokens: '+manual,exact:true}).fill('400000');
 await page.locator('#editor-save').click();await expect(page.locator('#editor-status')).toContainText('Configuration saved:');
 const saved=await readFile(path.join(gateway.profiles.opencode,'opencode.json'),'utf8');
 expect(saved).toContain('400000');
 await page.reload();await page.locator('#tab-opencode').click();await page.locator('#editor-load').click();
 await expect(row.getByRole('button',{name:'Custom',exact:true})).toHaveAttribute('aria-pressed','true');
 await expect(row.getByRole('spinbutton',{name:'Context tokens: '+manual,exact:true})).toHaveValue('400000');
 exported=JSON.parse(await copied(page,'#editor-export'));
 expect(exported.provider['kilo-local'].models[manual].limit.context).toBe(400000);
});

test('context and output stay below Kilo model capabilities even for larger custom entries',async({page})=>{
 const id='vendor/one';
 await page.locator('#tab-omp').click();await page.locator(`[data-editor-id="${id}"]`).check();
 const row=card(page,id);
 await expect(row.locator('.context-policy-summary')).toContainText('Working: 128,000 · Maximum: 128,000');
 await row.getByRole('button',{name:'Custom',exact:true}).click();
 await row.getByRole('spinbutton',{name:'Context tokens: '+id,exact:true}).fill('1000000');
 await row.locator('details > summary').click();
 await row.getByRole('spinbutton',{name:'Max output tokens (0 = automatic): '+id,exact:true}).fill('128000');
 const exported=JSON.parse(await copied(page,'#editor-export'));
 expect(exported.providers['kilo-local'].models[0]).toMatchObject({contextWindow:128000,maxTokens:4000});
});

test('Codex exports per-model compaction and keeps model switching limits distinct',async({page,gateway})=>{
 await page.locator('#tab-codex').click();
 await page.locator('#codex-manual-entry > summary').click();
 await page.locator('#codex-manual-id').fill(manual);await page.locator('#add-codex-model').click();
 const row=page.locator('.codex-model-entry').filter({has:page.locator(`[data-focus="model:${manual}"]`)});
 await row.getByRole('button',{name:'Low · 128K',exact:true}).click();
 await page.locator('#save-codex-catalog').click();await expect(page.locator('#codex-setup-status')).toContainText('Profile ready');
 const saved=JSON.parse(await readFile(path.join(gateway.profiles.codex,'models.json'),'utf8'));
 expect(saved.models[0]).toMatchObject({slug:manual,context_window:128000,auto_compact_token_limit:113408});
 await page.setViewportSize({width:390,height:844});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
});


test('refresh updates filtered-out selections and invalid context drafts block preparation',async({page})=>{
 const id='vendor/one';
 await page.locator('#tab-omp').click();await page.locator(`[data-editor-id="${id}"]`).check();
 const row=card(page,id);
 await row.getByRole('button',{name:'Maximum',exact:true}).click();
 await page.locator('#editor-search').fill('no matches here');
 let known=true;
 await page.route('**/api/models',async route=>{const response=await route.fetch();const body=await response.json();body.models=body.models.map(model=>model.id===id?{...model,contextWindow:known?64000:0,maxOutputTokens:known?2000:0}:model);await route.fulfill({response,json:body});});
 await page.locator('#editor-refresh').click();
 await expect(page.locator('#editor-export')).toBeEnabled();
 const exported=JSON.parse(await copied(page,'#editor-export'));
 expect(exported.providers['kilo-local'].models[0]).toMatchObject({contextWindow:64000,maxTokens:2000});
 known=false;await page.locator('#editor-refresh').click();
 await expect(page.locator('#editor-save')).toBeDisabled();
 await expect(page.locator('#editor-status')).toContainText('maximum context');
 await page.locator('#editor-search').fill('');
 await row.getByRole('button',{name:'Custom',exact:true}).click();
 const input=row.getByRole('spinbutton',{name:'Context tokens: '+id,exact:true});
 await input.fill('128000');await expect(page.locator('#editor-save')).toBeEnabled();
 await input.fill('');await expect(page.locator('#editor-save')).toBeDisabled();
 await expect(page.locator('#editor-status')).toContainText('whole number');
});


test('adding a catalog ID manually keeps provider caps and other client choices independent',async({page})=>{
 const id='vendor/one';
 await page.locator('#tab-omp').click();
 await page.locator('#editor-manual-title').click();
 await page.locator('#editor-id').fill(id);await page.locator('#editor-add').click();
 const row=card(page,id);
 await row.getByRole('button',{name:'Custom',exact:true}).click();
 await row.getByRole('spinbutton',{name:'Context tokens: '+id,exact:true}).fill('80000');
 await row.locator('details > summary').click();
 await row.getByRole('spinbutton',{name:'Max output tokens (0 = automatic): '+id,exact:true}).fill('128000');
 const omp=JSON.parse(await copied(page,'#editor-export'));
 expect(omp.providers['kilo-local'].models[0]).toMatchObject({contextWindow:80000,maxTokens:4000});
 await page.locator('#tab-opencode').click();await page.locator(`[data-editor-id="${id}"]`).check();
 await expect(row.getByRole('button',{name:'Recommended · 272K',exact:true})).toHaveAttribute('aria-pressed','true');
 const openCode=JSON.parse(await copied(page,'#editor-export'));
 expect(openCode.provider['kilo-local'].models[id].limit).toEqual({context:128000,output:4000});
});
