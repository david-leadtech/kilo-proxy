import test from 'node:test';
import assert from 'node:assert/strict';
import {createCLIPreferencesHelper,cliPreferenceOperation} from '../ui/cli-preferences-helper.mjs';
const initial=()=>({version:1,codexExecutable:'',claudeExecutable:'',museExecutable:'',synaraExecutable:''});
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
 for(const [path,client]of [['codex-cli/catalog','codex-cli'],['claude/profile','claude'],['clients/muse','muse'],['clients/t3-code','t3-code'],['clients/synara','synara'],['open-design/profile','open-design'],['xcode/codex','xcode-codex'],['xcode/claude','xcode-claude']])assert.equal(cliPreferenceOperation(path,{},'POST'),client);
 assert.equal(cliPreferenceOperation('clients/launch',{client:'claude'},'POST'),'claude');
 assert.equal(cliPreferenceOperation('clients/launch',{client:'muse'},'POST'),'muse');
 for(const [path,body,method]of [['codex/catalog',{},'POST'],['clients/launch',{client:'codex'},'POST'],['clients/cli/preferences',{},'POST'],['clients/t3-code',undefined,'GET']])assert.equal(cliPreferenceOperation(path,body,method),'');
});
test('Muse path settings accept older version 1 responses and preserve existing selections',async t=>{
 let prefs={version:1,codexExecutable:'/custom/codex',claudeExecutable:'/custom/claude'},refreshes=0;const writes=[];
 const {helper,nodes,input}=fixture(t,async(path,body)=>{if(body){writes.push({...body});prefs={...body}}return {...prefs}},{onPreferencesSaved:()=>refreshes++});
 await tick();assert.equal(helper.blocked('muse'),false);assert.equal(nodes.get('cli-path-muse').value,'');
 input('muse','/custom/muse');assert.equal(helper.blocked('t3-code'),true);nodes.get('cli-path-save-muse').dispatch('click');await tick();
 assert.deepEqual(writes[0],{...initial(),codexExecutable:'/custom/codex',claudeExecutable:'/custom/claude',museExecutable:'/custom/muse'});assert.equal(helper.blocked('muse'),false);
 helper.render({language:'es',launchInfo:{clients:{muse:{path:'/custom/muse'}}}});assert.equal(nodes.get('cli-path-resolved-muse').textContent,'Ejecutable detectado: /custom/muse');assert.match(nodes.get('cli-preferences-muse-help').textContent,/modelos compartidos de Kilo o tu cuenta de Meta/);
 nodes.get('cli-path-auto-muse').dispatch('click');await tick();assert.equal(prefs.museExecutable,'');assert.equal(prefs.codexExecutable,'/custom/codex');assert.equal(prefs.claudeExecutable,'/custom/claude');assert.equal(refreshes,2);
});
test('Muse drafts and external path changes invalidate T3 preparation and unacknowledged Muse saves stay pending',async t=>{
 let prefs=initial(),omitConfirmation=false;const oc={version:1,major:1,executable1:'',executable2:''};
 const {helper,nodes,input}=fixture(t,async(path,body)=>{if(path==='clients/opencode/preferences')return {...oc};if(body){prefs={...body};if(omitConfirmation){const reply={...prefs};delete reply.museExecutable;return reply}}return {...prefs}});await tick();
 await helper.guardOperation('t3-code',async()=>({prepared:true}));assert.equal(helper.prepared('t3-code'),true);
 input('muse','/draft/muse');await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('Muse draft reached T3 preparation')),/settings changed/);
 omitConfirmation=true;nodes.get('cli-path-save-muse').dispatch('click');await tick();assert.equal(helper.blocked('muse'),true);assert.equal(nodes.get('cli-preferences-retry').hidden,false);assert.match(nodes.get('cli-preferences-status').textContent,/did not confirm/);
 omitConfirmation=false;nodes.get('cli-preferences-retry').dispatch('click');await tick();assert.equal(helper.prepared('t3-code'),false);
 await helper.guardOperation('t3-code',async()=>({prepared:true}));prefs.museExecutable='/external/muse';await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('stale Muse executable launched T3'),{preparation:false}),/settings changed/);assert.equal(helper.prepared('t3-code'),false);assert.equal(nodes.get('cli-path-muse').value,'/external/muse');
});
test('a malformed Muse path response cannot make CLI preferences ready',async t=>{
 const {helper,nodes}=fixture(t,async()=>({...initial(),museExecutable:42}));await tick();assert.equal(helper.blocked('muse'),true);assert.match(nodes.get('cli-preferences-status').textContent,/Could not read/);
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
test('Synara shares OpenCode executable paths while keeping its major independent',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},operations=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>({...prefs,major:2})});await tick();
 const result=await helper.guardOperation('synara',async()=>{operations++;prefs.major=2;return 'prepared'});assert.equal(result,'prepared');assert.equal(operations,1);
});
test('T3 rejects external OpenCode path changes before and during prepare and requests visible path reload',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},known={...prefs},reloads=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>known,onOpenCodePathsChanged:()=>reloads++});await tick();prefs.executable2='/changed/two';
 await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('stale preparation')),/settings changed/);known={...prefs};await assert.rejects(helper.guardOperation('t3-code',async()=>{prefs.executable1='/changed/one'}),/settings changed/);assert.equal(reloads,2);
});
test('Synara rejects external OpenCode path changes before and during prepare and requests visible path reload',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},known={...prefs},reloads=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>known,onOpenCodePathsChanged:()=>reloads++});await tick();prefs.executable2='/changed/two';
 await assert.rejects(helper.guardOperation('synara',async()=>assert.fail('stale preparation')),/settings changed/);known={...prefs};await assert.rejects(helper.guardOperation('synara',async()=>{prefs.executable1='/changed/one'}),/settings changed/);assert.equal(reloads,2);
});
test('confirmed preparations remain reusable across unchanged path reloads and each opening still verifies saved paths',async t=>{
 let reads=0,prepares=0,launches=0,refreshes=0;
 const {helper}=fixture(t,async()=>{reads++;return initial()},{onPreferencesSaved:()=>refreshes++});await tick();
 assert.equal(helper.prepared('codex-cli'),false);
 await assert.rejects(helper.guardOperation('codex-cli',async()=>assert.fail('unprepared launch'),{preparation:false}),/settings changed/);
 await helper.guardOperation('codex-cli',async()=>{prepares++;return {prepared:true}});
 assert.equal(helper.prepared('codex-cli'),true);
 await helper.reload();assert.equal(helper.prepared('codex-cli'),true);assert.equal(refreshes,0);
 const before=reads;
 for(let i=0;i<2;i++)await helper.guardOperation('codex-cli',async()=>{launches++},{preparation:false});
 assert.equal(prepares,1);assert.equal(launches,2);assert.equal(reads-before,4);assert.equal(helper.prepared('codex-cli'),true);
});
test('saving or discovering a changed CLI path invalidates preparation until the new path is confirmed',async t=>{
 let prefs=initial();
 const {helper,nodes,input}=fixture(t,async(path,body)=>{if(body)prefs={...body};return {...prefs}});await tick();
 await helper.guardOperation('claude',async()=>({prepared:true}));assert.equal(helper.prepared('claude'),true);
 input('claude','/custom/claude');assert.equal(helper.prepared('claude'),false);nodes.get('cli-path-save-claude').dispatch('click');await tick();
 assert.equal(helper.prepared('claude'),false);
 await assert.rejects(helper.guardOperation('claude',async()=>assert.fail('new route was not prepared'),{preparation:false}),/settings changed/);
 await helper.guardOperation('claude',async()=>({prepared:true}));assert.equal(helper.prepared('claude'),true);
 prefs.codexExecutable='/external/codex';
 await assert.rejects(helper.guardOperation('claude',async()=>assert.fail('external route change launched'),{preparation:false}),/settings changed/);
 assert.equal(helper.prepared('claude'),false);
});
test('failed preparation and path changes during confirmation cannot restore reusable readiness',async t=>{
 let prefs=initial();const {helper}=fixture(t,async()=>({...prefs}));await tick();
 await helper.guardOperation('synara',async()=>({prepared:true}));assert.equal(helper.prepared('synara'),true);
 await assert.rejects(helper.guardOperation('synara',async()=>{throw new Error('Synthetic preparation failure')}),/preparation failure/);assert.equal(helper.prepared('synara'),false);
 await assert.rejects(helper.guardOperation('synara',async()=>{prefs.claudeExecutable='/changed/claude';return {prepared:true}}),/settings changed/);assert.equal(helper.prepared('synara'),false);
});
test('T3 reuses confirmed executable paths independently of OpenCode major and blocks an external path before launching',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},known=null,reloads=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>known,onOpenCodePathsChanged:()=>reloads++});await tick();
 await helper.guardOperation('t3-code',async()=>({prepared:true}));assert.equal(helper.prepared('t3-code'),true);
 prefs.major=2;known={...prefs};await helper.guardOperation('t3-code',async()=>({opened:true}),{preparation:false});assert.equal(helper.prepared('t3-code'),true);
 prefs.executable2='/external/two';known=null;
 await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('external OpenCode route launched'),{preparation:false}),/settings changed/);assert.equal(helper.prepared('t3-code'),false);assert.equal(reloads,1);
});
test('Synara reuses confirmed executable paths independently of OpenCode major and blocks an external path before launching',async t=>{
 let prefs={version:1,major:1,executable1:'/oc/one',executable2:'/oc/two'},known=null,reloads=0;
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...prefs}:initial(),{openCodeSnapshot:()=>known,onOpenCodePathsChanged:()=>reloads++});await tick();
 await helper.guardOperation('synara',async()=>({prepared:true}));assert.equal(helper.prepared('synara'),true);
 prefs.major=2;known={...prefs};await helper.guardOperation('synara',async()=>({opened:true}),{preparation:false});assert.equal(helper.prepared('synara'),true);
 prefs.executable2='/external/two';known=null;
 await assert.rejects(helper.guardOperation('synara',async()=>assert.fail('external OpenCode route launched'),{preparation:false}),/settings changed/);assert.equal(helper.prepared('synara'),false);assert.equal(reloads,1);
});
test('an OpenCode path draft or pending save blocks T3 without affecting unrelated CLI clients',async t=>{
 let pathBlocked=false;const oc={version:1,major:1,executable1:'',executable2:''};
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...oc}:initial(),{openCodePathsBlocked:()=>pathBlocked});await tick();
 await helper.guardOperation('t3-code',async()=>({prepared:true}));await helper.guardOperation('claude',async()=>({prepared:true}));
 pathBlocked=true;assert.equal(helper.blocked('t3-code'),true);assert.equal(helper.prepared('t3-code'),false);assert.equal(helper.blocked('claude'),false);assert.equal(helper.prepared('claude'),true);
 await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('OpenCode draft reached T3 preparation')),/settings changed/);
 await assert.rejects(helper.guardOperation('t3-code',async()=>assert.fail('OpenCode draft reached T3 opening'),{preparation:false}),/settings changed/);
 await helper.guardOperation('claude',async()=>({opened:true}),{preparation:false});pathBlocked=false;assert.equal(helper.prepared('t3-code'),true);
});
test('an OpenCode path draft or pending save blocks Synara without affecting unrelated CLI clients',async t=>{
 let pathBlocked=false;const oc={version:1,major:1,executable1:'',executable2:''};
 const {helper}=fixture(t,async path=>path==='clients/opencode/preferences'?{...oc}:initial(),{openCodePathsBlocked:()=>pathBlocked});await tick();
 await helper.guardOperation('synara',async()=>({prepared:true}));await helper.guardOperation('claude',async()=>({prepared:true}));
 pathBlocked=true;assert.equal(helper.blocked('synara'),true);assert.equal(helper.prepared('synara'),false);assert.equal(helper.blocked('claude'),false);assert.equal(helper.prepared('claude'),true);
 await assert.rejects(helper.guardOperation('synara',async()=>assert.fail('OpenCode draft reached T3 preparation')),/settings changed/);
 await assert.rejects(helper.guardOperation('synara',async()=>assert.fail('OpenCode draft reached T3 opening'),{preparation:false}),/settings changed/);
 await helper.guardOperation('claude',async()=>({opened:true}),{preparation:false});pathBlocked=false;assert.equal(helper.prepared('synara'),true);
});
test('an OpenCode draft during preflight or preparation prevents stale T3 execution or prepared success',async t=>{
 let pathBlocked=false,hold=false,release;const gate=new Promise(resolve=>release=resolve),oc={version:1,major:1,executable1:'',executable2:''};
 const {helper}=fixture(t,async path=>{if(path==='clients/opencode/preferences'){if(hold)await gate;return {...oc}}return initial()},{openCodePathsBlocked:()=>pathBlocked});await tick();
 hold=true;const preflight=helper.guardOperation('t3-code',async()=>assert.fail('draft during OpenCode preflight reached operation'));await tick();pathBlocked=true;release();await assert.rejects(preflight,/settings changed/);assert.equal(helper.prepared('t3-code'),false);
 hold=false;pathBlocked=false;await assert.rejects(helper.guardOperation('t3-code',async()=>{pathBlocked=true;return {prepared:true}}),/settings changed/);assert.equal(helper.prepared('t3-code'),false);
});
test('an OpenCode draft during preflight or preparation prevents stale Synara execution or prepared success',async t=>{
 let pathBlocked=false,hold=false,release;const gate=new Promise(resolve=>release=resolve),oc={version:1,major:1,executable1:'',executable2:''};
 const {helper}=fixture(t,async path=>{if(path==='clients/opencode/preferences'){if(hold)await gate;return {...oc}}return initial()},{openCodePathsBlocked:()=>pathBlocked});await tick();
 hold=true;const preflight=helper.guardOperation('synara',async()=>assert.fail('draft during OpenCode preflight reached operation'));await tick();pathBlocked=true;release();await assert.rejects(preflight,/settings changed/);assert.equal(helper.prepared('synara'),false);
 hold=false;pathBlocked=false;await assert.rejects(helper.guardOperation('synara',async()=>{pathBlocked=true;return {prepared:true}}),/settings changed/);assert.equal(helper.prepared('synara'),false);
});
test('an OpenCode draft during the final path verification prevents a prepared confirmation',async t=>{
 let pathBlocked=false,reads=0,release;const gate=new Promise(resolve=>release=resolve),oc={version:1,major:1,executable1:'',executable2:''};
 const {helper}=fixture(t,async path=>{if(path==='clients/opencode/preferences'){if(++reads===2)await gate;return {...oc}}return initial()},{openCodePathsBlocked:()=>pathBlocked});await tick();
 const pending=helper.guardOperation('t3-code',async()=>({prepared:true}));await tick();assert.equal(reads,2);pathBlocked=true;release();await assert.rejects(pending,/settings changed/);assert.equal(helper.prepared('t3-code'),false);
});
test('an OpenCode draft during the final path verification prevents a prepared Synara confirmation',async t=>{
 let pathBlocked=false,reads=0,release;const gate=new Promise(resolve=>release=resolve),oc={version:1,major:1,executable1:'',executable2:''};
 const {helper}=fixture(t,async path=>{if(path==='clients/opencode/preferences'){if(++reads===2)await gate;return {...oc}}return initial()},{openCodePathsBlocked:()=>pathBlocked});await tick();
 const pending=helper.guardOperation('synara',async()=>({prepared:true}));await tick();assert.equal(reads,2);pathBlocked=true;release();await assert.rejects(pending,/settings changed/);assert.equal(helper.prepared('synara'),false);
});

