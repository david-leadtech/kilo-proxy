import {test, expect, startProxy, state} from './fixture.mjs';
import {readFile} from 'node:fs/promises';
import path from 'node:path';

test('request capture requires opt-in, saves the choice, and keeps accounting while off', async ({page, gateway, request}) => {
  await startProxy(page, gateway);
  const initial = await state(request, gateway);
  expect(initial.captureEnabled).toBe(false);
  await expect(page.locator('#capture-activity')).not.toBeChecked();
  const send = async () => {
    const response = await request.post(gateway.baseURL + '/responses', {
      headers: {Authorization: 'Bearer ' + initial.localKey},
      data: {model: 'vendor/one', input: 'SYNTHETIC_CAPTURE_OPT_IN'},
    });
    expect(response.status()).toBe(200);
  };
  await send();
  await expect(page.locator('#spend-total')).toHaveText('$0.012300');
  await expect(page.locator('#activity-table')).toBeHidden();
  expect((await state(request, gateway)).events || []).toHaveLength(0);
  await page.locator('#capture-activity').check();
  await expect.poll(async () => (await state(request, gateway)).captureEnabled).toBe(true);
  const settingsFile = path.join(gateway.root, 'app', 'settings.json');
  expect(JSON.parse(await readFile(settingsFile, 'utf8')).captureActivity).toBe(true);
  await page.reload();
  await expect(page.locator('#capture-activity')).toBeChecked();
  await send();
  await expect(page.locator('#event-rows button')).toHaveCount(1);
  await page.locator('#event-rows button').click();
  await expect(page.locator('#trace-body')).toContainText('SYNTHETIC_CAPTURE_OPT_IN');
  await page.locator('#capture-activity').uncheck();
  await expect.poll(async () => (await state(request, gateway)).captureEnabled).toBe(false);
  await expect(page.locator('#activity-table')).toBeHidden();
  await expect(page.locator('#activity-inspector')).toBeHidden();
  expect(JSON.parse(await readFile(settingsFile, 'utf8')).captureActivity).toBe(false);
  await send();
  await expect(page.locator('#spend-total')).toHaveText('$0.036900');
  expect((await state(request, gateway)).events || []).toHaveLength(0);
  await page.reload();
  await expect(page.locator('#capture-activity')).not.toBeChecked();
  await expect(page.locator('#empty-activity-title')).toHaveText('Request capture is off');
});
