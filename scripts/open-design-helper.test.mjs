import test from 'node:test';
import assert from 'node:assert/strict';
import {createOpenDesignHelper, openDesignLibrary, openDesignCanLaunch, openDesignEngines} from '../ui/open-design-helper.mjs';

const state = {baseURL:'http://127.0.0.1:8877/v1', localKey:'kl_local_synthetic', hasKey:true, orgId:'synthetic-team'};
const engines = {'codex-cli':{available:true}, claude:{available:true}, opencode:{available:true}};
const models = [{id:'vendor/first', name:'Friendly First'}, {id:'vendor/second', name:'Friendly Second'}];

test('Open Design profile payload retains exact IDs and initial choice without credentials or catalog prices', () => {
 const library = openDesignLibrary([{...models[0], apiKey:'secret', pricing:{input:5}, contextWindow:64000, maxOutputTokens:4096}, models[1]], 'vendor/second');
 assert.deepEqual(library, {schemaVersion:1, defaultModel:'vendor/second', models:[{id:'vendor/first', displayName:'Friendly First', contextPreset:'recommended', maxOutputTokens:4096}, {id:'vendor/second', displayName:'Friendly Second', contextPreset:'recommended'}]});
 assert.doesNotMatch(JSON.stringify(library), /secret|pricing|apiKey/);
});

test('saved custom reasoning and names survive profile preparation without fabricating unknown limits', () => {
 const levels = ['low', 'high'];
 const library = openDesignLibrary([{...models[0], displayName:'My model', reasoningCustom:true, reasoningLevels:levels, reasoningEffort:'high'}, {id:'manual/new'}]);
 assert.equal(library.models[0].displayName, 'My model');
 assert.equal(library.models[0].reasoningEffort, 'high');
 assert.deepEqual(library.models[0].reasoningLevels, levels);
 library.models[0].reasoningLevels.push('max');
 assert.deepEqual(levels, ['low', 'high']);
 assert.equal(library.models[1].contextWindow, undefined);
 assert.equal(library.models[1].maxOutputTokens, undefined);
});

test('launch requires a supported installed engine, a selected model and completed account connection', () => {
 const library = openDesignLibrary(models);
 assert.deepEqual(openDesignEngines, ['codex-cli', 'claude', 'opencode']);
 for (const engine of openDesignEngines) assert.equal(openDesignCanLaunch(state, engine, engines, library), true);
 for (const missing of [{hasKey:false}, {orgId:''}, {orgId:'   '}, {auth:{status:'pending'}}, {auth:{status:'starting'}}]) assert.equal(openDesignCanLaunch({...state, ...missing}, 'codex-cli', engines, library), false);
 assert.equal(openDesignCanLaunch(state, 'codex-cli', {'codex-cli':{available:false}}, library), false);
 assert.equal(openDesignCanLaunch(state, 'unsupported', engines, library), false);
 assert.equal(openDesignCanLaunch(state, 'codex-cli', engines, openDesignLibrary()), false);
 assert.equal(openDesignCanLaunch(undefined, 'codex-cli', engines, library), false);
 assert.equal(openDesignCanLaunch({...state, running:false}, 'codex-cli', engines, library), true);
});

test('selection drops malformed and duplicate IDs and recovers a removed initial model', () => {
 const library = openDesignLibrary([null, {id:44}, {id:'bad\nmodel'}, ...models, models[0]], 'removed/model');
 assert.deepEqual(library.models.map(model => model.id), ['vendor/first', 'vendor/second']);
 assert.equal(library.defaultModel, 'vendor/first');
 assert.equal(openDesignLibrary(Array.from({length:60}, (_, i) => ({id:'vendor/'+i}))).models.length, 50);
});

const tick = () => new Promise(resolve => setImmediate(resolve));
function helperFixture(t, api) {
 const previous = globalThis.document, nodes = new Map();
 const element = () => ({children:[], textContent:'', value:'', checked:false, disabled:false, dataset:{}, scrollTop:0, classList:{toggle(){}},
  ownerDocument:globalThis.document, replaceChildren(){this.children=[];}, append(...items){this.children.push(...items);}, setAttribute(){}, setCustomValidity(){},
  listeners:{}, addEventListener(name,callback){this.listeners[name]=callback;}});
 globalThis.document = {getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);}, createElement:element};
 t.after(() => {globalThis.document=previous;});
 const helper = createOpenDesignHelper({api, refreshCatalog(){}});
 helper.render({state, language:'en', catalog:[]});
 return {helper,nodes};
}
const profile = (available, model = 'vendor/first') => ({engine:'codex-cli', engines:{'codex-cli':{available},claude:{available:false},opencode:{available:false}},
 library:{schemaVersion:1,defaultModel:model,models:[{id:model,displayName:model}]}});

test('a CLI path reload discards the old in-flight engine result and checks the current executable', async t => {
 const pending = [];
 const {helper,nodes} = helperFixture(t, async () => new Promise(resolve => pending.push(resolve)));
 assert.equal(pending.length,1);
 helper.reload();
 assert.equal(helper.launchState().valid,false);
 pending[0]({...profile(false,'vendor/stale'),engine:'claude'});
 await tick();
 assert.equal(pending.length,2);
 assert.equal(helper.launchState().working,true);
 assert.equal(helper.launchState().count,0);
 assert.equal(helper.launchState().engine,'codex-cli');
 assert.match(nodes.get('open-design-engine-status').textContent,/Checking installed CLI engines/);
 pending[1](profile(true));
 await tick();
 assert.equal(helper.launchState().valid,true);
 assert.equal(helper.launchState().count,1);
 assert.equal(helper.launchState().working,false);
 assert.match(nodes.get('open-design-engine-status').textContent,/Installed/);
});

test('a CLI path reload during preparation runs afterward and preserves the selected model library', async t => {
 let reads=0, releasePrepare, releaseReload;
 const writes=[];
 const {helper} = helperFixture(t, async (path,body) => {
  if(body){writes.push(body);if(writes.length===1)await new Promise(resolve=>{releasePrepare=resolve;});return {};}
  if(++reads===1)return profile(true);
  return new Promise(resolve=>{releaseReload=resolve;});
 });
 await tick();
 const prepare=helper.launchState().prepare();
 helper.reload();
 assert.equal(helper.launchState().valid,false);
 releasePrepare();
 await prepare;
 assert.equal(reads,2);
 assert.equal(helper.launchState().working,true);
 assert.equal(helper.launchState().valid,false);
 releaseReload(profile(true,'vendor/later'));
 await tick();
 assert.equal(helper.launchState().valid,true);
 assert.deepEqual(writes[0].library.models.map(model=>model.id),['vendor/first']);
 await helper.launchState().prepare();
 assert.deepEqual(writes[1].library.models.map(model=>model.id),['vendor/first']);
});
