import test from 'node:test';
import assert from 'node:assert/strict';
import {billingMoney, billingStatus} from '../ui/account-usage.mjs';

test('account amounts preserve exact signed decimal money without inventing zero', () => {
  for (const [value,expected] of [['0.000000000','$0'],['0.000000001','$0.000000001'],['-0.062515000','$-0.062515'],['9007199254740993.01','$9007199254740993.01']]) assert.equal(billingMoney(value),expected);
  for (const value of [undefined,null,'','NaN','Infinity','1e3','1/2',0]) assert.equal(billingMoney(value),'—');
  for (const status of ['loading','unavailable','signed_out',undefined]) assert.equal(billingMoney('0.00', status || 'unknown'),'—');
  assert.equal(billingMoney('48.12','ready',true),'—');
});

test('failed and stale account requests explain their state in both languages', () => {
  for (const language of ['en','es']) {
    assert.equal(billingStatus('ready',false,language),'');
    assert.match(billingStatus('ready',true,language), language === 'en' ? /Out of date/ : /Desactualizado/);
    assert.match(billingStatus('signed_out',false,language), language === 'en' ? /Sign in/ : /Inicia sesión/);
    assert.match(billingStatus('unavailable',false,language,'HTTP 401'), /HTTP 401/);
    assert.match(billingStatus('loading',false,language), /Kilo/);
  }
});
