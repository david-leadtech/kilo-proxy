import test from 'node:test';
import assert from 'node:assert/strict';
import {createOpenCodeVersionHelper} from '../ui/opencode-version-helper.mjs';

const source=()=>({revision:1,library:{schemaVersion:1,defaultModel:'vendor/model',models:[{id:'vendor/model',displayName:'Coding model',contextWindow:128000,reasoningEffort:'high'}]}});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function fixture(t,api){
 const previous=globalThis.document,nodes=new Map(),messages=[];
 const element=()=>({children:[],textContent:'',value:'',disabled:false,hidden:false,listeners:{},classList:{toggle(){}},replaceChildren(){this.children=[]},append(value){this.children.push(value)},addEventListener(name,callback){this.listeners[name]=callback},dispatch(name){return this.listeners[name]?.({target:this})}});
 globalThis.document={getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id)},createElement:element};
 t.after(()=>{globalThis.document=previous});
 const helper=createOpenCodeVersionHelper({api,notify:(message,error)=>messages.push({message,error})});
 helper.render({language:'en',state:{connectionReady:true,baseURL:'http://127.0.0.1:8877/v1',localKey:'do-not-show-local-secret'}});
 return {helper,nodes,messages};
}

test('OpenCode 2 uses an immutable shared-library snapshot and its own profile endpoint',async t=>{
 let library=source();const writes=[];
 const {helper,nodes}=fixture(t,async(path,body)=>{if(body)writes.push({path,body});return path==='clients/opencode/preferences'?{executable1:'',executable2:'',version:1,major:2}:path==='model-library'?structuredClone(library):{prepared:!!body,configPath:'/private/opencode2/home/config'};});
 await tick();const state=helper.launchState({});assert.equal(state.id,'opencode2');assert.equal(state.count,1);assert.equal(state.ready,false);assert.equal(state.valid,true);
 await state.prepare();assert.deepEqual(writes,[{path:'clients/opencode2',body:{library:library.library}}]);
 library.library.models[0].displayName='Later';assert.equal(writes[0].body.library.models[0].displayName,'Coding model');
 assert.doesNotMatch([...nodes.values()].map(node=>node.textContent).join(' '),/do-not-show-local-secret|\/private\/opencode2/);
 assert.match(nodes.get('opencode2-install').textContent,/manually/);
 helper.render({language:'es',state:{connectionReady:false}});assert.equal(helper.launchState({}).valid,false);assert.match(nodes.get('opencode-version-help').textContent,/Se conservan.*cuentas y chats/);
});

test('a pending or failed version change cannot open either version and retry preserves the requested choice and paths',async t=>{
 let preferences={executable1:'/custom/one',executable2:'/custom/two',version:1,major:1},fail=true,release;const writes=[];
 const {helper,nodes}=fixture(t,async(path,body)=>{
  if(path==='clients/opencode/preferences'){if(body){writes.push({...body});await new Promise(resolve=>{release=resolve});if(fail)throw new Error('synthetic save failed');preferences={...body}}return {...preferences}}
  return path==='model-library'?source():{};
 });
 await tick();nodes.get('opencode-version').value='2';nodes.get('opencode-version').dispatch('change');
 assert.equal(nodes.get('opencode-version').value,'2');assert.equal(nodes.get('opencode-version').disabled,true);assert.equal(helper.major(),1);assert.equal(helper.launchState({valid:true}).valid,false);
 release();await tick();assert.equal(helper.major(),1);assert.equal(nodes.get('opencode-version').value,'1');assert.equal(nodes.get('opencode-version-retry').hidden,false);assert.equal(helper.blocked(),true);
 fail=false;nodes.get('opencode-version-retry').dispatch('click');assert.equal(writes.length,2);assert.deepEqual(writes[0],writes[1]);release();await tick();await tick();
 assert.equal(helper.major(),2);assert.equal(helper.launchState({}).valid,true);assert.equal(nodes.get('opencode-version').disabled,false);
 assert.equal(writes[1].executable1,'/custom/one');assert.equal(writes[1].executable2,'/custom/two');assert.equal(nodes.get('opencode-executable').value,'/custom/two');
});

test('an external version change blocks OpenCode 1 preparation and redirects the visible selection',async t=>{
 let preferences={executable1:'',executable2:'',version:1,major:1},prepares=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...preferences}:path==='model-library'?source():{});
 await tick();const state=helper.launchState({id:'opencode',valid:true,count:1,prepare:async()=>{prepares++}});
 preferences.major=2;await assert.rejects(state.prepare(),/version or models changed/);assert.equal(prepares,0);assert.equal(helper.major(),2);assert.equal(helper.launchState({}).id,'opencode2');
});

