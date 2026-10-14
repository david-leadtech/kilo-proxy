import {connectionReady} from './chatgpt-helper.mjs';
import {translate} from './i18n.mjs';
import {openMausBotLibraryReady} from './openmausbot-helper.mjs';

export const musePreferencesValid=value=>value?.version===1&&['kilo','native'].includes(value.connection);
const same=(a,b)=>JSON.stringify(a)===JSON.stringify(b);
export function musePrepared(saved,profile,preferences,executable){
 return musePreferencesValid(preferences)&&profile?.prepared===true&&same(profile.preferences,preferences)&&profile.resolvedExecutable===executable&&(preferences.connection==='native'||openMausBotLibraryReady(saved)&&same(saved.library,profile.library));
}

// A connection choice never changes the selected executable or writes Models.
export function createMuseHelper({api,onChange=()=>{}}){
 const $=id=>document.getElementById(id);
 let ctx={state:null,language:'en',launchInfo:null},preferences=null,pending=null,profile=null,saved=null;
 let loading=false,loaded=false,saving=false,working=false,error='',revision=0;
 const L=(en,es)=>ctx.language==='en'?en:es;
 const native=()=>musePreferencesValid(preferences)&&preferences.connection==='native';
 const executable=()=>ctx.launchInfo?.clients?.muse?.path||profile?.resolvedExecutable||'';
 const connection=()=>JSON.stringify([ctx.state?.baseURL,ctx.state?.localKey,ctx.state?.orgId,ctx.state?.connectionReady,ctx.state?.chatgpt?.connected]);
 const signature=()=>JSON.stringify([preferences,pending,native()?null:saved?.library,native()?null:connection(),executable(),revision]);
 const reason=()=>error||(loading?L('Loading Muse settings…','Cargando ajustes de Muse…'):!loaded||!musePreferencesValid(preferences)?L('Load Muse settings before opening.','Carga los ajustes de Muse antes de abrir.'):saving||pending?L('Finish saving the Muse connection before opening.','Termina de guardar la conexión de Muse antes de abrir.'):!same(profile?.preferences,preferences)?L('Muse settings changed. Reload before opening.','Los ajustes de Muse han cambiado. Carga de nuevo antes de abrir.'):!native()&&profile?.kiloSupported!==true?(profile?.reason||L('This Muse installation cannot use Kilo models. Choose a compatible executable or your Meta account.','Esta instalación de Muse no admite modelos de Kilo. Elige un ejecutable compatible o tu cuenta de Meta.')):!native()&&!connectionReady(ctx.state)?L('Connect Kilo or ChatGPT before opening Muse with Kilo models.','Conecta Kilo o ChatGPT antes de abrir Muse con modelos de Kilo.'):!native()&&!openMausBotLibraryReady(saved)?L('Add and save shared models in Models before opening Muse Kilo.','Añade y guarda modelos compartidos en Modelos antes de abrir Muse Kilo.'):!executable()?L('Refresh Muse installation detection before opening.','Actualiza la detección de Muse antes de abrir.'):'');
 function render(next=ctx){
  ctx=next;
  const shown=pending||preferences;
  $('muse-title').textContent='Muse Code';
  $('muse-intro').textContent=L('Choose Kilo shared models or your own Meta account. Both use the Muse executable selected in Application and CLI installations.','Elige los modelos compartidos de Kilo o tu cuenta propia de Meta. Ambas opciones usan el ejecutable Muse elegido en Instalaciones de aplicaciones y CLI.');
  $('muse-connection-label').textContent=L('Connection','Conexión');
  $('muse-connection-kilo').textContent=L('Kilo Proxy · shared models','Kilo Proxy · modelos compartidos');
  $('muse-connection-native').textContent=L('Own Meta account','Cuenta propia de Meta');
  $('muse-connection').value=shown?.connection||'kilo';
  $('muse-connection').disabled=!loaded||loading||saving||working||!!pending;
  $('muse-account').textContent=native()?L('Muse Code · Meta login','Muse Code · inicio de sesión de Meta'):'Kilo Proxy · Muse Code';
  $('muse-account-help').textContent=native()?L('Use /login in Muse, then /models after signing in. Your Kilo login does not sign in to Meta. Your normal Muse configuration and chats are kept.','Usa /login en Muse. Tras iniciar sesión, usa /models. Tu sesión de Kilo no inicia sesión en Meta. Se conservan tu configuración y chats habituales de Muse.'):L('Uses your saved shared models through Kilo Proxy in a separate Muse profile. Your Meta account and its chats stay separate.','Usa tus modelos compartidos guardados a través de Kilo Proxy en un perfil Muse separado. Tu cuenta de Meta y sus chats siguen separados.');
  $('muse-models').replaceChildren();
  const models=!native()&&same(profile?.preferences,preferences)?profile?.effectiveModels||[]:[];
  for(const model of models){const li=document.createElement('li');li.textContent=(model.name?model.name+' · ':'')+model.id;$('muse-models').append(li);}
  $('muse-models-help').textContent=native()?L('The catalog belongs to your Meta account; Kilo shared models do not configure it.','El catálogo pertenece a tu cuenta de Meta; los modelos compartidos de Kilo no lo configuran.'):L('Choose and save models in the native app’s Models page, then refresh here. Kilo prepares that exact selection.','Elige y guarda modelos en Modelos de la app nativa y actualiza aquí. Kilo prepara esa selección exacta.');
  $('muse-executable').textContent=L('Resolved executable: ','Ejecutable detectado: ')+(executable()||L('Not detected','Sin detectar'));
  $('muse-terminal-help').textContent=L('kilo-muse follows this connection choice. Keep Kilo Proxy open for Kilo models; the native Meta connection also works with Kilo closed.','kilo-muse sigue esta conexión. Mantén Kilo Proxy abierto para los modelos de Kilo; la conexión nativa de Meta también funciona con Kilo cerrado.');
  $('muse-status').textContent=translate(reason()||(working?L('Preparing Muse…','Preparando Muse…'):profile?.message||''),ctx.language);
  $('muse-status').classList.toggle('error',!!error);
  $('muse-reload').textContent=L('Refresh settings and models','Actualizar ajustes y modelos');$('muse-reload').disabled=loading||saving||working;
  $('muse-retry').textContent=L('Retry saving','Reintentar guardado');$('muse-retry').hidden=!pending||!error;$('muse-retry').disabled=loading||saving||working;
  onChange();if(!loaded&&!loading)void reload();
 }
 async function reload(){
  if(loading||saving||working)return;
  loading=true;error='';const generation=++revision;render();
  try{
   const [settings,info,library]=await Promise.allSettled([api('clients/muse/preferences'),api('clients/muse'),api('model-library')]);
   if(generation!==revision)return;
   if(settings.status!=='fulfilled')throw settings.reason;if(!musePreferencesValid(settings.value?.preferences))throw new Error(L('Could not read the Muse connection. Reload settings.','No se pudo leer la conexión de Muse. Carga los ajustes.'));
   if(info.status!=='fulfilled')throw info.reason;
   preferences=structuredClone(settings.value.preferences);profile=info.value;
   if(!same(profile.preferences,preferences))throw new Error(L('Muse settings changed. Reload before opening.','Los ajustes de Muse han cambiado. Carga de nuevo antes de abrir.'));
   if(library.status!=='fulfilled'&&!native())throw library.reason;
   saved=library.status==='fulfilled'?library.value:null;pending=null;
  }catch(failure){if(generation===revision)error=failure.message;}
  finally{if(generation===revision){loaded=true;loading=false;render();}}
 }
 async function savePreferences(next){
  if(!musePreferencesValid(next)||!loaded||loading||saving||working)return;
  pending=structuredClone(next);saving=true;error='';const generation=++revision;if(profile)profile={...profile,prepared:false};render();
  try{
   const result=await api('clients/muse/preferences',next);if(generation!==revision)return;
   if(!musePreferencesValid(result?.preferences)||!same(result.preferences,next))throw new Error(L('Muse did not confirm the saved connection. Reload settings before opening.','Muse no confirmó la conexión guardada. Carga los ajustes antes de abrir.'));
   preferences=structuredClone(result.preferences);pending=null;const nextProfile=await api('clients/muse');if(generation!==revision)return;if(!same(nextProfile?.preferences,preferences))throw new Error(L('Muse settings changed. Reload before opening.','Los ajustes de Muse han cambiado. Carga de nuevo antes de abrir.'));profile=nextProfile;
  }catch(failure){if(generation===revision)error=failure.message;}
  finally{if(generation===revision){saving=false;render();}}
 }
 async function prepare(){
  if(reason()||working)throw new Error(reason()||L('Wait for Muse preparation.','Espera a que termine la preparación de Muse.'));
  const prefs=structuredClone(preferences),path=executable(),library=native()?null:structuredClone(saved.library),before=signature(),gateway=connection(),generation=revision;
  working=true;error='';render();
  try{
   const [settings,currentInfo,currentModels]=await Promise.all([api('clients/muse/preferences'),api('clients/muse'),library?api('model-library'):Promise.resolve(null)]);
   if(!same(settings?.preferences,prefs)||currentInfo.resolvedExecutable!==path||before!==signature())throw new Error(L('Muse connection or executable changed. Refresh and open again.','La conexión o el ejecutable Muse han cambiado. Actualiza y vuelve a abrir.'));
   if(library&&(!openMausBotLibraryReady(currentModels)||!same(currentModels.library,library)))throw new Error(L('The shared models changed. Refresh and open again.','Los modelos compartidos han cambiado. Actualiza y vuelve a abrir.'));
   const result=await api('clients/muse',{preferences:prefs,museExecutable:path,...(library?{library}: {})});
   if(!musePrepared(library?{library}:null,result,prefs,path))throw new Error(L('Muse did not confirm the prepared profile.','Muse no confirmó el perfil preparado.'));
   const [confirmed,confirmedInfo,confirmedModels]=await Promise.all([api('clients/muse/preferences'),api('clients/muse'),library?api('model-library'):Promise.resolve(null)]);
   if(generation!==revision||before!==signature()||!same(confirmed?.preferences,prefs)||confirmedInfo.resolvedExecutable!==path||library&&(!same(confirmedModels?.library,library)||connection()!==gateway))throw new Error(L('Muse settings changed while preparing. Review them and open again.','Los ajustes de Muse cambiaron durante la preparación. Revísalos y vuelve a abrir.'));
   profile=result;return result;
  }catch(failure){error=failure.message;throw failure;}
  finally{working=false;render();}
 }
 $('muse-connection').addEventListener('change',()=>void savePreferences({version:1,connection:$('muse-connection').value}));
 $('muse-reload').addEventListener('click',()=>void reload());$('muse-retry').addEventListener('click',()=>{if(pending)void savePreferences(pending);});
 return {render,reload,savePreferences,preferences:()=>preferences&&structuredClone(preferences),launchState:()=>({id:'muse',nativeOnly:native(),count:native()?0:saved?.library?.models?.length||0,ready:false,valid:!reason(),reason:reason(),working:loading||saving||working||!!pending,fingerprint:signature(),museConnection:preferences?.connection,museExecutable:executable(),prepare})};
}
