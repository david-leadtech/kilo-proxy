import test from 'node:test';
import assert from 'node:assert/strict';
import {createSynaraHelper, synaraNativeOnly, synaraPrepared, synaraPreferencesValid} from '../ui/synara-helper.mjs';

const saved = () => ({revision:2,library:{schemaVersion:1,defaultModel:'vendor/second',models:[{id:'vendor/first',displayName:'Friendly',reasoningEffort:'high'},{id:'vendor/second'}]}});
const preferences = () => ({version:1,codex:true,claude:true,kiloCodex:true,kiloClaude:true,codexModels:'shared',claudeModels:'shared',providers:[],openCode2:false,openCodeConnection:'native'});
const preferencesResponse = () => ({claudePickerSupported:true,preferences:preferences(),supportedProviders:[],openCode2Supported:false,requiresPreparation:false});
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture(t, api, options = {}) {
 const previous=globalThis.document,nodes=new Map();
 const element=()=>({children:[],textContent:'',value:'',checked:false,disabled:false,hidden:false,listeners:{},classList:{toggle(){}},replaceChildren(){this.children=[];},append(item){this.children.push(item);},addEventListener(name,callback){this.listeners[name]=callback;},dispatch(name){this.listeners[name]?.({target:this});}});
 globalThis.document={getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},createElement:element};
 t.after(()=>{globalThis.document=previous;});
 const helper=createSynaraHelper({api:async(path,body)=>path==='clients/synara/preferences'?(options.preferencesAPI ? Promise.resolve(options.preferencesAPI(body)).then(value=>({claudePickerSupported:true,...value})) : preferencesResponse()):Promise.resolve(api(path,body)).then(result => path==='clients/synara' ? {prepared:!!body,preferences:options.profilePreferences?.() ?? preferences(),...result} : result),onChange(){},onPreferencesConfirmed:options.onPreferencesConfirmed});
 helper.render({state:{connectionReady:true,baseURL:'http://127.0.0.1:8877/v1',localKey:'private-local-secret'},language:'en'});
 return {helper,nodes};
}

test('prepared status requires the current complete shared library',()=>{
 const source=saved(),profile={prepared:true,library:structuredClone(source.library)};
 assert.equal(synaraPrepared(source,profile),true);
 profile.library.models[0].reasoningEffort='low';
 assert.equal(synaraPrepared(source,profile),false);
 assert.equal(synaraPrepared({...source,recoveryRequired:true},profile),false);
});

test('native-only readiness requires explicit valid preferences and keeps native model ownership',()=>{
 const native={...preferences(),codex:false,claude:false,kiloCodex:false,kiloClaude:false,providers:['pi']};
 const profile={prepared:true,preferences:structuredClone(native),library:{schemaVersion:1,defaultModel:'',models:[]}};
 assert.equal(synaraNativeOnly(native),true);
 assert.equal(synaraNativeOnly({...native,version:0}),false);
 assert.equal(synaraNativeOnly({...native,kiloClaude:true}),false);
 assert.equal(synaraPrepared({recoveryRequired:true},profile,native),true);
 assert.equal(synaraPrepared(null,profile,{...native,kiloCodex:true}),false);
});

test('native Pi prepares without shared Models or gateway and ignores unrelated connection edits',async t=>{
 const native={...preferences(),codex:false,claude:false,kiloCodex:false,kiloClaude:false,providers:['pi']};
 const writes=[]; let libraryReads=0;
 const {helper,nodes}=fixture(t,async(path,body)=>{
  if(path==='model-library'){libraryReads++;throw new Error('unrelated library recovery');}
  if(body)writes.push(structuredClone(body));
  return {prepared:true,preferences:structuredClone(native),library:{schemaVersion:1,defaultModel:'',models:[]},effectiveCodexModels:[],effectiveClaudeModels:[]};
 },{preferencesAPI:async()=>({preferences:structuredClone(native),supportedProviders:[{id:'pi',label:'Pi'}]})});
 await tick();
 helper.render({state:{connectionReady:false},language:'en'});
 const state=helper.launchState();
 assert.equal(state.nativeOnly,true);
 assert.equal(state.count,0);
 assert.equal(state.valid,true);
 assert.equal(state.reason,'');
 assert.equal(nodes.get('synara-prepare').disabled,false);
 helper.render({state:{connectionReady:false,baseURL:'http://unrelated.invalid',localKey:'unrelated',orgId:'unrelated'},language:'en'});
 assert.equal(helper.launchState().fingerprint,state.fingerprint);
 await helper.launchState().prepare();
 assert.deepEqual(writes,[{preferences:native}]);
 assert.equal(libraryReads,1);
 assert.match(nodes.get('synara-prepared').textContent,/saved Synara accounts and models are prepared/);
});