test('library changes before preparation require review and never write stale OpenCode 2 settings',async t=>{
 let library=source();const writes=[];
 const {helper}=fixture(t,async(path,body)=>{if(body)writes.push(body);return path==='clients/opencode/preferences'?{executable1:'',executable2:'',version:1,major:2}:path==='model-library'?structuredClone(library):{};});
 await tick();library.library.models[0].displayName='Changed';await assert.rejects(helper.launchState({}).prepare(),/version or models changed/);assert.equal(writes.length,0);
 await helper.launchState({}).prepare();assert.equal(writes[0].library.models[0].displayName,'Changed');
});

test('a version change while the profile is being written never reports the old version as prepared',async t=>{
 let major=2;
 const {helper,messages}=fixture(t,async(path,body)=>{if(body)major=1;return path==='clients/opencode/preferences'?{executable1:'',executable2:'',version:1,major}:path==='model-library'?source():{prepared:!!body};});
 await tick();await assert.rejects(helper.launchState({}).prepare(),/version or models changed/);assert.equal(messages.length,0);
});

test('executable paths require an explicit save, keep the other version and can return to automatic detection',async t=>{
 let preferences={version:1,major:1,executable1:'/original/one',executable2:'/original/two'};const writes=[];
 const {helper,nodes}=fixture(t,async(path,body)=>{
  if(path==='clients/opencode/preferences'){if(body){writes.push({...body});preferences={...body};}return {...preferences};}
  return path==='model-library'?source():{};
 });
 await tick();assert.equal(nodes.get('opencode-executable').value,'/original/one');
 nodes.get('opencode-executable').value='relative/opencode';nodes.get('opencode-executable').dispatch('input');
 assert.equal(helper.blocked(),true);assert.equal(nodes.get('opencode-version').disabled,true);assert.equal(writes.length,0);
 nodes.get('opencode-executable-save').dispatch('click');assert.equal(writes.length,0);assert.match(nodes.get('opencode-version-status').textContent,/absolute path/);
 nodes.get('opencode-executable').value='/new path/opencode';nodes.get('opencode-executable').dispatch('input');nodes.get('opencode-executable-save').dispatch('click');await tick();
 assert.deepEqual(writes[0],{version:1,major:1,executable1:'/new path/opencode',executable2:'/original/two'});assert.equal(helper.blocked(),false);
 nodes.get('opencode-version').value='2';nodes.get('opencode-version').dispatch('change');await tick();await tick();assert.equal(nodes.get('opencode-executable').value,'/original/two');
 nodes.get('opencode-executable-auto').dispatch('click');await tick();await tick();
 assert.equal(preferences.executable1,'/new path/opencode');assert.equal(preferences.executable2,'');assert.equal(nodes.get('opencode-executable').value,'');assert.equal(helper.blocked(),false);
 helper.render({language:'es',state:{connectionReady:true},launchInfo:{clients:{opencode2:{path:'/resolved/opencode2'}}}});
 assert.match(nodes.get('opencode-executable-help').textContent,/T3 Code usa las mismas rutas/);assert.equal(nodes.get('opencode-executable-resolved').textContent,'Ejecutable detectado: /resolved/opencode2');
});

test('an externally changed executable blocks preparation even when the selected major is unchanged',async t=>{
 let preferences={version:1,major:1,executable1:'/first/one',executable2:''},prepares=0;
 const {helper,nodes}=fixture(t,async path=>path==='clients/opencode/preferences'?{...preferences}:path==='model-library'?source():{});
 await tick();const state=helper.launchState({id:'opencode',valid:true,count:1,prepare:async()=>{prepares++}});
 preferences.executable1='/changed/one';await assert.rejects(state.prepare(),/version or models changed/);assert.equal(prepares,0);assert.equal(nodes.get('opencode-executable').value,'/changed/one');
});

test('an executable change during preparation prevents a success notice and updates the visible saved path',async t=>{
 let preferences={version:1,major:2,executable1:'',executable2:'/first/two'};
 const {helper,nodes,messages}=fixture(t,async(path,body)=>{
  if(path==='clients/opencode/preferences')return {...preferences};
  if(body)preferences.executable2='/changed/two';
  return path==='model-library'?source():{prepared:!!body};
 });
 await tick();await assert.rejects(helper.launchState({}).prepare(),/version or models changed/);assert.equal(messages.length,0);assert.equal(nodes.get('opencode-executable').value,'/changed/two');
});
