import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const fixture = JSON.parse(await readFile(process.argv[2], 'utf8'));
const context = vm.createContext({ providerDefaultModel: provider => `${provider}-native-default` });
vm.runInContext(`class AgentGatewayTargetError extends Error { constructor(code,message){super(message);this.code=code;} }
${fixture.helpers.replace('__KILO_SYNARA_GATEWAY_MODELS__', JSON.stringify(fixture.models))}
globalThis.api={kiloSynaraGatewayAccount,kiloSynaraDefaultModel,kiloSynaraMergeAccountModels,kiloSynaraDiscoveryModels,kiloSynaraOpenCodeSelectionError};`,context);
const api = context.api;
const customModels = fixture.models.claudeAgent.map(row => row.slug);
const availability = { instanceId: 'kilo_claude_proxy', provider:'claudeAgent', enabled:true, available:true, customModels };
const input = { instanceId: availability.instanceId, provider:'claudeAgent', availability };
assert.equal(api.kiloSynaraDefaultModel(input),'claude-opus-5-5');
const catalog = api.kiloSynaraMergeAccountModels(input,[{slug:'claude-opus-4-8'},{slug:'default'}]);
assert.deepEqual(Array.from(catalog,row=>row.slug),['claude-opus-5-5','claude-sonnet-5-5','claude-fable-5-1']);
for (const effort of ['low','high']) {
 const target={provider:'claudeAgent',instanceId:availability.instanceId,model:'claude-opus-5-5',options:{effort}};
 assert.equal(api.kiloSynaraGatewayAccount({target,availability}),target,'Changing native effort must preserve the selected account/model');
}
for (const [model,effort] of [['claude-opus-5-5','medium'],['claude-sonnet-5-5','high']]) {
 assert.throws(()=>api.kiloSynaraGatewayAccount({target:{provider:'claudeAgent',instanceId:availability.instanceId,model,options:{effort}},availability}),error=>error.code==='model_option_unavailable');
}
assert.equal(api.kiloSynaraMergeAccountModels({instanceId:'kilo_codex_proxy',availability:{customModels:['native-default']}},[{slug:'native-default'}]).length,0,'A disabled Kilo account must not fall back to native discovery');
const native=[{slug:'native-model'}];
assert.equal(api.kiloSynaraMergeAccountModels({instanceId:'kilo_claude_normal',availability:{customModels:[]}},native)[0],native[0],'Native Claude discovery must remain intact');
const openModels = fixture.models.opencode;
const openAvailability = {instanceId:'kilo_opencode_proxy',provider:'opencode',enabled:true,available:true,customModels:openModels.map(row=>row.slug)};
const openInput = {instanceId:openAvailability.instanceId,provider:'opencode',availability:openAvailability};
assert.equal(api.kiloSynaraDefaultModel(openInput),openModels[0].slug);
const discovered = [...openModels.map(row=>({slug:row.slug,optionDescriptors:[{id:'variant',type:'select',options:['low','high','evil'].map(id=>({id}))},{id:'agent',type:'select',options:[{id:'build'}]}]})),{slug:'foreign/not-prepared'}];
discovered[0].optionDescriptors[0].currentValue='evil';
discovered[0].optionDescriptors[0].options[0].isDefault=true;
discovered[0].supportedReasoningEfforts=['low','high','evil'].map(value=>({value}));
discovered[0].defaultReasoningEffort='evil';
const openCatalog = api.kiloSynaraMergeAccountModels(openInput,discovered);
assert.deepEqual(Array.from(openCatalog,row=>row.slug),openModels.map(row=>row.slug));
assert.deepEqual(Array.from(openCatalog[0].optionDescriptors[0].options,row=>row.id),['low','high']);
assert.equal(openCatalog[0].optionDescriptors[1].options[0].id,'build');
assert.equal(openCatalog[0].optionDescriptors[0].currentValue,'high');
assert.deepEqual(Array.from(openCatalog[0].supportedReasoningEfforts,row=>row.value),['low','high']);
assert.equal(openCatalog[0].defaultReasoningEffort,'high');
assert.equal(Object.hasOwn(openCatalog[0].optionDescriptors[0].options[0],'isDefault'),false,'A previous native default must not serialize invalid isDefault:false');
assert.equal(openCatalog[0].optionDescriptors[0].options[1].isDefault,true);
const original=JSON.stringify(discovered);
const result={models:discovered,source:'live',cached:true,error:'retained diagnostics'};
const filtered=api.kiloSynaraDiscoveryModels(openInput,result);
assert.deepEqual(Array.from(filtered.models,row=>row.slug),openModels.map(row=>row.slug),'The UI discovery response must use the exact prepared catalog');
assert.equal(filtered.source,result.source);
assert.equal(filtered.cached,true);
assert.equal(filtered.error,result.error);
assert.equal(JSON.stringify(discovered),original,'Filtering must not mutate the shared discovery cache');
assert.equal(api.kiloSynaraDiscoveryModels({provider:'opencode',instanceId:'opencode'},result),result);
assert.equal(api.kiloSynaraDiscoveryModels({provider:'claudeAgent',instanceId:'kilo_opencode_proxy'},result),result);
const emptyIndex=filtered.models.findIndex(model=>model.slug===openModels[1].slug);
assert.equal(filtered.models[emptyIndex].optionDescriptors[0].options.length,0,'Explicit empty variants must suppress upstream levels');
assert.equal(filtered.models[emptyIndex].optionDescriptors[0].currentValue,undefined);
const legacyEfforts = api.kiloSynaraDiscoveryModels(openInput,{models:openModels.map(row=>({slug:row.slug,supportedReasoningEfforts:['low','high','evil'].map(value=>({value})),defaultReasoningEffort:'evil'}))}).models;
assert.equal(Object.hasOwn(legacyEfforts[0],'optionDescriptors'),false,'Legacy OpenCode discovery must keep the native variant-menu fallback');
assert.deepEqual(Array.from(legacyEfforts[0].supportedReasoningEfforts,row=>row.value),['low','high']);
assert.equal(legacyEfforts[0].defaultReasoningEffort,'high');
assert.equal(legacyEfforts[1].supportedReasoningEfforts.length,0);
assert.equal(legacyEfforts[1].defaultReasoningEffort,undefined);
const openTarget = {provider:'opencode',instanceId:openAvailability.instanceId,model:openModels[0].slug,options:{variant:'low'}};
assert.equal(api.kiloSynaraGatewayAccount({target:openTarget,availability:openAvailability}),openTarget);
assert.equal(api.kiloSynaraOpenCodeSelectionError(openAvailability.instanceId,openTarget),undefined);
assert.match(api.kiloSynaraOpenCodeSelectionError(openAvailability.instanceId,{...openTarget,model:'foreign/old-project-model'}),/previous model was not substituted/);
assert.match(api.kiloSynaraOpenCodeSelectionError(openAvailability.instanceId,undefined),/Choose a model/);
assert.match(api.kiloSynaraOpenCodeSelectionError(openAvailability.instanceId,{...openTarget,options:{variant:'evil'}}),/enabled reasoning level/);
assert.match(api.kiloSynaraOpenCodeSelectionError(openAvailability.instanceId,{...openTarget,model:openModels[1].slug,options:{variant:'high'}}),/enabled reasoning level/);
assert.equal(api.kiloSynaraOpenCodeSelectionError('opencode',{...openTarget,model:'foreign/old-project-model',options:{variant:'evil'}}),undefined,'Native accounts must keep their own selections');
assert.throws(()=>api.kiloSynaraGatewayAccount({target:{...openTarget,options:{variant:'evil'}},availability:openAvailability}),error=>error.code==='model_option_unavailable');
assert.equal(api.kiloSynaraMergeAccountModels({instanceId:'opencode',availability:{customModels:[]}},discovered).length,discovered.length,'Native OpenCode catalog must stay intact');
console.log('Synara policy: exact curated aliases, native effort levels and disabled-account catalogs passed');