test('native Pi detects external account changes before preparing or after a concurrent change',async t=>{
 let current={...preferences(),codex:false,claude:false,kiloCodex:false,kiloClaude:false,providers:['pi']};
 const native=structuredClone(current),writes=[];
 let changeDuringPrepare=false;
 const {helper}=fixture(t,async(path,body)=>{
  if(path==='model-library')return {recoveryRequired:true,library:{schemaVersion:1,defaultModel:'',models:[]}};
  if(body){writes.push(structuredClone(body));if(changeDuringPrepare)current={...current,kiloCodex:true};}
  return {prepared:true,preferences:structuredClone(native),library:{schemaVersion:1,defaultModel:'',models:[]}};
 },{preferencesAPI:async()=>({preferences:structuredClone(current),supportedProviders:[{id:'pi',label:'Pi'}]})});
 await tick();
 current={...current,kiloClaude:true};
 await assert.rejects(helper.launchState().prepare(),/settings changed/);
 assert.equal(writes.length,0);
 assert.equal(helper.launchState().nativeOnly,false);
 assert.equal(helper.launchState().valid,false);
 current=structuredClone(native);await helper.reload();changeDuringPrepare=true;
 await assert.rejects(helper.launchState().prepare(),/settings or connection changed/);
 assert.deepEqual(writes,[{preferences:native}]);
 assert.equal(helper.launchState().nativeOnly,false);
 assert.equal(helper.launchState().valid,false);
});

test('Synara refuses a library exceeding its 32 custom-model limit before writing', async t => {
 const source = saved(), writes = [];
 source.library.models = Array.from({length:33}, (_, i) => ({id:'vendor/model-'+i}));
 source.library.defaultModel = 'vendor/model-32';
 const {helper} = fixture(t, async (path, body) => {
  if (body) writes.push(body);
  return path === 'model-library' ? source : {};
 });
 await tick();
 assert.equal(helper.launchState().valid, false);
 assert.match(helper.launchState().reason, /32 shared models/);
 await assert.rejects(helper.launchState().prepare(), /32 shared models/);
 assert.equal(writes.length, 0);
 helper.render({state:{connectionReady:true},language:'es'});
 assert.match(helper.launchState().reason, /32 modelos compartidos/);
});

test('enabled accounts use an immutable common-library snapshot without exposing credentials or profile paths',async t=>{
 const source=saved(),calls=[];
 const {helper,nodes}=fixture(t,async(path,body)=>{calls.push({path,body});return path==='model-library'?source:{prepared:true,preferences:preferences(),library:structuredClone(source.library),profileDir:'/private/profile/secret',version:'0.0.45'};});
 await tick();
 assert.equal(helper.launchState().valid,true);
 assert.equal(helper.launchState().ready,false);
 await helper.launchState().prepare();
 const sent=calls.find(call=>call.body);
 assert.equal(sent.path,'clients/synara');
 assert.deepEqual(sent.body,{library:source.library,preferences:preferences()});
 source.library.models[0].displayName='Changed later';
 assert.equal(sent.body.library.models[0].displayName,'Friendly');
 assert.equal(calls.filter(call=>call.path==='model-library'&&call.body).length,0);
 assert.deepEqual(nodes.get('synara-models').children.map(node=>node.textContent),[]);
 assert.deepEqual(nodes.get('synara-agents').children.map(node=>node.textContent.split(' · ').slice(0,2).join(' · ')),['Codex · Existing Codex CLI login','Kilo Proxy · Codex','Claude · Existing Claude Code login','Kilo Proxy · Claude']);
 assert.match(nodes.get('synara-intro').textContent,/Kilo Proxy names and green account indicators/);
 assert.match(nodes.get('synara-compatibility').textContent,/new chat to switch between native and Kilo accounts/);
 assert.match(nodes.get('synara-requirements').textContent,/not Desktop app logins/);
 assert.match(nodes.get('synara-prepared').textContent,/saved Synara accounts and models are prepared/);
 assert.doesNotMatch([...nodes.values()].map(node=>node.textContent).join(' '),/private-local-secret|\/private\/profile\/secret/);
});

