import {test, expect, startProxy, state} from './fixture.mjs';
import {readFile} from 'node:fs/promises';
import path from 'node:path';

test('large image modes and compression profiles are opt-in and persist while running', async ({page,gateway,request}) => {
 const mode=page.locator('#image-transport-mode'),profile=page.locator('#image-compression-profile');
 await expect(mode).toHaveValue('off');
 expect((await state(request,gateway)).imageTransport).toEqual({mode:'off',profile:'high'});
 await expect(profile).toBeHidden();
 await expect(page.locator('#image-upload-options')).toBeHidden();
 await startProxy(page,gateway);
 await mode.selectOption('compress');
 await expect(profile).toHaveValue('high');
 await expect(page.locator('#image-compression-note')).toContainText('never lowers quality further or uploads');
 for(const value of ['balanced','small','high']) {
  await profile.selectOption(value);
  await expect.poll(async()=>(await state(request,gateway)).imageTransport).toEqual({mode:'compress',profile:value});
 }
 await profile.selectOption('balanced');
 await expect.poll(async()=>(await state(request,gateway)).imageTransport.profile).toBe('balanced');
 const settingsFile=path.join(gateway.root,'app','settings.json');
 expect(JSON.parse(await readFile(settingsFile,'utf8')).imageTransport).toEqual({mode:'compress',profile:'balanced'});
 await page.reload();
 await expect(mode).toHaveValue('compress');
 await expect(profile).toHaveValue('balanced');
 await mode.selectOption('upload');
 await expect(page.locator('#image-upload-limit')).toContainText('not a documented Gateway integration');
 await expect(page.locator('#image-upload-cleanup')).toContainText('an expired link does not mean');
 await expect(profile).toBeHidden();
 await expect.poll(async()=>(await state(request,gateway)).imageTransport).toEqual({mode:'upload',profile:'balanced'});
 await expect(page.locator('#status-label')).toHaveText('Proxy running');
 await page.locator('#language').selectOption('es');
 await expect(page.locator('#image-transport-title')).toHaveText('Imágenes grandes');
 await mode.selectOption('compress');
 await expect(profile).toHaveValue('balanced');
 await expect(profile.locator('option:checked')).toHaveText('Equilibrado');
 await mode.selectOption('off');
 await expect.poll(async()=>(await state(request,gateway)).imageTransport.mode).toBe('off');
 expect(JSON.parse(await readFile(settingsFile,'utf8')).imageTransport).toEqual({mode:'off',profile:'balanced'});
 await page.reload();
 await expect(mode).toHaveValue('off');
 await expect(profile).toBeHidden();
 await expect(page.locator('#image-upload-warning')).toBeHidden();
});
