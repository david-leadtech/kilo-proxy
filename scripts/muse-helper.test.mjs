import test from 'node:test';
import assert from 'node:assert/strict';
import {createMuseHelper,musePrepared,musePreferencesValid} from '../ui/muse-helper.mjs';

const tick=()=>new Promise(resolve=>setImmediate(resolve));
const library=()=>({revision:1,library:{schemaVersion:1,defaultModel:'vendor/exact',models:[{id:'vendor/exact',displayName:'Exact shared model',reasoningEffort:'high'}]}});
function fixture(t,{mode='kilo',supported=true,models=library(),intercept=()=>undefined}={}){
 const previous=globalThis.document,nodes=new Map(),writes=[];
 const element=()=>({textContent:'',value:'',disabled:false,hidden:false,children:[],listeners:{},classList:{toggle(){}},replaceChildren(){this.children=[];},append(x){this.children.push(x);},addEventListener(n,f){this.listeners[n]=f;},dispatch(n){this.listeners[n]?.({target:this});}});
 globalThis.document={getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},createElement:element};t.after(()=>globalThis.document=previous);
 const state={preferences:{version:1,connection:mode},path:'/selected/custom muse',models,supported};
 const profile=()=>({preferences:structuredClone(state.preferences),library:state.models?.library,effectiveModels:[{id:'vendor/exact',name:'Exact shared model'}],resolvedExecutable:state.path,prepared:true,kiloSupported:state.supported});
 const api=async(path,body)=>{
  const changed=intercept(path,body,state);if(changed!==undefined)return await changed;
  if(path==='model-library'){if(state.models instanceof Error)throw state.models;return structuredClone(state.models);}
  if(path==='clients/muse/preferences'){if(body){writes.push({path,body:structuredClone(body)});state.preferences=structuredClone(body);}return {preferences:structuredClone(state.preferences)};}
  if(path==='clients/muse'){if(body)writes.push({path,body:structuredClone(body)});return profile();}
  throw new Error(path);
 };
 const helper=createMuseHelper({api});const render=(connected=true,language='en')=>helper.render({state:{connectionReady:connected,baseURL:'http://127.0.0.1:8877/v1',localKey:'synthetic-local-key'},language,launchInfo:{clients:{muse:{path:state.path}}}});
 render();return {helper,nodes,writes,state,render};
}

test('Muse preferences and prepared status require exact mode, selected binary and shared library',()=>{
 assert.equal(musePreferencesValid({version:1,connection:'kilo'}),true);
 for(const connection of [undefined,null,'','other'])assert.equal(musePreferencesValid({version:1,connection}),false);
 const saved=library(),prefs={version:1,connection:'kilo'},profile={preferences:prefs,library:saved.library,prepared:true,resolvedExecutable:'/exact'};
 assert.equal(musePrepared(saved,profile,prefs,'/exact'),true);
 assert.equal(musePrepared(saved,profile,prefs,'/other'),false);
 assert.equal(musePrepared(saved,{...profile,library:{...saved.library,defaultModel:'other'}},prefs,'/exact'),false);
 const native={version:1,connection:'native'};
 assert.equal(musePrepared(null,{prepared:true,preferences:native,resolvedExecutable:'/exact'},native,'/exact'),true);
});

test('Muse Kilo alone requires gateway and saved Models while native Meta bypasses both',async t=>{
 const f=fixture(t);await tick();f.render(false);assert.equal(f.helper.launchState().valid,false);assert.match(f.helper.launchState().reason,/Connect Kilo/);
 f.render(true);assert.equal(f.helper.launchState().valid,true);
 f.state.models={library:{schemaVersion:1,defaultModel:'',models:[]}};await f.helper.reload();assert.match(f.helper.launchState().reason,/save shared models/);
 f.state.models=new Error('unrelated library recovery');await f.helper.savePreferences({version:1,connection:'native'});await f.helper.reload();f.render(false,'es');
 assert.equal(f.helper.launchState().nativeOnly,true);assert.equal(f.helper.launchState().valid,true);assert.match(f.nodes.get('muse-account-help').textContent,/Usa \/login en Muse/);assert.match(f.nodes.get('muse-account-help').textContent,/Kilo no inicia sesión en Meta/);
});