test('concurrent library changes block prepare or launch and a failed refresh clears stale data',async t=>{
 let source=saved(),fail=false;const writes=[];
 const {helper}=fixture(t,async(path,body)=>{if(fail)throw new Error('unreadable');if(body)writes.push(body);return path==='model-library'?structuredClone(source):{};});
 await tick();
 source.library.defaultModel='vendor/first';
 await assert.rejects(helper.launchState().prepare(),/shared models changed/);
 assert.equal(writes.length,0);
 await helper.launchState().prepare();
 assert.equal(writes[0].library.defaultModel,'vendor/first');
 fail=true;await helper.reload();
 assert.equal(helper.launchState().valid,false);
 assert.equal(helper.launchState().count,0);
 assert.match(helper.launchState().reason,/unreadable/);
});

test('connection edits invalidate prepared status and disconnect blocks preparation',async t=>{
 const source=saved();const writes=[];
 const {helper,nodes}=fixture(t,async(path,body)=>{if(body)writes.push(body);return path==='model-library'?source:{prepared:true,preferences:preferences(),library:source.library};});
 await tick();
 assert.match(nodes.get('synara-prepared').textContent,/saved Synara accounts and models are prepared/);
 helper.render({state:{connectionReady:true,baseURL:'http://127.0.0.1:9999/v1',localKey:'different'},language:'es'});
 assert.match(nodes.get('synara-prepared').textContent,/Prepara o abre/);
 assert.equal(nodes.get('synara-agents').children.length,4);
 assert.match(nodes.get('synara-intro').textContent,/nombres Kilo Proxy y los indicadores verdes/);
 assert.match(nodes.get('synara-agents').children[1].textContent,/^Kilo Proxy · Codex · /);
 assert.match(nodes.get('synara-agents').children[3].textContent,/^Kilo Proxy · Claude · /);
 helper.render({state:{connectionReady:false},language:'es'});
 await assert.rejects(helper.launchState().prepare(),/Conecta primero/);
 assert.equal(writes.length,0);
});

test('preparation and installation guidance translates to Spanish',async t=>{
 const source=saved();let fail=false;
 const {helper,nodes}=fixture(t,async(path,body)=>{if(body&&fail)throw new Error('Quit the Synara Kilo window before changing its models or connection. Your regular Synara window can stay open.');return path==='model-library'?source:{message:'Install Claude Code CLI before preparing Synara. Claude Desktop is a separate application.'};});
 await tick();
 helper.render({state:{connectionReady:true},language:'es'});
 assert.match(nodes.get('synara-status').textContent,/Instala Claude Code CLI/);
 fail=true;
 await assert.rejects(helper.launchState().prepare(),/Quit the Synara/);
 assert.match(nodes.get('synara-status').textContent,/Cierra la ventana Kilo de Synara/);
});

