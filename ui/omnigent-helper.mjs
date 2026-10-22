import {connectionReady} from './chatgpt-helper.mjs';
import {translate} from './i18n.mjs';
import {openMausBotLibraryReady} from './openmausbot-helper.mjs';

const librarySignature = library => JSON.stringify(library);
const preferenceSignature = p => JSON.stringify(p && [p.version,p.codex,p.claude,p.codexConnection,p.claudeConnection,p.codexModels,p.claudeModels]);
export function omnigentPreferencesValid(p) {
 return p?.version === 1 && typeof p.codex === 'boolean' && typeof p.claude === 'boolean' && ['native','kilo'].includes(p.codexConnection) && ['native','kilo'].includes(p.claudeConnection) && ['official','shared'].includes(p.codexModels) && ['official','shared'].includes(p.claudeModels);
}
export const omnigentUsesKilo = p => omnigentPreferencesValid(p) && (p.codex && p.codexConnection === 'kilo' || p.claude && p.claudeConnection === 'kilo');
export const omnigentNativeOnly = p => omnigentPreferencesValid(p) && !omnigentUsesKilo(p);
export function omnigentPrepared(saved,profile,p) {
 return omnigentPreferencesValid(p) && profile?.prepared === true && typeof profile.fingerprint === 'string' && profile.fingerprint.length > 0 && preferenceSignature(profile.preferences) === preferenceSignature(p) && (omnigentNativeOnly(p) || openMausBotLibraryReady(saved) && librarySignature(saved.library) === librarySignature(profile.library));
}

