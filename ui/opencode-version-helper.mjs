import {connectionReady} from './chatgpt-helper.mjs';
const validPreferences = value => value?.version === 1 && [1,2].includes(value.major) && typeof value.executable1==='string' && typeof value.executable2==='string';
const preferencesSignature = value => JSON.stringify([value?.version,value?.major,value?.executable1,value?.executable2]);
const librarySignature = value => JSON.stringify(value);

export function createOpenCodeVersionHelper({api,notify,onChange=()=>{},onPreferencesSaved=()=>{}}) {
 const $=id=>document.getElementById(id);
 let ctx={language:'en'},preferences=null,pending=null,library=null,profile=null,requested=false,loading=false,saving=false,working=false,error='',revision=0,drafts={1:'',2:''};
 const L=(en,es)=>ctx.language==='en'?en:es;
 const connection=()=>JSON.stringify([ctx.state?.baseURL,ctx.state?.localKey,ctx.state?.orgId,ctx.state?.connectionReady,ctx.state?.chatgpt?.connected]);
 const signature=()=>JSON.stringify([preferences,library,connection()]);
 const pathDirty=()=>!!preferences&&[1,2].some(major=>drafts[major].trim()!==preferences['executable'+major]);
 const available=()=>validPreferences(preferences)&&!loading&&!saving&&!pending&&!pathDirty();
 const ready=()=>available()&&preferences.major===2&&!!library?.models?.length&&!working&&connectionReady(ctx.state);
 const changed=()=>new Error(L('The OpenCode version or models changed. Review them and open again.','La versión de OpenCode o los modelos cambiaron. Revísalos y vuelve a abrir.'));
 function acceptPreferences(next,discardDraft=false) {
  for(const major of [1,2])if(discardDraft||!preferences||drafts[major].trim()===preferences['executable'+major])drafts[major]=next['executable'+major];
  preferences={...next};
 }
 function render(context=ctx) {
  ctx=context;
  if(!requested)void reload();
  const displayed=saving&&pending?pending:preferences;
  $('opencode-version-title').textContent=L('OpenCode version','Versión de OpenCode');
  $('opencode-version-label').textContent=L('Version to open','Versión que quieres abrir');
  $('opencode-version').value=String(displayed?.major||1);
  $('opencode-version').disabled=!available()||working;
  $('opencode-version-help').textContent=L('OpenCode 2 uses a separate Kilo profile. Existing OpenCode 1 accounts and chats are kept.','OpenCode 2 usa un perfil Kilo separado. Se conservan las cuentas y chats de OpenCode 1.');
  const major=displayed?.major||1,pathEditable=validPreferences(preferences)&&!loading&&!saving&&!pending&&!working;
  $('opencode-executable-label').textContent=L('OpenCode executable path (optional)','Ruta del ejecutable de OpenCode (opcional)');
  if($('opencode-executable').value!==drafts[major])$('opencode-executable').value=drafts[major];
  $('opencode-executable').placeholder=L('Absolute path; empty = automatic detection','Ruta absoluta; vacía = detección automática');
  $('opencode-executable').disabled=!pathEditable;
  $('opencode-executable-help').textContent=L('This path applies only to the selected version. T3 Code uses the same paths, with its own choice of OpenCode 1 or 2.','Esta ruta solo se aplica a la versión seleccionada. T3 Code usa las mismas rutas y elige OpenCode 1 o 2 por separado.');
  $('opencode-executable-save').textContent=L('Save executable path','Guardar ruta del ejecutable');
  $('opencode-executable-save').disabled=!pathEditable||!pathDirty();
  $('opencode-executable-auto').textContent=L('Use automatic detection','Usar detección automática');
  $('opencode-executable-auto').disabled=!pathEditable||(!pathDirty()&&!preferences?.['executable'+major]);
  const detected=ctx.launchInfo?.clients?.[major===2?'opencode2':'opencode'];
  $('opencode-executable-resolved').textContent=ctx.launchDetecting?L('Checking executable…','Comprobando ejecutable…'):detected?.path?L('Resolved executable: ','Ejecutable detectado: ')+detected.path:detected?L('No compatible executable detected for this version.','No se ha detectado un ejecutable compatible con esta versión.'):'';
  $('opencode-version-status').textContent=error||(saving?L('Saving settings…','Guardando ajustes…'):loading?L('Loading settings…','Cargando ajustes…'):pathDirty()?L('Save the executable path before opening or changing version.','Guarda la ruta del ejecutable antes de abrir o cambiar de versión.'):'');
  $('opencode-version-status').classList.toggle('error',!!error);
  $('opencode-version-retry').hidden=!pending||saving||loading;
  $('opencode-version-retry').textContent=L('Retry saving','Reintentar guardado');
  $('opencode-version-reload').hidden=!error||saving||loading;
  $('opencode-version-reload').textContent=L('Reload settings','Cargar ajustes');
  $('opencode2-title').textContent=L('OpenCode 2 · shared models','OpenCode 2 · modelos compartidos');
  $('opencode2-intro').textContent=L('Opens a project terminal with your saved shared model library in a separate Kilo profile. Authentication and chats stay separate from OpenCode 1.','Abre una terminal del proyecto con tu biblioteca compartida guardada en un perfil Kilo separado. La autenticación y los chats quedan separados de OpenCode 1.');
  $('opencode2-summary').textContent=library?L(`${library.models?.length||0} saved shared models`,`${library.models?.length||0} modelos compartidos guardados`):L('Loading the shared model library…','Cargando la biblioteca compartida…');
  $('opencode2-models').replaceChildren();
  for(const model of library?.models||[]) {const row=document.createElement('li');row.textContent=(model.displayName||model.id)+' · '+model.id;$('opencode2-models').append(row);}
  $('opencode2-status').textContent=profile?.prepared?L('Profile prepared. Opening checks the current models and connection again.','Perfil preparado. Al abrir se comprueban de nuevo los modelos y la conexión.'):L('Prepare or open to apply your saved models.','Prepara o abre para aplicar los modelos guardados.');
  $('opencode2-prepare').textContent=working?L('Preparing…','Preparando…'):L('Prepare without opening','Preparar sin abrir');
  $('opencode2-prepare').disabled=!ready();
  $('opencode2-install').textContent=L('Install OpenCode 2.0.18+ manually, then check detection again ↗','Instala OpenCode 2.0.18+ manualmente y vuelve a comprobar la detección ↗');
  onChange();
 }
 async function loadModelsAndProfile(generation) {
  const [source,prepared]=await Promise.all([api('model-library'),api('clients/opencode2')]);
  if(generation!==revision)return;
  library=source.library;profile=prepared;
 }
 async function reload() {
  if(loading||saving||working)return;
  requested=true;loading=true;error='';const generation=++revision;render();
  try {
   const next=await api('clients/opencode/preferences');
   if(generation!==revision)return;
   if(!validPreferences(next))throw new Error(L('Could not read the OpenCode version choice.','No se pudo leer la versión elegida de OpenCode.'));
   acceptPreferences(next,true);pending=null;
   if(next.major===2)await loadModelsAndProfile(generation);
  }catch(failure){if(generation===revision)error=failure.message;}finally{if(generation===revision){loading=false;render();}}
 }
 async function save(next) {
  if(!validPreferences(next)||!preferences||saving||loading||working)return;
  pending={...next};saving=true;error='';const generation=++revision;render();
  try {
   const confirmed=await api('clients/opencode/preferences',next);
   if(generation!==revision)return;
   if(!validPreferences(confirmed)||preferencesSignature(confirmed)!==preferencesSignature(next))throw new Error(L('OpenCode did not confirm the saved settings.','OpenCode no confirmó los ajustes guardados.'));
   acceptPreferences(confirmed);pending=null;profile=null;
   if(confirmed.major===2)await loadModelsAndProfile(generation);
   onPreferencesSaved();
  }catch(failure){if(generation===revision){error=failure.message;notify(failure.message,true);}}finally{if(generation===revision){saving=false;render();}}
 }
 async function guardVersion(expected) {
  const current=await api('clients/opencode/preferences');
  if(!validPreferences(current)||preferencesSignature(current)!==preferencesSignature(expected)||pathDirty()) {
   if(validPreferences(current)){acceptPreferences(current);profile=null;if(current.major===2)await loadModelsAndProfile(revision);render();}
   throw changed();
  }
 }
 async function prepare() {
  if(!ready())throw changed();
  const generation=revision,before=signature(),chosen=structuredClone(library),chosenPreferences={...preferences};
  working=true;render();
  try {
   const [current,source]=await Promise.all([api('clients/opencode/preferences'),api('model-library')]);
   if(generation!==revision||signature()!==before||!validPreferences(current)||preferencesSignature(current)!==preferencesSignature(chosenPreferences)||pathDirty()||librarySignature(source.library)!==librarySignature(chosen)) {
    if(generation===revision){if(validPreferences(current))acceptPreferences(current);library=source.library;profile=null;render();}
    throw changed();
   }
   const result=await api('clients/opencode2',{library:chosen});
   const [confirmed,after]=await Promise.all([api('clients/opencode/preferences'),api('model-library')]);
   if(generation!==revision||signature()!==before||!validPreferences(confirmed)||preferencesSignature(confirmed)!==preferencesSignature(chosenPreferences)||pathDirty()||librarySignature(after.library)!==librarySignature(chosen)) {
    if(generation===revision&&validPreferences(confirmed)){acceptPreferences(confirmed);library=after.library;profile=null;render();}
    throw changed();
   }
   profile=result;notify(L('OpenCode 2 profile prepared.','Perfil de OpenCode 2 preparado.'));
  }finally{working=false;render();}
 }
 $('opencode-version').addEventListener('change',()=>{if(available())void save({...preferences,major:Number($('opencode-version').value)});else render();});
 $('opencode-executable').addEventListener('input',()=>{if(preferences&&!loading&&!saving&&!pending&&!working){drafts[preferences.major]=$('opencode-executable').value;error='';render();}});
 function saveExecutable(automatic=false) {
  if(!validPreferences(preferences)||loading||saving||pending||working)return;
  const major=preferences.major,path=automatic?'':drafts[major].trim();
  if(path&&!path.startsWith('/')&&!/^[a-zA-Z]:[\\/]/.test(path)&&!/^\\\\[^\\]+\\[^\\]+/.test(path)) {
   error=L('Enter an absolute path to the OpenCode executable, or leave it empty for automatic detection.','Introduce una ruta absoluta al ejecutable de OpenCode, o déjala vacía para la detección automática.');render();return;
  }
  drafts[major]=path;void save({...preferences,['executable'+major]:path});
 }
 $('opencode-executable-save').addEventListener('click',()=>saveExecutable());
 $('opencode-executable-auto').addEventListener('click',()=>saveExecutable(true));
 $('opencode-version-retry').addEventListener('click',()=>{if(pending)void save({...pending});});
 $('opencode-version-reload').addEventListener('click',()=>void reload());
 $('opencode2-prepare').addEventListener('click',()=>void prepare().catch(failure=>notify(failure.message,true)));
 return {render,reload,toolPaths:()=>preferences?{...preferences}:null,pathsBlocked:()=>loading||saving||!!pending||pathDirty()||requested&&!validPreferences(preferences),major:()=>preferences?.major||1,blocked:()=>!available()||working,
  launchState(fallback) {
   if(preferences?.major!==2){const chosenPreferences=preferences?{...preferences}:null;return {...fallback,working:fallback.working||!available(),valid:available()&&fallback.valid!==false,fingerprint:JSON.stringify([fallback.fingerprint,preferences,drafts]),prepare:async()=>{await guardVersion(chosenPreferences);await fallback.prepare();await guardVersion(chosenPreferences);}};}
   return {id:'opencode2',count:library?.models?.length||0,ready:false,valid:ready(),reason:error||(!library?.models?.length?L('Save at least one shared model before opening.','Guarda al menos un modelo compartido antes de abrir.'):''),working:working||loading||saving,fingerprint:signature(),prepare};
  }};
}