test('settings save a concrete account policy once, disable editing while pending, and expose only supported providers', async t => {
 let current=preferences(), release, posted=[];
 const support=[{id:'cursor',label:'Cursor'},{id:'opencode',label:'OpenCode'}];
 const {helper,nodes}=fixture(t, async path => path==='model-library'?saved():{preferences:structuredClone(current),prepared:true,library:saved().library,effectiveCodexModels:[{id:'openai/gpt-fixture',name:'Fixture GPT'}],effectiveClaudeModels:[{id:'anthropic/claude-opus-5.5',name:'Claude Opus 5.5'}]}, {preferencesAPI:async body=>{
  if(body){posted.push(structuredClone(body));await new Promise(resolve=>{release=resolve;});current=structuredClone(body);}
  return {preferences:structuredClone(current),requiresPreparation:!!body,supportedProviders:support,openCode2Supported:true};
 }});
 await tick();
 assert.deepEqual(nodes.get('synara-provider-options').children.map(label=>label.children[1].textContent),['Cursor','OpenCode']);
 assert.equal(nodes.get('synara-opencode-version-field').hidden,false);
 assert.equal(nodes.get('synara-opencode-version').disabled,true);
 assert.deepEqual(nodes.get('synara-models').children.map(row=>row.textContent),['Kilo Proxy · Codex · Fixture GPT · openai/gpt-fixture','Kilo Proxy · Claude · Claude Opus 5.5 · anthropic/claude-opus-5.5']);
 const codex=nodes.get('synara-account-codex');codex.checked=false;codex.dispatch('change');
 assert.equal(posted.length,1);assert.equal(posted[0].codex,false);assert.equal(posted[0].claude,true);
 assert.equal(codex.checked,false);assert.equal(helper.preferences().codex,true);
 assert.equal(nodes.get('synara-account-claude').disabled,true);
 assert.equal(helper.launchState().valid,false);
 await helper.savePreferences({...current,claude:false});assert.equal(posted.length,1);
 release();await tick();await tick();
 assert.equal(helper.preferences().codex,false);
 assert.equal(nodes.get('synara-account-codex').checked,false);
 assert.equal(nodes.get('synara-account-claude').disabled,false);
 assert.doesNotMatch(nodes.get('synara-agents').children.map(row=>row.textContent).join(' '),/Normal/);
 assert.equal(nodes.get('synara-agents').children.length,3);
});

test('a failed settings save retains the confirmed policy and retries exactly the requested policy', async t=>{
 let current=preferences(),fail=true,writes=[];
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{preferences:current,library:saved().library,prepared:true},{preferencesAPI:async body=>{
  if(body){writes.push(structuredClone(body));if(fail)throw new Error('synthetic disk unavailable');current=structuredClone(body);}
  return {preferences:current,supportedProviders:[],openCode2Supported:false,requiresPreparation:true};
 }});
 await tick();
 await helper.savePreferences({...current,claude:false});
 assert.equal(helper.preferences().claude,true);
 assert.equal(nodes.get('synara-settings-retry').hidden,false);
 assert.match(nodes.get('synara-settings-status').textContent,/disk unavailable/);
 assert.equal(helper.launchState().valid,false);
 fail=false;nodes.get('synara-settings-retry').dispatch('click');await tick();await tick();
 assert.equal(helper.preferences().claude,false);
 assert.deepEqual(writes[1],writes[0]);
 assert.equal(nodes.get('synara-settings-retry').hidden,true);
});

test('settings edits elsewhere invalidate a preparation snapshot before writing', async t=>{
 let current=preferences(),writes=[];
 const {helper}=fixture(t,async(path,body)=>{if(body)writes.push(body);return path==='model-library'?saved():{prepared:true,preferences:current,library:saved().library};},{preferencesAPI:async()=>({preferences:structuredClone(current),supportedProviders:[],requiresPreparation:false})});
 await tick();current.claude=false;
 await assert.rejects(helper.launchState().prepare(),/settings changed/);
 assert.equal(writes.length,0);
 assert.equal(helper.preferences().claude,false);
});

