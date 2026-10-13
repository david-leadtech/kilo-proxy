const endpoint='clients/cli/preferences';
const valid=value=>value?.version===1&&typeof value.codexExecutable==='string'&&typeof value.claudeExecutable==='string'&&(value.museExecutable===undefined||typeof value.museExecutable==='string')&&(value.synaraExecutable===undefined||typeof value.synaraExecutable==='string');
const signature=value=>JSON.stringify([value?.version,value?.codexExecutable,value?.claudeExecutable,value?.museExecutable??'',value?.synaraExecutable??'']);
const openCodePaths=value=>JSON.stringify([value?.executable1,value?.executable2]);
export const cliPathClient=client=>['codex-cli','claude','muse','t3-code','synara','open-design','xcode-codex','xcode-claude'].includes(client);
export function cliPreferenceOperation(path,body,method) {
 if(method!=='POST')return '';
 if(path==='clients/launch')return cliPathClient(body?.client)?body.client:'';
 return ({'codex-cli/catalog':'codex-cli','claude/profile':'claude','clients/t3-code':'t3-code','clients/synara':'synara','open-design/profile':'open-design','xcode/codex':'xcode-codex','xcode/claude':'xcode-claude'})[path]||'';
}

export function createCLIPreferencesHelper({api,notify,onChange=()=>{},onPreferencesSaved=()=>{},openCodeSnapshot=()=>null,openCodePathsBlocked=()=>false,onOpenCodePathsChanged=()=>{}}) {
 const $=id=>document.getElementById(id);
 const preparedPaths=new Map();
 let ctx={language:'en'},preferences=null,pending=null,error='',loaded=false,loading=false,saving=false,revision=0,changed=false,drafts={codexExecutable:'',claudeExecutable:'',museExecutable:'',synaraExecutable:''};
 const L=(en,es)=>ctx.language==='en'?en:es;
 const dirty=()=>!!preferences&&Object.keys(drafts).some(key=>drafts[key].trim()!==preferences[key]);
 const blocked=client=>!loaded||loading||saving||!!pending||dirty()||client==='t3-code'&&openCodePathsBlocked();
 const changedError=()=>new Error(L('CLI executable settings changed. Review the paths and prepare again.','Las rutas de las CLI cambiaron. Revísalas y prepara de nuevo.'));
 function accept(next,discard=false){next={...next,museExecutable:next.museExecutable??'',synaraExecutable:next.synaraExecutable??''};const wasChanged=!!preferences&&signature(preferences)!==signature(next);for(const key of Object.keys(drafts))if(discard||!preferences||drafts[key].trim()===preferences[key])drafts[key]=next[key];preferences={...next};loaded=true;if(wasChanged){preparedPaths.clear();onPreferencesSaved();}}
 function render(context=ctx) {
  ctx=context;if(!loaded&&!loading&&!error)void reload();
  $('cli-preferences-title').textContent=L('Application and CLI installations','Instalaciones de aplicaciones y CLI');
  $('cli-preferences-help').textContent=L('Choose an installed application or CLI, including a custom version. Empty paths use the current verified Kilo copy first, then the system installation. T3 Code, Synara and Open Design use these same CLI paths. Accounts and chats are kept.','Elige una aplicación o CLI instalada, incluida una versión personalizada. Las rutas vacías usan primero la copia Kilo actual verificada y después la instalación del sistema. T3 Code, Synara y Open Design usan las mismas rutas de las CLI. Se conservan las cuentas y los chats.');
  $('cli-preferences-muse-help').textContent=L('Muse Code uses its own Meta login and official models in the terminal and T3 Code. It does not use your Kilo gateway or shared model selection.','Muse Code usa su propio inicio de sesión de Meta y sus modelos oficiales en la terminal y T3 Code. No usa tu gateway de Kilo ni la selección compartida de modelos.');
  $('cli-preferences-synara-help').textContent=L('On Linux, select a persistent extracted Synara executable. A raw AppImage can be detected but is not ready to open: install a verified Kilo copy or select the extracted executable. Detection does not verify its source, version or channel. Ubuntu sandbox restrictions still apply.','En Linux, selecciona un ejecutable de Synara extraído en una ubicación permanente. Un AppImage sin extraer puede detectarse, pero no está listo para abrir: instala una copia Kilo verificada o selecciona el ejecutable extraído. La detección no verifica su origen, versión ni canal. Se mantienen las restricciones del sandbox de Ubuntu.');
  for(const [client,key,label]of [['codex-cli','codexExecutable','Codex CLI'],['claude','claudeExecutable','Claude Code'],['muse','museExecutable','Muse Code'],['synara','synaraExecutable','Synara']]) {
   const field=$('cli-path-'+client),editable=loaded&&!loading&&!saving&&!pending;
   $('cli-path-label-'+client).textContent=label+(client==='synara'?L(' application path (optional)',' · ruta de la aplicación (opcional)'):L(' executable path (optional)',' · ruta del ejecutable (opcional)'));
   if(field.value!==drafts[key])field.value=drafts[key];field.disabled=!editable;field.placeholder=L('Absolute path; empty = automatic detection','Ruta absoluta; vacía = detección automática');
   $('cli-path-save-'+client).textContent=L('Save path','Guardar ruta')+' · '+label;$('cli-path-save-'+client).disabled=!editable||drafts[key].trim()===preferences?.[key];
   $('cli-path-auto-'+client).textContent=L('Automatic detection','Detección automática')+' · '+label;$('cli-path-auto-'+client).disabled=!editable||(!drafts[key].trim()&&!preferences?.[key]);
   const detected=ctx.launchInfo?.clients?.[client];$('cli-path-resolved-'+client).textContent=ctx.launchDetecting?L('Checking executable…','Comprobando ejecutable…'):detected?.path?L('Resolved executable: ','Ejecutable detectado: ')+detected.path:'';
  }
  $('cli-preferences-status').textContent=error||(saving?L('Saving CLI paths…','Guardando rutas de las CLI…'):loading?L('Loading CLI paths…','Cargando rutas de las CLI…'):dirty()?L('Save the executable paths before preparing or opening a harness.','Guarda las rutas de los ejecutables antes de preparar o abrir un harness.'):changed?L('Paths saved. Close the affected Kilo workspace and prepare again. Chats are kept.','Rutas guardadas. Cierra el espacio Kilo afectado y prepara de nuevo. Los chats se conservan.'):'');
  $('cli-preferences-status').classList.toggle('error',!!error);
  $('cli-preferences-retry').hidden=!pending||loading||saving;$('cli-preferences-retry').textContent=L('Retry saving','Reintentar guardado');
  $('cli-preferences-reload').disabled=loading||saving;$('cli-preferences-reload').textContent=L('Reload paths','Cargar rutas');
  onChange();
 }
 async function reload(){if(loading||saving)return;loading=true;error='';const generation=++revision;render();try{const next=await api(endpoint);if(generation!==revision)return;if(!valid(next))throw new Error(L('Could not read CLI executable settings.','No se pudieron leer las rutas de las CLI.'));accept(next,true);pending=null;}catch(failure){if(generation===revision)error=failure.message;}finally{if(generation===revision){loading=false;render();}}}
 async function save(next){if(!loaded||loading||saving||!valid(next))return;pending={...next};saving=true;error='';const generation=++revision;render();try{const result=await api(endpoint,next);if(generation!==revision)return;if(!valid(result)||signature(result)!==signature(next))throw new Error(L('CLI settings did not confirm the saved paths.','No se confirmaron las rutas guardadas de las CLI.'));accept(result);pending=null;changed=true;}catch(failure){if(generation===revision){error=failure.message;notify(failure.message,true);}}finally{if(generation===revision){saving=false;render();}}}
 function savePath(key,automatic){if(!loaded||loading||saving||pending)return;const path=automatic?'':drafts[key].trim();if(path&&!path.startsWith('/')&&!/^[a-zA-Z]:[\\/]/.test(path)&&!/^\\\\[^\\]+\\[^\\]+/.test(path)){error=L('Enter an absolute path to the CLI executable, or leave it empty for automatic detection.','Introduce una ruta absoluta al ejecutable CLI, o déjala vacía para la detección automática.');render();return;}drafts[key]=path;void save({...preferences,[key]:path});}
 for(const [client,key]of [['codex-cli','codexExecutable'],['claude','claudeExecutable'],['muse','museExecutable'],['synara','synaraExecutable']]) {
  $('cli-path-'+client).addEventListener('input',()=>{if(!loaded||loading||saving||pending)return;drafts[key]=$('cli-path-'+client).value;error='';render();});
  $('cli-path-save-'+client).addEventListener('click',()=>savePath(key,false));$('cli-path-auto-'+client).addEventListener('click',()=>savePath(key,true));
 }
 $('cli-preferences-retry').addEventListener('click',()=>{if(pending)void save({...pending});});$('cli-preferences-reload').addEventListener('click',()=>void reload());
 function prepared(client) {
  const saved=preparedPaths.get(client),known=client==='t3-code'&&openCodeSnapshot();
  return !blocked(client)&&!!saved&&saved.cli===signature(preferences)&&(!known||saved.openCode===openCodePaths(known));
 }
 async function guardOperation(client,operation,{preparation=true}={}) {
  if(!loaded&&!loading)await reload();
  if(blocked(client))throw changedError();
  const expected={...preferences},generation=revision;
  const check=async()=>{const current=await api(endpoint);if(generation!==revision||blocked(client))throw changedError();if(!valid(current)||signature(current)!==signature(expected)){if(valid(current)){accept(current);changed=true;render();}throw changedError();}};
  await check();
  let expectedOpenCode;
  if(client==='t3-code') {
   const current=await api('clients/opencode/preferences'),known=openCodeSnapshot();expectedOpenCode=openCodePaths(current);
   if(known&&expectedOpenCode!==openCodePaths(known)){preparedPaths.delete(client);onOpenCodePathsChanged();throw changedError();}
  }
  const paths={cli:signature(expected),...(client==='t3-code'?{openCode:expectedOpenCode}:{})};
  if(generation!==revision||blocked(client))throw changedError();
  if(!preparation&&JSON.stringify(preparedPaths.get(client))!==JSON.stringify(paths)){preparedPaths.delete(client);if(client==='t3-code')onOpenCodePathsChanged();throw changedError();}
  if(preparation)preparedPaths.delete(client);
  const result=await operation();
  await check();
  if(client==='t3-code'&&expectedOpenCode!==openCodePaths(await api('clients/opencode/preferences'))){preparedPaths.delete(client);onOpenCodePathsChanged();throw changedError();}
  if(generation!==revision||blocked(client))throw changedError();
  if(preparation)preparedPaths.set(client,paths);
  return result;
 }
 return {render,reload,blocked,prepared,guardOperation,signature:()=>JSON.stringify([preferences,pending,drafts,revision])};
}
