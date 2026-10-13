import {test,expect,startProxy} from './fixture.mjs';
import {readFile,writeFile} from 'node:fs/promises';

test('historical crosses download every raw row with exact precision and keep drafts during polling',async({page,gateway},testInfo)=>{
  await startProxy(page,gateway);
  await page.locator('#usage-startDate').fill('2025-01-01');
  await page.locator('#usage-endDate').fill('2025-01-10');
  await page.locator('#usage-group-provider').check();
  await page.locator('#usage-group-project').check();
  await expect(page.locator('#usage-group-user')).toBeDisabled();
  await page.locator('#usage-cost-source').selectOption('market');
  await page.locator('#usage-filters summary').click();
  await page.locator('#usage-filter-models').fill('vendor/one');
  // The normal state polling used to recreate account UI every second.
  await expect.poll(()=>page.locator('#usage-filter-models').inputValue()).toBe('vendor/one');
  await page.locator('#usage-endDate').press('Enter');
  await expect(page.locator('#usage-query-status')).toContainText('2 rows');
  await expect(page.locator('#usage-query-table th')).toHaveCount(11);
  await expect(page.locator('#usage-query-table')).toContainText('9007199254740993');
  const downloaded=page.waitForEvent('download');await page.locator('#usage-download').click();const file=await downloaded;
  expect(file.suggestedFilename()).toBe('kilo-usage-2025-01-01-2025-01-10-market.csv');
  const content=await readFile(await file.path(),'utf8');
  expect(content).toContain('datetime,model,provider,project,costMicrodollars,requestCount,inputTokens,outputTokens,cacheWriteTokens,cacheHitTokens,errorCount\r\n');
  expect(content).toContain('123.000125,2,9007199254740993,20,10,60,0');
  expect(content.trim().split('\r\n')).toHaveLength(3);
  expect(content).not.toContain('synthetic-kilo-personal-key');
  await testInfo.attach('usage-history-wide.png',{body:await page.locator('#usage-explorer').screenshot(),contentType:'image/png'});
  await page.setViewportSize({width:390,height:844});
  await testInfo.attach('usage-history-narrow.png',{body:await page.locator('#usage-explorer').screenshot(),contentType:'image/png'});
  await expect(page.locator('#usage-download')).toBeEnabled();
  await page.locator('#language').selectOption('es');
  await expect(page.locator('#usage-download')).toHaveText('Descargar CSV raw');
  await expect(page.locator('#usage-query-table tbody tr')).toHaveCount(2);
});

test('failed queries disable exports and changing accounts discards old rows',async({page,gateway})=>{
  await startProxy(page,gateway);
  await page.locator('#usage-startDate').fill('2025-01-01');await page.locator('#usage-endDate').fill('2025-01-01');
  await page.locator('#usage-query').click();await expect(page.locator('#usage-download')).toBeEnabled();
  await writeFile(gateway.launchControl,JSON.stringify({billingFailure:true}));
  await page.locator('#usage-query').click();await expect(page.locator('#usage-query-status')).toContainText('Sign in again');
  await expect(page.locator('#usage-download')).toBeDisabled();await expect(page.locator('#usage-query-table')).toHaveCount(0);
  await writeFile(gateway.launchControl,'{}');await page.locator('#usage-query').click();await expect(page.locator('#usage-download')).toBeEnabled();
  await page.locator('#start-stop').click();await expect(page.locator('#status-label')).toHaveText('Proxy stopped');
  await page.locator('#org-id').fill('other-team');await page.locator('#start-stop').click();await expect(page.locator('#status-label')).toHaveText('Proxy running');
  await expect(page.locator('#usage-download')).toBeDisabled();await expect(page.locator('#usage-query-table')).toHaveCount(0);
  await page.locator('#usage-query').click();await expect(page.locator('#usage-query-status')).toContainText('0 rows');
  await expect(page.locator('#usage-download')).toBeEnabled();
});