test('OpenCode selects one version and disabling its provider clears OpenCode 2 without changing other providers',async t=>{
 let current={...preferences(),providers:['cursor','opencode'],openCode2:true},writes=[];
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{prepared:false,preferences:current,library:saved().library},{preferencesAPI:async body=>{if(body){writes.push(structuredClone(body));current=structuredClone(body);}return {preferences:current,supportedProviders:[{id:'opencode',label:'OpenCode'}],openCode2Supported:true};}});
 await tick();assert.equal(nodes.get('synara-opencode-version').value,'2');assert.equal(nodes.get('synara-opencode-version').disabled,false);
 const checkbox=nodes.get('synara-provider-options').children[0].children[0];checkbox.checked=false;checkbox.dispatch('change');await tick();await tick();
 assert.deepEqual(writes[0].providers,['cursor']);assert.equal(writes[0].openCode2,false);
 assert.equal(helper.preferences().openCode2,false);
 assert.equal(nodes.get('synara-opencode-version').disabled,true);
 helper.render({state:{connectionReady:true},language:'es'});
 assert.match(nodes.get('synara-settings-help').textContent,/se conservan tus chats/);
 assert.match(nodes.get('synara-codex-official').textContent,/OpenAI y ChatGPT/);
 assert.match(nodes.get('synara-settings-status').textContent,/Ajustes guardados/);
});

test('official mode does not reject an unrelated shared library over the per-account custom limit',async t=>{
 const source=saved();source.library.models=Array.from({length:40},(_,i)=>({id:'vendor/model-'+i}));source.library.defaultModel='vendor/model-0';
 const prefs={...preferences(),codexModels:'official',claudeModels:'official'};
 const {helper}=fixture(t,async path=>path==='model-library'?source:{prepared:false,preferences:prefs,library:source.library},{preferencesAPI:async()=>({preferences:prefs,supportedProviders:[],requiresPreparation:true})});
 await tick();assert.equal(helper.launchState().valid,true);
});

test('saved choices absent from a newer build stay visible until disabled, without enabling unknown providers or OpenCode 2',async t=>{
 let current={...preferences(),providers:['cursor','opencode'],openCode2:true},writes=[];
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{prepared:false,preferences:current,library:saved().library},{preferencesAPI:async body=>{if(body){writes.push(structuredClone(body));current=structuredClone(body);}return {preferences:current,supportedProviders:[{id:'opencode',label:'OpenCode'},{id:'unknown',label:'Unknown provider'}],openCode2Supported:false};}});
 await tick();
 assert.deepEqual(nodes.get('synara-provider-options').children.map(label=>label.children[1].textContent),['OpenCode','Cursor · unavailable in this build']);
 assert.equal(nodes.get('synara-opencode-version').value,'2');assert.equal(nodes.get('synara-opencode-version-2').disabled,true);
 assert.match(nodes.get('synara-opencode-help').textContent,/cannot use the saved OpenCode 2 choice/);
 assert.equal(helper.launchState().valid,false);assert.match(helper.launchState().reason,/unavailable saved choices/);
 const cursor=nodes.get('synara-provider-options').children[1].children[0];assert.equal(cursor.checked,true);assert.equal(cursor.disabled,false);cursor.checked=false;cursor.dispatch('change');await tick();await tick();
 assert.deepEqual(writes[0].providers,['opencode']);assert.equal(writes[0].openCode2,true);
 nodes.get('synara-opencode-version').value='1';nodes.get('synara-opencode-version').dispatch('change');await tick();await tick();
 assert.equal(writes[1].openCode2,false);assert.equal(nodes.get('synara-opencode-version-field').hidden,true);
 nodes.get('synara-opencode-version').value='2';nodes.get('synara-opencode-version').dispatch('change');await tick();assert.equal(writes.length,2);
 assert.deepEqual(nodes.get('synara-provider-options').children.map(label=>label.children[1].textContent),['OpenCode']);
 helper.render({state:{connectionReady:true},language:'es'});assert.doesNotMatch(nodes.get('synara-agents').children.map(node=>node.textContent).join(' '),/Cursor|Unknown/);
});


test('OpenCode connection is optional in old responses and only enabled Kilo OpenCode requires the gateway',()=>{
 const own={...preferences(),kiloCodex:false,kiloClaude:false,providers:['opencode']};
 delete own.openCodeConnection;
 assert.equal(synaraPreferencesValid(own),true);
 assert.equal(synaraNativeOnly(own),true);
 assert.equal(synaraNativeOnly({...own,openCodeConnection:'kilo'}),false);
 assert.equal(synaraNativeOnly({...own,openCodeConnection:'kilo',providers:[]}),true);
 assert.equal(synaraPreferencesValid({...own,openCodeConnection:''}),true);
 for(const connection of ['other',null,false,12]) assert.equal(synaraPreferencesValid({...own,openCodeConnection:connection}),false);
 assert.equal(synaraPrepared(null,{prepared:true,preferences:own},{...own,openCodeConnection:'native'}),true);
});

