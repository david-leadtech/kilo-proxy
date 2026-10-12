import test from 'node:test';
import assert from 'node:assert/strict';
import {createCLIPreferencesHelper,cliPreferenceOperation} from '../ui/cli-preferences-helper.mjs';
const initial=()=>({version:1,codexExecutable:'',claudeExecutable:''});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(t,api,extra={}) {
 const old=globalThis.document,nodes=new Map(),messages=[];
 globalThis.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{textContent:'',value:'',disabled:false,hidden:false,classList:{toggle(){}},listeners:{},addEventListener(name,fn){this.listeners[name]=fn},dispatch(name){return this.listeners[name]?.()}});return nodes.get(id)}};
 t.after(()=>{globalThis.document=old});
 const helper=createCLIPreferencesHelper({api,notify:(message,error)=>messages.push({message,error}),...extra});helper.render({language:'en'});
 const input=(client,value)=>{nodes.get('cli-path-'+client).value=value;nodes.get('cli-path-'+client).dispatch('input')};
 return {helper,nodes,messages,input};
}
test('CLI operation guards target executable consumers and leave Codex Desktop and unrelated writes alone',()=>{
 for(const [path,client]of [['codex-cli/catalog','codex-cli'],['claude/profile','claude'],['clients/t3-code','t3-code'],['clients/synara','synara'],['open-design/profile','open-design'],['xcode/codex','xcode-codex'],['xcode/claude','xcode-claude']])assert.equal(cliPreferenceOperation(path,{},'POST'),client);
 assert.equal(cliPreferenceOperation('clients/launch',{client:'claude'},'POST'),'claude');
 for(const [path,body,method]of [['codex/catalog',{},'POST'],['clients/launch',{client:'codex'},'POST'],['clients/cli/preferences',{},'POST'],['clients/t3-code',undefined,'GET']])assert.equal(cliPreferenceOperation(path,body,method),'');
});
test('CLI paths explicitly save one tool, preserve the other, reject relative paths and return to automatic detection',async t=>{
 let prefs={...initial(),claudeExecutable:'/old/claude'},refreshes=0;const writes=[];
 const {helper,nodes,input}=fixture(t,async(path,body)=>{if(body){prefs={...body};writes.push(body)}return {...prefs}}, {onPreferencesSaved:()=>refreshes++});
 await tick();input('codex-cli','relative/codex');assert.equal(helper.blocked(),true);nodes.get('cli-path-save-codex-cli').dispatch('click');assert.equal(writes.length,0);assert.match(nodes.get('cli-preferences-status').textContent,/absolute path/);
 input('codex-cli','/custom/codex');nodes.get('cli-path-save-codex-cli').dispatch('click');await tick();assert.deepEqual(writes[0],{...initial(),codexExecutable:'/custom/codex',claudeExecutable:'/old/claude'});assert.equal(helper.blocked(),false);
 nodes.get('cli-path-auto-claude').dispatch('click');await tick();assert.equal(prefs.codexExecutable,'/custom/codex');assert.equal(prefs.claudeExecutable,'');assert.equal(refreshes,2);
 helper.render({language:'es',launchInfo:{clients:{'codex-cli':{path:'/custom/codex'}}}});assert.match(nodes.get('cli-preferences-help').textContent,/T3 Code, Synara y Open Design/);assert.equal(nodes.get('cli-path-resolved-codex-cli').textContent,'Ejecutable detectado: /custom/codex');
});
test('failed and unacknowledged saves keep an exact pending snapshot and block every affected operation until retry succeeds',async t=>{
 let prefs=initial(),fail=true,release;const gate=new Promise(resolve=>release=resolve),writes=[];
 const {helper,nodes,input}=fixture(t,async(path,body)=>{if(body){writes.push({...body});if(fail){await gate;throw new Error('Synthetic persistence unavailable')}prefs={...body}}return {...prefs}});
 await tick();input('claude','/custom/claude');nodes.get('cli-path-save-claude').dispatch('click');assert.equal(nodes.get('cli-path-codex-cli').disabled,true);await assert.rejects(helper.guardOperation('claude',async()=>assert.fail('started while pending')),/settings changed/);release();await tick();assert.equal(nodes.get('cli-preferences-retry').hidden,false);assert.equal(helper.blocked(),true);
 fail=false;nodes.get('cli-preferences-retry').dispatch('click');await tick();assert.deepEqual(writes[1],writes[0]);assert.equal(helper.blocked(),false);
});
test('a changed saved path before preparation is shown and prevents the profile request',async t=>{
 let prefs=initial(),operations=0,refreshes=0;
 const {helper,nodes}=fixture(t,async()=>({...prefs}),{onPreferencesSaved:()=>refreshes++});await tick();prefs.claudeExecutable='/new/claude';
 await assert.rejects(helper.guardOperation('claude',async()=>operations++),/settings changed/);assert.equal(operations,0);assert.equal(nodes.get('cli-path-claude').value,'/new/claude');assert.equal(refreshes,1);
});
test('a changed path during preparation prevents prepared success and displays the actual saved path',async t=>{
 let prefs=initial();const {helper,nodes,messages}=fixture(t,async()=>({...prefs}));await tick();
 await assert.rejects(helper.guardOperation('synara',async()=>{prefs.codexExecutable='/changed/codex';return {prepared:true}}),/settings changed/);assert.equal(nodes.get('cli-path-codex-cli').value,'/changed/codex');assert.deepEqual(messages,[]);
});
test('a draft changed while verification is in flight remains visible and cannot prepare',async t=>{
 let reads=0,release;const gate=new Promise(resolve=>release=resolve);const {helper,nodes,input}=fixture(t,async()=>{if(++reads===2)await gate;return initial()});await tick();
 const pending=helper.guardOperation('codex-cli',async()=>assert.fail('stale preparation'));input('claude','/unsaved/claude');release();await assert.rejects(pending,/settings changed/);assert.equal(nodes.get('cli-path-claude').value,'/unsaved/claude');assert.equal(helper.blocked(),true);
});
test('T3 shares OpenCode executable paths while keeping its major independent',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},operations=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>({...prefs,major:2})});await tick();
 const result=await helper.guardOperation('t3-code',async()=>{operations++;prefs.major=2;return 'prepared'});assert.equal(result,'prepared');assert.equal(operations,1);
});
test('T3 rejects external OpenCode path changes before and during prepare and requests visible path reload',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},known={...prefs},reloads=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>known,onOpenCodePathsChanged:()=>reloads++});await tick();prefs.executable2='/changed/two';
 await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('stale preparation')),/settings changed/);known={...prefs};await assert.rejects(helper.guardOperation('t3-code',async()=>{prefs.executable1='/changed/one'}),/settings changed/);assert.equal(reloads,2);
});
