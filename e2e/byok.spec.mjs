import {test, expect, startProxy, state} from './fixture.mjs';
import {writeFile} from 'node:fs/promises';

for (const language of ['en','es']) {
  test(`BYOK catalog section ${language} keeps selection and follows the active team`, async ({page,gateway,request},testInfo) => {
    await writeFile(gateway.launchControl,JSON.stringify({byokModels:true}));
    await startProxy(page,gateway);
    await page.locator('#language').selectOption(language);
    await page.locator('#tab-codex').click();
    await page.locator('#load-models').click();
    const picker=page.locator('#model-picker');
    const first=picker.locator('.codex-model-entry').filter({has:page.locator('[data-focus="model:vendor/one"]')});
    const second=picker.locator('.codex-model-entry').filter({has:page.locator('[data-focus="model:anthropic/claude-sonnet-4.6"]')});
    await expect(picker.locator('.model-byok-heading').first()).toContainText(language==='en'?'Models with BYOK':'Modelos con BYOK');
    await expect(first.locator('.model-byok-badge')).toHaveText('BYOK');
    await expect(second.locator('.model-byok-badge')).toHaveCount(0);
    await first.locator('input[name="model-choice"]').check();
    await page.locator('#model-search').fill('vendor/one');
    await expect(first.locator('.model-byok-badge')).toHaveText('BYOK');
    await expect(first.locator('input[name="model-choice"]')).toBeChecked();
    await page.locator('#model-search').fill('');
    for(const width of [1180,390]) {
      await page.setViewportSize({width,height:1000});
      await expect(first.locator('.model-byok-badge')).toBeVisible();
      expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
      await picker.scrollIntoViewIfNeeded();
      await page.screenshot({path:testInfo.outputPath(`byok-${language}-${width}.png`)});
    }
    await page.locator('#start-stop').click();
    await page.locator('#org-id').fill('other-team');
    await page.locator('#start-stop').click();
    await expect.poll(async()=> (await state(request,gateway)).orgId).toBe('other-team');
    await page.locator('#load-models').click();
    await expect(first.locator('.model-byok-badge')).toHaveCount(0);
    await expect(first.locator('input[name="model-choice"]')).toBeChecked();
    await expect(second).toHaveCount(1);
    await expect(picker.locator('.model-byok-heading').first()).toContainText(language==='en'?'does not report':'no informa');
  });
}
