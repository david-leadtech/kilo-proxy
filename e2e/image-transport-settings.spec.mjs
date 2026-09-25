import {test, expect, startProxy, state} from './fixture.mjs';
import {readFile} from 'node:fs/promises';
import path from 'node:path';

test('large image modes and compression profiles are opt-in and persist while running', async ({page,gateway,request}) => {
 const mode=page.locator('#image-transport-mode'),profile=page.locator('#image-compression-profile');
 await expect(mode).toHaveValue('off');
 expect((await state(request,gateway)).imageTransport).toEqual({mode:'off',profile:'high',litterboxTTL:'1h'});
 await expect(profile).toBeHidden();
 await expect(page.locator('#image-upload-options')).toBeHidden();
 await startProxy(page,gateway);
 await mode.selectOption('compress');
 await expect(profile).toHaveValue('high');
 await expect(page.locator('#image-compression-note')).toContainText('never lowers quality further or uploads');
 for(const value of ['balanced','small','high']) {
  await profile.selectOption(value);
  await expect.poll(async()=>(await state(request,gateway)).imageTransport).toEqual({mode:'compress',profile:value,litterboxTTL:'1h'});
 }
 await profile.selectOption('balanced');
 await expect.poll(async()=>(await state(request,gateway)).imageTransport.profile).toBe('balanced');
 const settingsFile=path.join(gateway.root,'app','settings.json');
 expect(JSON.parse(await readFile(settingsFile,'utf8')).imageTransport).toEqual({mode:'compress',profile:'balanced',litterboxTTL:'1h'});
 await page.reload();
 await expect(mode).toHaveValue('compress');
 await expect(profile).toHaveValue('balanced');
 await mode.selectOption('upload');
 await expect(page.locator('#image-upload-limit')).toContainText('not a documented Gateway integration');
 await expect(page.locator('#image-upload-cleanup')).toContainText('an expired link does not mean');
 await expect(profile).toBeHidden();
 await expect.poll(async()=>(await state(request,gateway)).imageTransport).toEqual({mode:'upload',profile:'balanced',litterboxTTL:'1h'});
 await expect(page.locator('#status-label')).toHaveText('Proxy running');
 await page.locator('#language').selectOption('es');
 await expect(page.locator('#image-transport-title')).toHaveText('Imágenes grandes');
 await mode.selectOption('compress');
 await expect(profile).toHaveValue('balanced');
 await expect(profile.locator('option:checked')).toHaveText('Equilibrado');
 await mode.selectOption('off');
 await expect.poll(async()=>(await state(request,gateway)).imageTransport.mode).toBe('off');
 expect(JSON.parse(await readFile(settingsFile,'utf8')).imageTransport).toEqual({mode:'off',profile:'balanced',litterboxTTL:'1h'});
 await page.reload();
 await expect(mode).toHaveValue('off');
 await expect(profile).toBeHidden();
 await expect(page.locator('#image-upload-warning')).toBeHidden();
});


test('public image backends explain requirements and persist the explicit choice and expiry', async ({page,gateway,request},testInfo) => {
 const mode=page.locator('#image-transport-mode'),ttl=page.locator('#image-litterbox-ttl');
 const settingsFile=path.join(gateway.root,'app','settings.json');
 await expect(mode).toHaveValue('off');
 await expect(page.locator('#image-public-options')).toBeHidden();
 await expect(ttl).toBeHidden();
 await expect(page.locator('#image-transport-description')).toContainText('Chat Completions');
 await expect(page.locator('#image-transport-description')).toContainText('Anthropic Messages');
 await expect(page.locator('#image-transport-saving')).toContainText('failures never switch');
 for(const [backend,requirement,description] of [
  ['cloudflare','Requires cloudflared','original image bytes'],
  ['tailscale','8443','outside your tailnet'],
  ['litterbox','No account or extra executable','third-party service']
 ]) {
  await mode.selectOption(backend);
  await expect.poll(async()=>(await state(request,gateway)).imageTransport.mode).toBe(backend);
  await expect(page.locator('#image-public-requirements')).toContainText(requirement);
  await expect(page.locator('#image-public-description')).toContainText(description);
  await expect(page.locator('#image-compression-options')).toBeHidden();
  await expect(page.locator('#image-upload-options')).toBeHidden();
  await page.reload();
  await expect(mode).toHaveValue(backend);
  expect(JSON.parse(await readFile(settingsFile,'utf8')).imageTransport.mode).toBe(backend);
 }
 await expect(ttl).toHaveValue('1h');
 await expect(mode.locator('option:checked')).toHaveText('Litterbox · Experimental');
 await expect(page.locator('#image-litterbox-experimental')).toHaveText('Experimental: live availability could not be confirmed from this network. If the service rejects uploads, choose Cloudflare or local compression.');
 await expect(page.locator('#image-litterbox-cleanup')).toContainText('cannot delete these uploads early');
 await expect(page.locator('#image-litterbox-terms')).toContainText('prior approval for commercial service use');
 await expect(page.locator('#image-litterbox-faq')).toHaveAttribute('href','https://litterbox.catbox.moe/faq.php');
 for(const expiry of ['12h','24h','72h','1h']) {
  await ttl.selectOption(expiry);
  await expect.poll(async()=>(await state(request,gateway)).imageTransport).toEqual({mode:'litterbox',profile:'high',litterboxTTL:expiry});
 }
 await ttl.selectOption('24h');
 await expect.poll(async()=>(await state(request,gateway)).imageTransport.litterboxTTL).toBe('24h');
 await page.locator('#language').selectOption('es');
 await expect(page.locator('#image-public-title')).toHaveText('Alojamiento público temporal');
 await expect(mode.locator('option:checked')).toHaveText('Litterbox · Experimental');
 await expect(page.locator('#image-litterbox-experimental')).toHaveText('Experimental: no se ha podido confirmar la disponibilidad real desde esta red. Si el servicio rechaza las subidas, elige Cloudflare o la compresión local.');
 await expect(ttl.locator('option:checked')).toHaveText('24 horas');
 await expect(page.locator('#image-litterbox-cleanup')).toContainText('no puede borrar');
 await page.locator('#image-transport-settings').scrollIntoViewIfNeeded();
 await testInfo.attach('image-backends-desktop.png',{body:await page.locator('#image-transport-settings').screenshot(),contentType:'image/png'});
 await page.setViewportSize({width:390,height:844});
 await expect.poll(()=>page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await testInfo.attach('image-backends-mobile.png',{body:await page.locator('#image-transport-settings').screenshot(),contentType:'image/png'});
 await mode.selectOption('cloudflare');
 await expect(ttl).toBeHidden();
 await expect(page.locator('#image-litterbox-experimental')).toBeHidden();
 await expect(page.locator('#image-public-requirements')).toContainText('Requiere cloudflared');
 await mode.selectOption('off');
 await expect.poll(async()=>(await state(request,gateway)).imageTransport).toEqual({mode:'off',profile:'high',litterboxTTL:'24h'});
 await page.reload();
 await expect(mode).toHaveValue('off');
 await expect(page.locator('#image-public-options')).toBeHidden();
 await expect(ttl).toBeHidden();
 expect(JSON.parse(await readFile(settingsFile,'utf8')).imageTransport).toEqual({mode:'off',profile:'high',litterboxTTL:'24h'});
});
