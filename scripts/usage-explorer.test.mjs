import test from 'node:test';
import assert from 'node:assert/strict';
import {usageCSV,usageFilename} from '../ui/usage-explorer.mjs';

test('raw CSV preserves precision and quotes dimension labels without aggregating rows',()=>{
  const result={columns:['datetime','model','project','costMicrodollars','inputTokens'],rows:[['2025-01-01','vendor/one','repo,"ticket"\nline','123.000125','9007199254740993'],['2025-01-01','vendor/two','','0','2']]};
  assert.equal(usageCSV(result),'datetime,model,project,costMicrodollars,inputTokens\r\n2025-01-01,vendor/one,"repo,""ticket""\nline",123.000125,9007199254740993\r\n2025-01-01,vendor/two,,0,2\r\n');
  assert.equal(usageCSV({columns:result.columns,rows:[]}),'datetime,model,project,costMicrodollars,inputTokens\r\n');
  assert.equal(usageFilename({startDate:'2025-01-01',endDate:'2025-01-02',costSource:'market'}),'kilo-usage-2025-01-01-2025-01-02-market.csv');
});