test('OpenCode connection saves an explicit immutable choice and displays only its matching Kilo summary',async t=>{
 let current={...preferences(),providers:['opencode'],openCode2:true}, release;
 const posts=[];
 const summary=[{id:'vendor/first',name:'First shared'},{id:'vendor/second',name:'Second shared'}];
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{preferences:structuredClone(current),prepared:false,library:saved().library,effectiveOpenCodeModels:summary},{preferencesAPI:async body=>{
  if(body){posts.push(structuredClone(body));await new Promise(resolve=>{release=resolve;});current=structuredClone(body);}
  return {preferences:structuredClone(current),supportedProviders:[{id:'opencode',label:'OpenCode'}],openCode2Supported:true,openCodeKiloSupported:true};
 }});
 await tick();
 assert.equal(nodes.get('synara-opencode-connection').value,'native');
 assert.match(nodes.get('synara-opencode-connection-help').textContent,/login and catalog/);
 assert.match(nodes.get('synara-opencode-help').textContent,/separate profile.*own login/);
 const before=helper.launchState().fingerprint;
 nodes.get('synara-opencode-connection').value='kilo';nodes.get('synara-opencode-connection').dispatch('change');
 assert.equal(posts.length,1);assert.equal(posts[0].openCodeConnection,'kilo');assert.equal(posts[0].openCode2,true);
 assert.equal(helper.preferences().openCodeConnection,'native');assert.equal(nodes.get('synara-opencode-connection').disabled,true);
 assert.equal(helper.launchState().valid,false);
 release();await tick();await tick();
 assert.equal(helper.preferences().openCodeConnection,'kilo');assert.notEqual(helper.launchState().fingerprint,before);
 assert.deepEqual(nodes.get('synara-models').children.map(row=>row.textContent),['Kilo Proxy · OpenCode · First shared · vendor/first','Kilo Proxy · OpenCode · Second shared · vendor/second']);
 assert.match(nodes.get('synara-agents').children.at(-1).textContent,/Kilo Proxy · OpenCode 2 · Shared models through the proxy/);
 assert.match(nodes.get('synara-summary').textContent,/OpenCode: 2/);
 assert.equal(helper.launchState().count,2);
 helper.render({state:{connectionReady:true},language:'es'});
 assert.equal(nodes.get('synara-opencode-connection-label').textContent,'Conexión de OpenCode');
 assert.match(nodes.get('synara-opencode-connection-help').textContent,/modelos guardados en Modelos/);
 assert.doesNotMatch(nodes.get('synara-opencode-help').textContent,/inicio de sesión/);
 nodes.get('synara-opencode-connection').value='native';nodes.get('synara-opencode-connection').dispatch('change');release();await tick();await tick();
 assert.equal(helper.preferences().openCodeConnection,'native');assert.deepEqual(nodes.get('synara-models').children,[]);
 assert.match(nodes.get('synara-opencode-help').textContent,/perfil separado.*inicio de sesión/);
 assert.doesNotMatch(nodes.get('synara-agents').children.at(-1).textContent,/Kilo Proxy/);
});