export function createOmnigentHelper({api,onChange=()=>{},onPreferencesConfirmed=()=>{},clock={now:()=>Date.now(),setTimeout:(fn,ms)=>setTimeout(fn,ms),clearTimeout:id=>clearTimeout(id)}}) {
 const $=id=>document.getElementById(id);
 let ctx={state:null,language:'en'},saved=null,profile=null,preferences=null,pending=null,requiresPreparation=false;
 let loading=false,loaded=false,saving=false,working=false,error='',preferencesError='',generation=0,profileConnection='';
 let launchWatching=false,statusBusy=false,statusTimer=null,statusUntil=0,statusGeneration=0;
 const L=(en,es)=>ctx.language==='en'?en:es;
 const connectionSignature=()=>JSON.stringify([ctx.state?.baseURL,ctx.state?.localKey,ctx.state?.orgId,ctx.state?.connectionReady,ctx.state?.chatgpt?.connected]);
 const nativeOnly=()=>omnigentNativeOnly(preferences);
 const signature=()=>JSON.stringify([preferenceSignature(preferences),preferenceSignature(pending),...(nativeOnly()?[]:[saved?.library,connectionSignature()])]);
 const prepared=()=>!loading&&!saving&&!pending&&(nativeOnly()||profileConnection===connectionSignature())&&omnigentPrepared(saved,profile,preferences);
 const ready=()=>loaded&&!loading&&!saving&&!working&&!pending&&omnigentPreferencesValid(preferences)&&(preferences.codex||preferences.claude)&&(nativeOnly()||openMausBotLibraryReady(saved));
 function reason() {
  if(preferencesError||error)return preferencesError||error;
  if(loading||!loaded)return L('Load Omnigent settings before opening.','Carga los ajustes de Omnigent antes de abrir.');
  if(saving||pending)return L('Finish saving Omnigent settings before opening.','Termina de guardar los ajustes de Omnigent antes de abrir.');
  if(!omnigentPreferencesValid(preferences))return L('Reload valid Omnigent preferences before opening.','Carga unas preferencias Omnigent válidas antes de abrir.');
  if(!preferences.codex&&!preferences.claude)return L('Enable Codex or Claude before preparing Omnigent.','Activa Codex o Claude antes de preparar Omnigent.');
  if(!nativeOnly()&&!openMausBotLibraryReady(saved))return L('Save at least one shared model in Models.','Guarda al menos un modelo compartido en Modelos.');
  return '';
 }
 function acceptPreferences(response) {
  if(!omnigentPreferencesValid(response?.preferences))throw new Error(L('Could not read Omnigent preferences. Reload settings before preparing.','No se pudieron leer las preferencias de Omnigent. Carga los ajustes antes de preparar.'));
  const changed=!!preferences&&preferenceSignature(preferences)!==preferenceSignature(response.preferences);
  preferences=structuredClone(response.preferences);requiresPreparation=!!response.requiresPreparation;
  if(changed)onPreferencesConfirmed();
 }
 function render(context=ctx) {
  ctx=context;
  $('omnigent-intro').textContent=L('A separate Omnigent workspace with Codex and Claude. Choose one connection for each enabled harness.','Un espacio Omnigent separado con Codex y Claude. Elige una conexión para cada harness activado.');
  const sameProfile=preferenceSignature(preferences)===preferenceSignature(profile?.preferences);
  const groups=[['Codex',sameProfile?profile?.effectiveCodexModels??[]:[]],['Claude',sameProfile?profile?.effectiveClaudeModels??[]:[]]];
  $('omnigent-summary').textContent=['codex','claude'].filter(kind=>preferences?.[kind]).map(kind=>{const name=kind==='codex'?'Codex':'Claude';return preferences[kind+'Connection']==='native'?name+L(' · own catalog',' · catálogo propio'):name+': '+(groups.find(([label])=>label===name)?.[1].length??0);}).join(' · ')||L('No harness enabled','Ningún harness activado');
  $('omnigent-models').replaceChildren();
  for(const [name,models] of groups)for(const model of models){const item=document.createElement('li');item.textContent=name+' · '+(model.name?model.name+' · ':'')+model.id+(model.isDefault?L(' · default',' · predeterminado'):'')+(model.reasoningEffort?' · '+model.reasoningEffort:'');$('omnigent-models').append(item);}
  $('omnigent-library-help').textContent=L('Kilo connections use your saved Models selection, its default and compatible reasoning. Own accounts keep their CLI login and native catalog.','Las conexiones Kilo usan tu selección guardada en Modelos, su predeterminado y el razonamiento compatible. Las cuentas propias conservan su sesión CLI y su catálogo nativo.');
  $('omnigent-settings-title').textContent=L('Accounts and models','Cuentas y modelos');
  $('omnigent-settings-help').textContent=L('Changes save automatically. Stop Omnigent · Kilo before applying changes and start a new chat after changing a connection.','Los cambios se guardan solos. Detén Omnigent · Kilo antes de aplicar cambios e inicia un chat nuevo tras cambiar de conexión.');
  const displayed=pending??preferences,editable=!!preferences&&!loading&&!saving&&!working&&!profile?.running&&!launchWatching;
  for(const kind of ['codex','claude']){
   const label=kind==='codex'?'Codex':'Claude';
   $('omnigent-account-'+kind).checked=displayed?.[kind]===true;$('omnigent-account-'+kind).disabled=!editable;
   $('omnigent-'+kind+'-connection-label').textContent=label+L(' connection',' · conexión');
   $('omnigent-'+kind+'-connection-native').textContent=L('Own account','Cuenta propia');$('omnigent-'+kind+'-connection-kilo').textContent='Kilo Proxy';
   $('omnigent-'+kind+'-connection').value=displayed?.[kind+'Connection']??'kilo';$('omnigent-'+kind+'-connection').disabled=!editable||!displayed?.[kind];
   $('omnigent-'+kind+'-models-label').textContent=label+L(' Kilo models',' · modelos Kilo');
   $('omnigent-'+kind+'-official').textContent=kind==='codex'?L('OpenAI and ChatGPT','OpenAI y ChatGPT'):L('Opus 5.5, Sonnet 5.5 and Fable 5.1','Opus 5.5, Sonnet 5.5 y Fable 5.1');
   $('omnigent-'+kind+'-shared').textContent=L('All shared models','Todos los modelos compartidos');
   $('omnigent-'+kind+'-models').value=displayed?.[kind+'Models']??'shared';$('omnigent-'+kind+'-models').disabled=!editable||!displayed?.[kind]||displayed?.[kind+'Connection']!=='kilo';
  }
  $('omnigent-settings-status').textContent=translate(preferencesError||(saving?L('Saving Omnigent settings…','Guardando ajustes de Omnigent…'):requiresPreparation&&!prepared()?L('Settings saved. Stop Omnigent · Kilo and prepare again.','Ajustes guardados. Detén Omnigent · Kilo y prepara de nuevo.'):''),ctx.language);
  $('omnigent-settings-status').classList.toggle('error',!!preferencesError);
  $('omnigent-settings-retry').hidden=!pending||!preferencesError;$('omnigent-settings-retry').disabled=saving||loading||working;$('omnigent-settings-retry').textContent=L('Retry saving','Reintentar guardado');
  $('omnigent-settings-reload').disabled=saving||loading||working;$('omnigent-settings-reload').textContent=L('Reload settings','Cargar ajustes');
  $('omnigent-prepare').textContent=L('Prepare without opening','Preparar sin abrir');$('omnigent-prepare').disabled=!ready()||launchWatching||!!profile?.running||!nativeOnly()&&!connectionReady(ctx.state);
  $('omnigent-reload').textContent=L('Refresh','Actualizar');$('omnigent-reload').disabled=loading||working||saving;
  $('omnigent-stop').textContent=L('Stop Omnigent · Kilo','Detener Omnigent · Kilo');$('omnigent-stop').hidden=!profile?.running;$('omnigent-stop').disabled=working||loading||saving;
  $('omnigent-prepared').textContent=prepared()?L('Your saved Omnigent accounts and models are prepared.','Las cuentas y modelos guardados de Omnigent están preparados.'):L('Prepare or open Omnigent · Kilo to apply your saved accounts and models.','Prepara o abre Omnigent · Kilo para aplicar las cuentas y modelos guardados.');
  $('omnigent-privacy').textContent=L('Kilo manages a separate local Omnigent server and runner, opened in your browser. Stop closes only these Kilo processes; the browser tab may remain open. Codex and Claude use the custom or verified managed CLI paths selected in Settings. OpenCode and Muse are not included.','Kilo gestiona un servidor y runner Omnigent locales separados, abiertos en tu navegador. Detener cierra sólo estos procesos Kilo; la pestaña puede quedar abierta. Codex y Claude usan las rutas CLI personalizadas o las copias verificadas elegidas en Ajustes. No se incluyen OpenCode ni Muse.');
  $('omnigent-status').textContent=translate(error||profile?.message||'',ctx.language);$('omnigent-status').classList.toggle('error',!!error);
  onChange();if(!loaded&&!loading)void reload();
 }
 async function reload(){
  if(loading||working||saving)return;
  loading=true;error='';preferencesError='';const current=++generation,connection=connectionSignature();render();
  try{const [library,info,prefs]=await Promise.allSettled([api('model-library'),api('clients/omnigent'),api('clients/omnigent/preferences')]);if(current!==generation)return;if(prefs.status!=='fulfilled')throw prefs.reason;if(info.status!=='fulfilled')throw info.reason;acceptPreferences(prefs.value);if(library.status!=='fulfilled'&&!nativeOnly())throw library.reason;saved=library.status==='fulfilled'?library.value:null;profile=info.value;profileConnection=connection;pending=null;}
  catch(failure){if(current===generation){error=failure.message;preferencesError=failure.message;profile=null;}}
  finally{if(current===generation){loading=false;loaded=true;render();}}
 }
 async function savePreferences(next){
  if(saving||working||loading||launchWatching||profile?.running||!omnigentPreferencesValid(next))return;
  const snapshot=structuredClone(next),current=++generation;saving=true;pending=snapshot;preferencesError='';if(profile)profile={...profile,prepared:false};render();
  try{const response=await api('clients/omnigent/preferences',snapshot);if(current!==generation)return;if(preferenceSignature(response?.preferences)!==preferenceSignature(snapshot))throw new Error(L('Omnigent did not confirm the saved preferences. Reload settings before preparing.','Omnigent no confirmó las preferencias guardadas. Carga los ajustes antes de preparar.'));acceptPreferences(response);requiresPreparation=true;pending=null;profile=await api('clients/omnigent');profileConnection=connectionSignature();}
  catch(failure){if(current===generation)preferencesError=failure.message;}
  finally{if(current===generation){saving=false;render();}}
 }
 async function prepare(){
  if(!ready()||!nativeOnly()&&!connectionReady(ctx.state))throw new Error(reason()||L('Connect Kilo Proxy before preparing.','Conecta Kilo Proxy antes de preparar.'));
  const native=nativeOnly(),snapshot=native?null:structuredClone(saved.library),expected=structuredClone(preferences),connection=connectionSignature();working=true;error='';render();
  try{
   const [latest,settings]=await Promise.all([native?null:api('model-library'),api('clients/omnigent/preferences')]);
   if(!native&&(!openMausBotLibraryReady(latest)||librarySignature(latest.library)!==librarySignature(snapshot))){saved=latest;throw new Error(L('The shared models changed. Review the refreshed list and open again.','Los modelos compartidos han cambiado. Revisa la lista actualizada y vuelve a abrir.'));}
   if(preferenceSignature(settings.preferences)!==preferenceSignature(expected)){acceptPreferences(settings);throw new Error(L('Omnigent settings changed. Review them and open again.','Los ajustes de Omnigent han cambiado. Revísalos y vuelve a abrir.'));}
   const current=await api('clients/omnigent');
   profile=current.running&&omnigentPrepared(saved,current,expected)?current:await api('clients/omnigent',native?{preferences:expected}:{library:snapshot,preferences:expected});profileConnection=connection;
   if(!omnigentPrepared(saved,profile,expected))throw new Error(L('Omnigent did not confirm the prepared accounts and models. Refresh before opening.','Omnigent no confirmó las cuentas y modelos preparados. Actualiza antes de abrir.'));
   const [after,afterPrefs]=await Promise.all([native?null:api('model-library'),api('clients/omnigent/preferences')]);
   if(preferenceSignature(afterPrefs.preferences)!==preferenceSignature(expected)||!native&&(connection!==connectionSignature()||librarySignature(after?.library)!==librarySignature(snapshot))){acceptPreferences(afterPrefs);throw new Error(L('Omnigent settings or models changed while preparing. Review them and open again.','Los ajustes o modelos de Omnigent cambiaron durante la preparación. Revísalos y vuelve a abrir.'));}
   requiresPreparation=false;return profile;
  }catch(failure){error=failure.message;throw failure;}finally{working=false;render();}
 }
 function cancelLaunchWatch(){statusGeneration++;launchWatching=false;if(statusTimer!==null)clock.clearTimeout(statusTimer);statusTimer=null;}
 async function refreshLaunchStatus(current){
  if(current!==statusGeneration)return;
  if(clock.now()>=statusUntil){cancelLaunchWatch();render();return;}
  if(!loading&&!working&&!saving&&!statusBusy){
   statusBusy=true;const snapshot=generation,connection=connectionSignature();
   try{const info=await api('clients/omnigent');if(current===statusGeneration&&snapshot===generation){profile=info;profileConnection=connection;error='';if(!info.running&&info.message==='The private Omnigent workspace could not start. Check the selected installation and open it again.')cancelLaunchWatch();}}
   catch(failure){if(current===statusGeneration&&snapshot===generation)error=failure.message;}
   finally{statusBusy=false;}
  }
  if(current!==statusGeneration){render();return;}
  render();statusTimer=clock.setTimeout(()=>{statusTimer=null;void refreshLaunchStatus(current);},1000);
 }
 async function launchConfirmed(){cancelLaunchWatch();launchWatching=true;statusUntil=clock.now()+50000;const current=statusGeneration;render();await refreshLaunchStatus(current);}
 async function stop(){if(working||loading||saving)return;working=true;error='';render();try{const result=await api('clients/omnigent/stop',{});if(result?.ok!==true)throw new Error(L('Omnigent did not confirm the stop. Refresh to check its state.','Omnigent no confirmó la detención. Actualiza para comprobar su estado.'));cancelLaunchWatch();}catch(failure){error=failure.message;return;}finally{working=false;render();}await reload();onPreferencesConfirmed();}
 for(const kind of ['codex','claude']){
  $('omnigent-account-'+kind).addEventListener('change',()=>{if(preferences)void savePreferences({...preferences,[kind]:$('omnigent-account-'+kind).checked});});
  for(const field of ['connection','models'])$('omnigent-'+kind+'-'+field).addEventListener('change',()=>{if(preferences)void savePreferences({...preferences,[kind+(field==='connection'?'Connection':'Models')]:$('omnigent-'+kind+'-'+field).value});});
 }
 $('omnigent-settings-retry').addEventListener('click',()=>{if(pending)void savePreferences(pending);});$('omnigent-settings-reload').addEventListener('click',()=>void reload());$('omnigent-reload').addEventListener('click',()=>void reload());$('omnigent-prepare').addEventListener('click',()=>void prepare().catch(()=>{}));$('omnigent-stop').addEventListener('click',()=>void stop());
 return {render,reload,savePreferences,stop,launchConfirmed,dispose:cancelLaunchWatch,preferences:()=>preferences&&structuredClone(preferences),launchState:()=>({id:'omnigent',nativeOnly:nativeOnly(),count:nativeOnly()?0:saved?.library?.models?.length??0,ready:false,valid:ready()&&(nativeOnly()||connectionReady(ctx.state)),working:working||loading||saving,reason:reason(),fingerprint:signature(),prepare})};
}