test('Kilo preparation preserves exact IDs and custom executable without writing shared Models or Meta credentials',async t=>{
 const f=fixture(t);await tick();await f.helper.launchState().prepare();
 assert.deepEqual(f.writes,[{path:'clients/muse',body:{preferences:{version:1,connection:'kilo'},museExecutable:'/selected/custom muse',library:library().library}}]);
 assert.deepEqual(f.helper.launchState().museConnection,'kilo');assert.equal(f.nodes.get('muse-models').children[0].textContent,'Exact shared model · vendor/exact');assert.equal(f.nodes.get('muse-account').textContent,'Kilo Proxy · Muse Code');
});

test('an unacknowledged mode save remains pending and exact retry cannot launch the old Meta account',async t=>{
 let omit=true;
 const f=fixture(t,{mode:'native',intercept:(path,body,state)=>{if(path==='clients/muse/preferences'&&body&&omit){state.preferences={...body};return {preferences:{version:1}};}}});await tick();
 await f.helper.savePreferences({version:1,connection:'kilo'});assert.equal(f.helper.preferences().connection,'native');assert.equal(f.helper.launchState().valid,false);assert.equal(f.nodes.get('muse-retry').hidden,false);
 await assert.rejects(f.helper.launchState().prepare(),/confirm/);assert.equal(f.writes.length,0);
 omit=false;f.nodes.get('muse-retry').dispatch('click');await tick();assert.equal(f.helper.preferences().connection,'kilo');assert.equal(f.nodes.get('muse-retry').hidden,true);
});

test('missing Kilo capability blocks only Kilo and the Meta connection remains usable',async t=>{
 const f=fixture(t);await tick();f.state.supported=undefined;await f.helper.reload();assert.match(f.helper.launchState().reason,/cannot use Kilo models/);
 await f.helper.savePreferences({version:1,connection:'native'});assert.equal(f.helper.launchState().valid,true);await f.helper.launchState().prepare();
 const nativeWrite=f.writes.find(row=>row.path==='clients/muse');assert.deepEqual(nativeWrite.body,{preferences:{version:1,connection:'native'},museExecutable:'/selected/custom muse'});
});

test('external Muse mode or selected executable changes block before profile writes',async t=>{
 const f=fixture(t);await tick();f.state.preferences={version:1,connection:'native'};await assert.rejects(f.helper.launchState().prepare(),/connection or executable changed/);assert.equal(f.writes.length,0);
 await f.helper.reload();f.state.path='/new/managed pointer';await assert.rejects(f.helper.launchState().prepare(),/connection or executable changed/);assert.equal(f.writes.length,0);
});

test('mode changes during retained preparation cannot claim success',async t=>{
 let release,entered;const started=new Promise(resolve=>entered=resolve);
 const f=fixture(t,{intercept:(path,body,state)=>{if(path==='clients/muse'&&body)return new Promise(resolve=>{release=()=>resolve({preferences:body.preferences,library:body.library,resolvedExecutable:body.museExecutable,prepared:true});entered();});}});await tick();
 const preparing=f.helper.launchState().prepare();await started;f.state.preferences={version:1,connection:'native'};release();await assert.rejects(preparing,/settings changed while preparing/);
});

test('a connection changed externally after save cannot leave a mismatching profile launchable',async t=>{
 let afterSave=false;
 const f=fixture(t,{mode:'native',intercept:(path,body,state)=>{
  if(path==='clients/muse/preferences'&&body){state.preferences={...body};afterSave=true;return {preferences:{...body}};}
  if(path==='clients/muse'&&!body&&afterSave){state.preferences={version:1,connection:'native'};return {preferences:state.preferences,resolvedExecutable:state.path,kiloSupported:true,prepared:true};}
 }});await tick();await f.helper.savePreferences({version:1,connection:'kilo'});
 assert.equal(f.helper.preferences().connection,'kilo');assert.equal(f.helper.launchState().valid,false);assert.match(f.helper.launchState().reason,/settings changed/);await assert.rejects(f.helper.launchState().prepare(),/settings changed/);
});