test('OpenCode Kilo fails closed without the multiple-instance capability and permits explicit recovery to an own account',async t=>{
 let current={...preferences(),kiloCodex:false,kiloClaude:false,providers:['opencode'],openCodeConnection:'kilo'};const writes=[];
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{preferences:current,prepared:false,library:saved().library},{preferencesAPI:async body=>{if(body){writes.push(body);current=structuredClone(body);}return {preferences:current,supportedProviders:[{id:'opencode',label:'OpenCode'}]};}});
 await tick();assert.equal(helper.launchState().valid,false);assert.match(helper.launchState().reason,/cannot use the Kilo OpenCode connection/);
 assert.match(helper.launchState().reason,/compatible macOS build/);
 helper.render({state:{connectionReady:true},language:'es'});assert.match(nodes.get('synara-opencode-connection-help').textContent,/compilación compatible de macOS/);
 helper.render({state:{connectionReady:true},language:'en'});
 assert.equal(nodes.get('synara-opencode-connection-kilo').disabled,true);assert.equal(nodes.get('synara-opencode-connection').value,'kilo');
 nodes.get('synara-opencode-connection').value='native';nodes.get('synara-opencode-connection').dispatch('change');await tick();await tick();
 assert.equal(writes.length,1);assert.equal(helper.launchState().nativeOnly,true);
 nodes.get('synara-opencode-connection').value='kilo';nodes.get('synara-opencode-connection').dispatch('change');await tick();assert.equal(writes.length,1);
 assert.equal(nodes.get('synara-opencode-connection').value,'native');
});

test('OpenCode Kilo alone needs a saved library and connection without imposing the custom Codex or Claude model limit',async t=>{
 const source=saved();source.library.models=Array.from({length:40},(_,i)=>({id:'vendor/model-'+i}));source.library.defaultModel='vendor/model-0';
 const current={...preferences(),kiloCodex:false,kiloClaude:false,providers:['opencode'],openCodeConnection:'kilo'};
 const {helper}=fixture(t,async path=>path==='model-library'?source:{preferences:current,prepared:false,library:source.library},{preferencesAPI:async()=>({preferences:current,supportedProviders:[{id:'opencode',label:'OpenCode'}],openCodeKiloSupported:true})});
 await tick();assert.equal(helper.launchState().nativeOnly,false);assert.equal(helper.launchState().valid,true);
 helper.render({state:{connectionReady:false},language:'en'});assert.equal(helper.launchState().valid,false);await assert.rejects(helper.launchState().prepare(),/Connect your selected provider/);
});

test('OpenCode Kilo rejects an old response that does not confirm the requested connection, then retries the exact choice',async t=>{
 let current={...preferences(),providers:['opencode']},omit=true;const writes=[];
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{preferences:current,prepared:false,library:saved().library},{preferencesAPI:async body=>{
  if(body){writes.push(structuredClone(body));current=structuredClone(body);}
  const returned=structuredClone(current);if(omit)delete returned.openCodeConnection;
  return {preferences:returned,supportedProviders:[{id:'opencode',label:'OpenCode'}],openCodeKiloSupported:true};
 }});
 await tick();assert.equal(helper.preferences().openCodeConnection,'native');
 await helper.savePreferences({...current,openCodeConnection:'kilo'});
 assert.equal(helper.preferences().openCodeConnection,'native');assert.equal(helper.launchState().valid,false);
 assert.match(nodes.get('synara-settings-status').textContent,/did not confirm/);
 omit=false;nodes.get('synara-settings-retry').dispatch('click');await tick();await tick();
 assert.deepEqual(writes[1],writes[0]);assert.equal(helper.preferences().openCodeConnection,'kilo');
});

test('an externally changed OpenCode connection invalidates the profile snapshot before preparation',async t=>{
 let current={...preferences(),providers:['opencode']};const writes=[];
 const {helper}=fixture(t,async(path,body)=>{if(body)writes.push(body);return path==='model-library'?saved():{preferences:current,prepared:false,library:saved().library};},{preferencesAPI:async()=>({preferences:structuredClone(current),supportedProviders:[{id:'opencode',label:'OpenCode'}],openCodeKiloSupported:true})});
 await tick();current={...current,openCodeConnection:'kilo'};
 await assert.rejects(helper.launchState().prepare(),/settings changed/);assert.deepEqual(writes,[]);
 assert.equal(helper.preferences().openCodeConnection,'kilo');
});



