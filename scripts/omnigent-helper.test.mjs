import test from 'node:test';
import assert from 'node:assert/strict';
import {createOmnigentHelper,omnigentPreferencesValid,omnigentNativeOnly,omnigentPrepared} from '../ui/omnigent-helper.mjs';
import {cliPathClient,cliPreferenceOperation} from '../ui/cli-preferences-helper.mjs';

const prefs=()=>({version:1,codex:true,claude:true,codexConnection:'kilo',claudeConnection:'kilo',codexModels:'shared',claudeModels:'shared'});
const saved=()=>({revision:2,library:{schemaVersion:1,defaultModel:'vendor/second',models:[{id:'vendor/first',reasoningEffort:'low'},{id:'vendor/second'}]}});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(t,handler,{preferences=prefs(),state={connectionReady:true,baseURL:'http://127.0.0.1:8877/v1'},clock}={}) {
 const previous=globalThis.document,nodes=new Map();
 const element=()=>({children:[],textContent:'',value:'',checked:false,disabled:false,hidden:false,listeners:{},classList:{toggle(){}},replaceChildren(){this.children=[];},append(value){this.children.push(value);},addEventListener(name,callback){this.listeners[name]=callback;},dispatch(name){this.listeners[name]?.({target:this});}});
 globalThis.document={getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},createElement:element};
 t.after(()=>{globalThis.document=previous;});
 let current=structuredClone(preferences),profile={prepared:false,preferences:current,library:saved().library,fingerprint:'prepared-snapshot'};
 const calls=[];
 const helper=createOmnigentHelper({clock,api:async(path,body)=>{
  calls.push({path,body});
  const custom=await handler?.(path,body,{preferences:current,profile,setPreferences:value=>current=value,setProfile:value=>profile=value});
  if(custom!==undefined)return custom;
  if(path==='model-library')return saved();
  if(path==='clients/omnigent/preferences'){if(body)current=structuredClone(body);return {preferences:structuredClone(current),requiresPreparation:!!body};}
  if(path==='clients/omnigent'){if(body)profile={...profile,prepared:true,preferences:body.preferences,library:body.library??{schemaVersion:1,defaultModel:'',models:[]}};return structuredClone(profile);}
  if(path==='clients/omnigent/stop'){profile={...profile,running:false};return {ok:true};}
  throw new Error('unexpected API '+path);
 },onChange(){}});
 t.after(()=>helper.dispose());
 helper.render({state,language:'en'});
 return {helper,nodes,calls};
}

test('Omnigent preferences require explicit two-harness choices and no optional providers',()=>{
 assert.equal(omnigentPreferencesValid(prefs()),true);
 for(const patch of [{version:0},{codex:null},{claude:'true'},{codexConnection:''},{claudeConnection:'proxy'},{codexModels:'all'}])assert.equal(omnigentPreferencesValid({...prefs(),...patch}),false);
 assert.equal(omnigentNativeOnly({...prefs(),codexConnection:'native',claude:false}),true);
 assert.equal(omnigentNativeOnly({...prefs(),codexConnection:'native'}),false);
 assert.equal(cliPathClient('omnigent'),true);
 assert.equal(cliPreferenceOperation('clients/omnigent',{},'POST'),'omnigent');
 assert.equal(cliPreferenceOperation('clients/omnigent/stop',{},'POST'),'');
});

test('prepared identity binds preferences, shared effort and a nonempty fingerprint',()=>{
 const source=saved(),profile={prepared:true,preferences:prefs(),library:structuredClone(source.library),fingerprint:'exact'};
 assert.equal(omnigentPrepared(source,profile,prefs()),true);
 assert.equal(omnigentPrepared(source,{...profile,fingerprint:''},prefs()),false);
 profile.library.models[0].reasoningEffort='high';assert.equal(omnigentPrepared(source,profile,prefs()),false);
 assert.equal(omnigentPrepared(source,{...profile,preferences:{...prefs(),claude:false}},prefs()),false);
});

test('native-only prepares with no shared library or gateway and ignores unrelated connection edits',async t=>{
 const native={...prefs(),codexConnection:'native',claude:false};
 const {helper,calls,nodes}=fixture(t,path=>{if(path==='model-library')throw new Error('library recovery');},{preferences:native,state:{connectionReady:false}});
 await tick();
 assert.equal(helper.launchState().valid,true);
 const before=helper.launchState().fingerprint;
 helper.render({state:{connectionReady:false,baseURL:'http://unrelated.invalid',localKey:'unrelated'},language:'es'});
 assert.equal(helper.launchState().fingerprint,before);
 assert.equal(nodes.get('omnigent-prepare').disabled,false);
 await helper.launchState().prepare();
 assert.deepEqual(calls.filter(call=>call.path==='clients/omnigent'&&call.body).map(call=>call.body),[{preferences:native}]);
});

test('all-off is explicitly blocked, while a disabled Kilo choice does not require its gateway',async t=>{
 const {helper,nodes}=fixture(t,null,{preferences:{...prefs(),codex:false,claude:false},state:{connectionReady:false}});await tick();
 assert.equal(helper.launchState().nativeOnly,true);assert.equal(helper.launchState().valid,false);assert.equal(nodes.get('omnigent-prepare').disabled,true);
 await assert.rejects(helper.launchState().prepare(),/Enable Codex or Claude/);
});

