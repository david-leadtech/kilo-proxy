import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,writeFileSync,readFileSync,rmSync,mkdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {spawnSync} from 'node:child_process';
import {ompPayload,ompConfig,ompLaunch,ompEfforts} from '../ui/omp-helper.mjs';

test('Oh My Pi keeps exact IDs, names and limits with only declared supported reasoning levels',()=>{
 const payload=ompPayload([
  {id:'vendor/one',name:'Short One',contextWindow:64000,maxOutputTokens:4096,reasoning:true,reasoningEfforts:['none','low','high','ultra','high'],effort:'high',inputModalities:['text','image','audio'],inputPrice:1.25,outputPrice:4},
  {id:'openai/unknown',name:'Unknown',reasoning:true,effort:'max'},
  {id:'vendor/no-thinking',reasoning:false,reasoningEfforts:['low','high'],effort:'high'},
 ],'openai/unknown');
 assert.equal(payload.initial,'openai/unknown');
 assert.deepEqual(payload.models[0],{id:'vendor/one',name:'Short One',contextWindow:64000,maxOutputTokens:4096,reasoning:true,reasoningEfforts:['low','high'],effort:'high',inputModalities:['text','image'],inputPrice:1.25,outputPrice:4});
 for(const m of payload.models.slice(1)){assert.equal(m.reasoning,false);assert.equal(m.effort,undefined);assert.equal(m.reasoningEfforts,undefined)}
 assert.equal(payload.models[1].contextWindow,272000);
 assert.deepEqual(ompEfforts({reasoningEfforts:['max','ultra','minimal','xhigh']}),['minimal','xhigh','max']);
});

test('Oh My Pi model export uses Responses compat and never invents cached-token prices',()=>{
 const source={baseURL:'http://127.0.0.1:8877/v1',key:'kl_local_test',selectedModels:[{id:'vendor/one',name:'Short One',contextWindow:64000,maxOutputTokens:4096,reasoningEfforts:['low','high'],effort:'high',inputPrice:1,outputPrice:2},{id:'vendor/unknown',name:'Unknown'}]};
 const config=JSON.parse(ompConfig(source)),provider=config.providers['kilo-local'];
 assert.deepEqual({...provider,models:undefined},{baseUrl:source.baseURL,apiKey:source.key,api:'openai-responses',authHeader:true,compat:{supportsStore:false,supportsReasoningSummary:false,supportsStrictMode:false},models:undefined});
 assert.equal(provider.models.length,2);
 assert.deepEqual(provider.models[0].thinking,{mode:'effort',efforts:['low','high'],defaultLevel:'high'});
 assert.equal(provider.models[0].maxTokens,4096);
 assert.equal(provider.models[1].maxTokens,8192);
 for(const m of provider.models){assert.equal(m.preferWebsockets,false);assert.equal(m.cost,undefined)}
 assert.equal(provider.models[1].reasoning,false);assert.equal(provider.models[1].thinking,undefined);
});

test('Oh My Pi shell launch isolates the profile, clears inherited named profiles and quotes arguments',{skip:process.platform==='win32'},()=>{
 const dir=mkdtempSync(join(tmpdir(),'kilo-omp-command-'));
 try{
  const profile=join(dir,"team's profile"),output=join(dir,'record');
  mkdirSync(profile);writeFileSync(join(profile,'models.yml'),'{}');
  writeFileSync(join(dir,'omp'),'#!/bin/sh\nprintf "%s\\n%s\\n%s\\n%s\\n%s\\n%s\\n" "$PI_CODING_AGENT_DIR" "$OMP_PROFILE" "$PI_PROFILE" "$PI_OPENAI_STATEFUL" "$1" "$2" > "$KILO_TEST_OUTPUT"\n',{mode:0o755});
  const env={...process.env,PATH:dir+':'+process.env.PATH,PI_CODING_AGENT_DIR:'/original',OMP_PROFILE:'production',PI_PROFILE:'legacy',PI_OPENAI_STATEFUL:'1',KILO_TEST_OUTPUT:output};
  const run=spawnSync('sh',['-c',ompLaunch(profile,'vendor/model')],{env,encoding:'utf8'});
  assert.equal(run.status,0,run.stderr);
  assert.equal(readFileSync(output,'utf8'),profile+'\n\n\n0\n--model\nkilo-local/vendor/model\n');
  assert.equal(spawnSync('sh',['-c',ompLaunch(join(dir,'missing'),'vendor/model')],{env}).status,1);
  const ps=ompLaunch(profile,'vendor/model','powershell');
  assert.match(ps,/team''s profile/);assert.match(ps,/finally/);
  assert.match(ps,/Remove-Item -LiteralPath Env:OMP_PROFILE/);assert.match(ps,/Remove-Item -LiteralPath Env:PI_PROFILE/);
  assert.match(ps,/Set-Item -LiteralPath "Env:\$kiloName" -Value \$kiloPrevious\[\$kiloName\]/);
  assert.doesNotMatch(ps,/kl_local_/);
 }finally{rmSync(dir,{recursive:true,force:true})}
});