test('Linux AppImage guidance separates discovery from readiness and preserves sandbox and runtime limits',async t=>{
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{});await tick();
 assert.match(nodes.get('synara-linux').textContent,/package candidate, not a prepared installation/);
 assert.match(nodes.get('synara-linux').textContent,/verified Kilo copy/);assert.match(nodes.get('synara-linux').textContent,/never starts an AppImage during detection/);
 assert.match(nodes.get('synara-linux').textContent,/keeps the sandbox enabled/);assert.match(nodes.get('synara-linux').textContent,/has not been validated on Linux/);
 helper.render({state:{connectionReady:true},language:'es'});
 assert.match(nodes.get('synara-linux').textContent,/paquete candidato, no una instalación preparada/);assert.match(nodes.get('synara-linux').textContent,/mantiene el sandbox activado/);assert.match(nodes.get('synara-linux').textContent,/no se ha validado en Linux/);
});

test('Synara rejects absent upstream Muse, duplicate and unknown provider policies',()=>{
 for(const providers of [['muse'],['cursor','cursor'],['unknown']]) assert.equal(synaraPreferencesValid({...preferences(),providers}),false);
});
test('Synara guidance keeps CLI authentication, Claude reasoning and voice requirements explicit in both languages',async t=>{
 const {helper,nodes}=fixture(t,async path=>path==='model-library'?saved():{prepared:false,version:'9.4.2-beta.8'});await tick();
 for(const language of ['en','es']) {
  helper.render({state:{connectionReady:true},language});
  assert.match(nodes.get('synara-requirements').textContent,/9\.4\.2-beta\.8/);assert.match(nodes.get('synara-requirements').textContent,/keyring\/auto/);
  assert.match(nodes.get('synara-reasoning-help').textContent,language==='en'?/native Claude models use the reasoning levels selected in Kilo/:/modelos Claude nativos compatibles usan sus niveles de razonamiento seleccionados en Kilo/);
  assert.match(nodes.get('synara-voice').textContent,/codex login/);assert.match(nodes.get('synara-voice').textContent,/ChatGPT/);
  assert.match(nodes.get('synara-privacy').textContent,language==='en'?/does not copy login credentials/:/no copia credenciales/);
  assert.doesNotMatch([...nodes.values()].map(node=>node.textContent).join(' '),/private-local-secret/);
 }
});


test('Kilo Claude picker capability fails closed for official and shared modes without blocking native Claude or other Kilo accounts',async t=>{
 let current=preferences();const writes=[];
 const {helper,nodes}=fixture(t,async(path,body)=>{if(body)writes.push(body);return path==='model-library'?saved():{preferences:current,prepared:false,library:saved().library};},{preferencesAPI:async body=>{if(body)current=structuredClone(body);return {preferences:current,claudePickerSupported:false,supportedProviders:[]};}});
 await tick();assert.equal(helper.launchState().valid,false);assert.match(helper.launchState().reason,/cannot restrict the Kilo Claude picker/);await assert.rejects(helper.launchState().prepare(),/cannot restrict/);assert.equal(writes.length,0);
 current={...current,claudeModels:'official'};await helper.reload();assert.equal(helper.launchState().valid,false);
 await helper.savePreferences({...current,kiloClaude:false});assert.equal(helper.launchState().valid,true);assert.equal(helper.preferences().claude,true);assert.equal(helper.preferences().kiloCodex,true);
 helper.render({state:{connectionReady:true},language:'es'});await helper.savePreferences({...current,kiloClaude:true});assert.equal(helper.launchState().valid,false);assert.match(nodes.get('synara-reasoning-help').textContent,/compilación compatible de macOS/);
});


test('only a confirmed Synara preference save refreshes installation availability',async t=>{
 let current=preferences(),fail=true,refreshes=0;
 const {helper}=fixture(t,async path=>path==='model-library'?saved():{preferences:current,prepared:false,library:saved().library},{onPreferencesConfirmed:()=>refreshes++,preferencesAPI:async body=>{if(body){if(fail)throw new Error('Synthetic save unavailable');current=structuredClone(body);}return {preferences:current};}});
 await tick();await helper.savePreferences({...current,kiloClaude:false});assert.equal(refreshes,0);fail=false;await helper.savePreferences({...current,kiloClaude:false});assert.equal(refreshes,1);
});