test('Synara optional v1 path preserves CLI choices and confirms both save and automatic reset',async t=>{
 let prefs={version:1,codexExecutable:'/cli/codex',claudeExecutable:'/cli/claude',museExecutable:'/cli/muse'};const writes=[];
 const {helper,nodes,input}=fixture(t,async(path,body)=>{if(body){writes.push({...body});prefs={...body}}return {...prefs}});await tick();
 assert.equal(nodes.get('cli-path-synara').value,'');assert.equal(helper.blocked('synara'),false);
 input('synara','/home/test/Applications/Synara-1.0.3-x86_64.AppImage');assert.equal(helper.blocked('synara'),true);nodes.get('cli-path-save-synara').dispatch('click');await tick();
 assert.deepEqual(writes[0],{...initial(),...prefs});assert.equal(prefs.synaraExecutable,'/home/test/Applications/Synara-1.0.3-x86_64.AppImage');
 helper.render({language:'es',launchInfo:{clients:{synara:{path:prefs.synaraExecutable,installed:true,available:false}}}});
 assert.equal(nodes.get('cli-preferences-title').textContent,'Instalaciones de aplicaciones y CLI');assert.match(nodes.get('cli-path-resolved-synara').textContent,/Ejecutable detectado:/);assert.match(nodes.get('cli-preferences-synara-help').textContent,/no está listo para abrir/);assert.match(nodes.get('cli-preferences-synara-help').textContent,/no verifica su origen, versión ni canal/);
 nodes.get('cli-path-auto-synara').dispatch('click');await tick();assert.equal(prefs.synaraExecutable,'');assert.equal(prefs.codexExecutable,'/cli/codex');assert.equal(prefs.claudeExecutable,'/cli/claude');assert.equal(prefs.museExecutable,'/cli/muse');
});
test('Synara draft, unconfirmed save and external selection cannot prepare or open a stale binary',async t=>{
 let prefs=initial(),omit=false;const {helper,nodes,input}=fixture(t,async(path,body)=>{if(body){prefs={...body};if(omit){const reply={...prefs};delete reply.synaraExecutable;return reply}}return {...prefs}});await tick();
 await helper.guardOperation('synara',async()=>({prepared:true}));assert.equal(helper.prepared('synara'),true);
 input('synara','/persistent/synara');await assert.rejects(helper.guardOperation('synara',async()=>assert.fail('draft was prepared')),/settings changed/);
 omit=true;nodes.get('cli-path-save-synara').dispatch('click');await tick();assert.equal(helper.blocked('synara'),true);assert.equal(nodes.get('cli-preferences-retry').hidden,false);
 omit=false;nodes.get('cli-preferences-retry').dispatch('click');await tick();assert.equal(helper.prepared('synara'),false);
 await helper.guardOperation('synara',async()=>({prepared:true}));prefs.synaraExecutable='/different/synara';
 await assert.rejects(helper.guardOperation('synara',async()=>assert.fail('changed Synara opened'),{preparation:false}),/settings changed/);assert.equal(helper.prepared('synara'),false);assert.equal(nodes.get('cli-path-synara').value,'/different/synara');
});
test('a malformed Synara path response cannot make executable preferences ready',async t=>{
 const {helper,nodes}=fixture(t,async()=>({...initial(),synaraExecutable:42}));await tick();assert.equal(helper.blocked('synara'),true);assert.match(nodes.get('cli-preferences-status').textContent,/Could not read/);
});