test('external preference edits are rejected before any profile write',async t=>{
 let mutate=false;
 const {helper,calls}=fixture(t,(path,body,{preferences})=>{if(mutate&&path==='clients/omnigent/preferences'&&!body)return {preferences:{...preferences,claude:false}};});await tick();mutate=true;
 await assert.rejects(helper.launchState().prepare(),/settings changed/);
 assert.equal(calls.filter(call=>call.path==='clients/omnigent'&&call.body).length,0);
});

test('concurrent shared model changes reject preparation even after a successful backend reply',async t=>{
 let changed=false;
 const {helper}=fixture(t,(path,body)=>{if(path==='clients/omnigent'&&body){changed=true;return {prepared:true,preferences:body.preferences,library:body.library,fingerprint:'exact'};}if(changed&&path==='model-library'){const source=saved();source.library.models[0].reasoningEffort='high';return source;}});await tick();
 await assert.rejects(helper.launchState().prepare(),/changed while preparing/);
});

test('failed preference saves keep an unconfirmed draft and block opening until retry succeeds',async t=>{
 let fail=true;
 const {helper,nodes}=fixture(t,(path,body)=>{if(path==='clients/omnigent/preferences'&&body&&fail)throw new Error('disk full');});await tick();
 await helper.savePreferences({...prefs(),claudeConnection:'native'});
 assert.equal(helper.preferences().claudeConnection,'kilo');assert.equal(helper.launchState().valid,false);assert.equal(nodes.get('omnigent-settings-retry').hidden,false);
 fail=false;nodes.get('omnigent-settings-retry').dispatch('click');await tick();
 assert.equal(helper.preferences().claudeConnection,'native');assert.equal(nodes.get('omnigent-settings-retry').hidden,true);
});

test('running workspace reuses only the exact prepared profile and stops only its own endpoint',async t=>{
 let running=true;
 const {helper,nodes,calls}=fixture(t,(path,body,{profile})=>{if(path==='clients/omnigent/stop'){running=false;return {ok:true};}if(path==='clients/omnigent'&&!body)return {...profile,prepared:true,running};});await tick();
 assert.equal(nodes.get('omnigent-prepare').disabled,true);assert.equal(nodes.get('omnigent-stop').hidden,false);
 await helper.launchState().prepare();assert.equal(calls.filter(call=>call.path==='clients/omnigent'&&call.body).length,0);
 await helper.stop();assert.deepEqual(calls.find(call=>call.path==='clients/omnigent/stop').body,{});assert.equal(nodes.get('omnigent-stop').hidden,true);
 assert.ok(calls.every(call=>!call.path.includes('t3-code')&&!call.path.includes('synara')));
});

test('Omnigent backend notices translate exactly and unknown details are preserved',async()=>{
 const {translate}=await import('../ui/i18n.mjs');
 const english='Omnigent is prepared with your selected connections and models.';
 const spanish=translate(english,'es');assert.notEqual(spanish,english);assert.equal(translate(spanish,'en'),english);
 assert.equal(translate('unknown diagnostic /synthetic/path','es'),'unknown diagnostic /synthetic/path');
});

function testClock(){let now=0,id=0;const pending=new Map();return {now:()=>now,setTimeout(fn){const key=++id;pending.set(key,fn);return key;},clearTimeout(key){pending.delete(key);},async step(ms=1000){now+=ms;const tasks=[...pending.values()];pending.clear();for(const fn of tasks)fn();await tick();},pending:()=>pending.size};}

test('launch confirmation reloads running status and observes a delayed startup failure',async t=>{
 const clock=testClock();let launched=false,failed=false;
 const {helper,nodes,calls}=fixture(t,(path,body,{profile})=>{if(path==='clients/omnigent'&&!body)return {...profile,running:launched&&!failed,message:failed?'The private Omnigent workspace could not start. Check the selected installation and open it again.':''};},{clock});await tick();
 assert.equal(nodes.get('omnigent-stop').hidden,true);
 launched=true;await helper.launchConfirmed();
 assert.equal(nodes.get('omnigent-stop').hidden,false);assert.equal(nodes.get('omnigent-account-codex').disabled,true);
 failed=true;await clock.step();
 assert.equal(nodes.get('omnigent-stop').hidden,true);assert.match(nodes.get('omnigent-status').textContent,/could not start/);assert.equal(clock.pending(),0);
 assert.equal(calls.filter(c=>c.path==='clients/omnigent'&&c.body).length,0);
});

test('startup status observation is bounded and cannot overwrite a preference draft',async t=>{
 const clock=testClock();const {helper,nodes,calls}=fixture(t,null,{clock});await tick();const before=calls.length;
 await helper.launchConfirmed();await helper.savePreferences({...prefs(),claude:false});
 assert.equal(calls.filter(c=>c.body).length,0);assert.equal(nodes.get('omnigent-account-claude').disabled,true);
 for(let i=0;i<50;i++)await clock.step();
 const statusCalls=calls.slice(before).filter(c=>c.path==='clients/omnigent');assert.equal(statusCalls.length,50);assert.equal(clock.pending(),0);assert.equal(nodes.get('omnigent-account-claude').disabled,false);
});

test('confirmed stop cancels the bounded launch status observer',async t=>{
 const clock=testClock();let running=false;
 const {helper,calls}=fixture(t,(path,body,{profile})=>{if(path==='clients/omnigent/stop'){running=false;return {ok:true};}if(path==='clients/omnigent'&&!body)return {...profile,running};},{clock});await tick();running=true;
 await helper.launchConfirmed();assert.equal(clock.pending(),1);await helper.stop();assert.equal(clock.pending(),0);const count=calls.length;await clock.step();assert.equal(calls.length,count);
});
