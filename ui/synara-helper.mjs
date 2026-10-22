import {connectionReady} from './chatgpt-helper.mjs';
import {translate} from './i18n.mjs';
import {openMausBotLibraryReady} from './openmausbot-helper.mjs';

const librarySignature = library => JSON.stringify(library);
const openCodeConnection = value => value?.openCodeConnection || 'native';
const openCodeUsesKilo = value => value?.providers?.includes('opencode') && openCodeConnection(value) === 'kilo';
const normalizePreferences = value => ({...structuredClone(value),openCodeConnection:openCodeConnection(value)});
const preferencesSignature = value => JSON.stringify(value && [value.version,value.codex,value.claude,value.kiloCodex,value.kiloClaude,value.codexModels,value.claudeModels,[...(value.providers ?? [])].sort(),value.openCode2,openCodeConnection(value)]);
const defaultFirst = (models, id) => [...models.filter(model => model.id === id), ...models.filter(model => model.id !== id)];
const knownProviders = {cursor:'Cursor',grok:'Grok',opencode:'OpenCode',antigravity:'Antigravity',pi:'Pi'};
export function synaraPreferencesValid(value) {
 return value?.version === 1 && ['official','shared'].includes(value.codexModels) && ['official','shared'].includes(value.claudeModels) && ['codex','claude','kiloCodex','kiloClaude','openCode2'].every(key => typeof value[key] === 'boolean') && Array.isArray(value.providers) && value.providers.every(id => typeof id === 'string' && Object.hasOwn(knownProviders,id)) && new Set(value.providers).size === value.providers.length && (!value.openCode2 || value.providers.includes('opencode')) && [undefined,'','native','kilo'].includes(value.openCodeConnection);
}
export function synaraNativeOnly(value) {
 return synaraPreferencesValid(value) && !value.kiloCodex && !value.kiloClaude && !openCodeUsesKilo(value);
}
export function synaraPrepared(saved, profile, preferences) {
 if (synaraNativeOnly(preferences)) return Boolean(profile?.prepared === true && preferencesSignature(preferences) === preferencesSignature(profile.preferences));
 return Boolean(openMausBotLibraryReady(saved) && profile?.prepared === true && librarySignature(saved.library) === librarySignature(profile.library) && (!preferences || preferencesSignature(preferences) === preferencesSignature(profile.preferences)));
}

// Settings and launch snapshots belong to the separate Kilo workspace.
export function createSynaraHelper({api, onChange, onPreferencesConfirmed = () => {}}) {
 const $ = id => document.getElementById(id);
 let ctx = {state:null, language:'en'}, saved = null, profile = null, preferences = null, capabilities = [];
 let loaded = false, loading = false, working = false, saving = false, error = '', preferencesError = '', pendingPreferences = null, profileConnection = '', requiresPreparation = false, openCode2Supported = false, openCodeKiloSupported = false, claudePickerSupported = false, generation = 0;
 const L = (en, es) => ctx.language === 'en' ? en : es;
 const connectionSignature = () => JSON.stringify([ctx.state?.baseURL, ctx.state?.localKey, ctx.state?.orgId, ctx.state?.connectionReady, ctx.state?.chatgpt?.connected]);
 const nativeOnly = () => synaraNativeOnly(preferences);
 const prepared = () => !saving && !pendingPreferences && (nativeOnly() || profileConnection === connectionSignature()) && synaraPrepared(saved, profile, preferences);
 const tooManyModels = () => (saved?.library?.models?.length || 0) > 32 && (preferences?.kiloCodex && preferences.codexModels === 'shared' || preferences?.kiloClaude && preferences.claudeModels === 'shared');
 const openCodeKiloUnavailable = () => openCodeUsesKilo(preferences) && !openCodeKiloSupported;
 const claudePickerUnavailable = () => preferences?.kiloClaude && !claudePickerSupported;
 const claudePickerReason = () => L('This Synara installation cannot restrict the Kilo Claude picker to its prepared models. Choose the native Claude account or a compatible macOS build.','Esta instalación de Synara no puede limitar el selector Claude Kilo a sus modelos preparados. Elige la cuenta propia de Claude o una compilación compatible de macOS.');
 const unavailableSelection = () => (preferences?.providers ?? []).some(id=>!capabilities.some(row=>row.id===id)) ? L('Turn off unavailable saved choices or install a compatible Synara build before preparing.','Desactiva las selecciones guardadas no disponibles o instala una compilación Synara compatible antes de preparar.') : preferences?.openCode2 && !openCode2Supported ? L('This Synara build cannot use the saved OpenCode 2 choice. Choose OpenCode 1, disable OpenCode or install a compatible Synara build.','Esta compilación de Synara no admite la selección guardada de OpenCode 2. Elige OpenCode 1, desactiva OpenCode o instala una compilación Synara compatible.') : '';
 const ready = () => !claudePickerUnavailable() && !openCodeKiloUnavailable() && !unavailableSelection() && loaded && (nativeOnly() || openMausBotLibraryReady(saved)) && synaraPreferencesValid(preferences) && !tooManyModels() && !pendingPreferences && !saving;
 const signature = () => JSON.stringify([nativeOnly() ? null : connectionSignature(), nativeOnly() ? null : saved?.library, preferences]);
 const reason = () => claudePickerUnavailable() ? claudePickerReason() : openCodeKiloUnavailable() ? L('This Synara build cannot use the Kilo OpenCode connection. Choose your own OpenCode account, disable OpenCode or install a compatible macOS build.', 'Esta compilación de Synara no admite la conexión Kilo de OpenCode. Elige tu cuenta propia de OpenCode, desactiva OpenCode o instala una compilación compatible de macOS.') : !nativeOnly() && !connectionReady(ctx.state) ? L('Connect your selected provider first.', 'Conecta primero el proveedor seleccionado.') : preferencesError || error || unavailableSelection() || (loading ? L('Loading Synara settings…', 'Cargando ajustes de Synara…') : tooManyModels() ? L('Synara supports up to 32 shared models per account. Reduce your selection in Models or choose official models.', 'Synara admite hasta 32 modelos compartidos por cuenta. Reduce la selección en Modelos o elige modelos oficiales.') : saving ? L('Wait for Synara settings to finish saving.', 'Espera a que termine el guardado de los ajustes de Synara.') : !ready() ? L('Add and save models in the native app’s Models page, then refresh here.', 'Añade y guarda modelos en la página Modelos de la app nativa y actualiza aquí.') : '');
 const visibleProviders = value => [...capabilities.map(row=>({...row,supported:true})),...(value?.providers||[]).filter(id=>knownProviders[id]&&!capabilities.some(row=>row.id===id)).map(id=>({id,label:knownProviders[id],supported:false}))];
 const acceptPreferences = response => {
  if (!synaraPreferencesValid(response?.preferences)) throw new Error(L('Could not read Synara preferences. Refresh to try again.', 'No se pudieron leer las preferencias de Synara. Actualiza para reintentarlo.'));
  const changed = preferences && preferencesSignature(preferences) !== preferencesSignature(response.preferences);
  preferences = normalizePreferences(response.preferences);
  if (Array.isArray(response.supportedProviders)) capabilities = response.supportedProviders.filter(row => knownProviders[row?.id] && typeof row?.label === 'string');
  openCode2Supported = response.openCode2Supported === true;
  openCodeKiloSupported = response.openCodeKiloSupported === true;
  claudePickerSupported = response.claudePickerSupported === true;
  requiresPreparation = response.requiresPreparation === true;
  if (changed) onPreferencesConfirmed();
 };
 function renderPreferences() {
  const displayed = saving && pendingPreferences ? pendingPreferences : preferences;
  $('synara-settings-title').textContent = L('Accounts and models', 'Cuentas y modelos');
  $('synara-settings-help').textContent = L('Changes save automatically and apply only to the separate Kilo workspace. Close Synara · Kilo and prepare it again; your chats and existing CLI logins are kept.', 'Los cambios se guardan solos y solo afectan al espacio Kilo separado. Cierra Synara · Kilo y prepara de nuevo; se conservan tus chats y sesiones CLI existentes.');
  $('synara-accounts-label').textContent = L('Accounts', 'Cuentas');
  const editable = loaded && !!preferences && !loading && !working && !saving && !pendingPreferences;
  for (const key of ['codex','claude','kiloCodex','kiloClaude']) {
   const node = $('synara-account-'+key); node.checked = displayed?.[key] === true; node.disabled = !editable;
  }
  $('synara-codex-models-label').textContent = L('Codex Kilo models', 'Modelos de Codex Kilo');
  $('synara-claude-models-label').textContent = L('Claude Kilo models', 'Modelos de Claude Kilo');
  $('synara-codex-official').textContent = L('OpenAI and ChatGPT', 'OpenAI y ChatGPT');
  $('synara-claude-official').textContent = L('Opus 5.5, Sonnet 5.5 and Fable 5.1', 'Opus 5.5, Sonnet 5.5 y Fable 5.1');
  for (const kind of ['codex','claude']) {
   $('synara-'+kind+'-shared').textContent = L('All shared models', 'Todos los modelos compartidos');
   const select = $('synara-'+kind+'-models'); select.value = displayed?.[kind+'Models'] ?? 'official'; select.disabled = !editable || !displayed?.[kind === 'codex' ? 'kiloCodex' : 'kiloClaude'];
  }
  $('synara-reasoning-help').textContent = L('Compatible native Claude models use the reasoning levels selected in Kilo. Other shared models keep their compatible defaults. Kilo Claude’s filtered model list requires a compatible private macOS runtime.', 'Los modelos Claude nativos compatibles usan sus niveles de razonamiento seleccionados en Kilo. Los otros modelos compartidos conservan sus valores iniciales compatibles. La lista filtrada de Claude Kilo requiere un runtime privado compatible de macOS.');
  if (claudePickerUnavailable()) $('synara-reasoning-help').textContent += ' '+claudePickerReason();
  $('synara-extra-providers-label').textContent = L('Other providers', 'Otros proveedores');
  $('synara-provider-options').replaceChildren();
  const visible = visibleProviders(displayed);
  for (const provider of visible) {
   const label = document.createElement('label'); label.className = 'checkbox-label';
   const input = document.createElement('input'); input.type = 'checkbox'; input.id = 'synara-provider-'+provider.id; input.checked = displayed?.providers.includes(provider.id) === true; input.disabled = !editable;
   input.addEventListener('change', () => { if (input.checked && !provider.supported) {input.checked=false;return;} const next = structuredClone(displayed); next.providers = next.providers.filter(id => id !== provider.id); if (input.checked) {next.providers.push(provider.id);} if (provider.id === 'opencode' && !input.checked) next.openCode2 = false; void savePreferences(next); });
   const text = document.createElement('span'); text.textContent = provider.label+(provider.supported?'':L(' · unavailable in this build',' · no disponible en esta compilación')); label.append(input); label.append(text); $('synara-provider-options').append(label);
  }
  $('synara-providers-help').textContent = visible.some(row=>!row.supported) ? L('Turn off unavailable saved choices or install a compatible Synara build before preparing.','Desactiva las selecciones guardadas no disponibles o instala una compilación Synara compatible antes de preparar.') : capabilities.length ? capabilities.some(row=>row.id==='opencode') ? L('Choose the OpenCode connection below. Other providers use their own logins.', 'Elige debajo la conexión de OpenCode. Los demás proveedores usan sus sesiones propias.') : L('These providers use their own installations and logins.', 'Estos proveedores usan sus instalaciones y sesiones propias.') : L('This Synara build does not expose additional compatible providers.', 'Esta compilación de Synara no ofrece proveedores compatibles adicionales.');
  const hasOpenCode = visible.some(row=>row.id === 'opencode');
  $('synara-opencode-connection-field').hidden = !hasOpenCode;
  $('synara-opencode-connection-help').hidden = !hasOpenCode;
  $('synara-opencode-connection-label').textContent = L('OpenCode connection', 'Conexión de OpenCode');
  $('synara-opencode-connection-native').textContent = L('Own OpenCode account', 'Cuenta propia de OpenCode');
  $('synara-opencode-connection-kilo').textContent = L('Kilo Proxy · shared models', 'Kilo Proxy · modelos compartidos');
  $('synara-opencode-connection').value = openCodeConnection(displayed);
  $('synara-opencode-connection').disabled = !editable || !displayed?.providers.includes('opencode') || !capabilities.some(row=>row.id === 'opencode');
  $('synara-opencode-connection-kilo').disabled = !openCodeKiloSupported;
  $('synara-opencode-connection-help').textContent = !openCodeKiloSupported ? L('This Synara build cannot use the Kilo OpenCode connection. Choose your own OpenCode account, disable OpenCode or install a compatible macOS build.', 'Esta compilación de Synara no admite la conexión Kilo de OpenCode. Elige tu cuenta propia de OpenCode, desactiva OpenCode o instala una compilación compatible de macOS.') : L('Your own account uses OpenCode’s login and catalog. Kilo uses the models saved in Models through the proxy, with a separate profile. Its prepared model list requires a compatible private macOS runtime. Close Synara · Kilo and prepare again after changing this connection.', 'Tu cuenta propia usa la sesión y el catálogo de OpenCode. Kilo usa los modelos guardados en Modelos a través del proxy, con un perfil separado. Su lista de modelos preparados requiere un runtime privado compatible de macOS. Cierra Synara · Kilo y prepara de nuevo tras cambiar esta conexión.');
  const hasOpenCode2 = openCode2Supported && capabilities.some(row => row.id === 'opencode') || displayed?.openCode2;
  $('synara-opencode-version-field').hidden = !hasOpenCode2;
  $('synara-opencode-help').hidden = !hasOpenCode2;
  $('synara-opencode-version-label').textContent = L('OpenCode version', 'Versión de OpenCode');
  $('synara-opencode-version').value = displayed?.openCode2 ? '2' : '1';
  $('synara-opencode-version').disabled = !editable || !displayed?.providers.includes('opencode');
  $('synara-opencode-version-2').disabled = !openCode2Supported;
  $('synara-opencode-help').textContent = !openCode2Supported && displayed?.openCode2 ? L('This Synara build cannot use the saved OpenCode 2 choice. Choose OpenCode 1, disable OpenCode or install a compatible Synara build.','Esta compilación de Synara no admite la selección guardada de OpenCode 2. Elige OpenCode 1, desactiva OpenCode o instala una compilación Synara compatible.') : L('Choose one version for this account. OpenCode 2 requires 2.0.18 or newer; both versions are never started together with the same profile.', 'Elige una versión para esta cuenta. OpenCode 2 requiere 2.0.18 o posterior; nunca se abren ambas versiones a la vez con el mismo perfil.');
  if (openCodeConnection(displayed) === 'native' && (openCode2Supported || !displayed?.openCode2)) $('synara-opencode-help').textContent += L(' OpenCode 2 has a separate profile that may need its own login.', ' OpenCode 2 tiene un perfil separado que puede requerir su propio inicio de sesión.');
  $('synara-settings-status').textContent = translate(preferencesError || (saving ? L('Saving Synara settings…', 'Guardando ajustes de Synara…') : loading ? L('Loading Synara settings…', 'Cargando ajustes de Synara…') : requiresPreparation && !prepared() ? L('Settings saved. Close Synara · Kilo and prepare again. Your chats are kept.', 'Ajustes guardados. Cierra Synara · Kilo y prepara de nuevo. Tus chats se conservan.') : ''), ctx.language);
  $('synara-settings-status').classList.toggle('error', !!preferencesError);
  $('synara-settings-retry').hidden = !pendingPreferences || !preferencesError;
  $('synara-settings-retry').textContent = L('Retry saving', 'Reintentar guardado'); $('synara-settings-retry').disabled = saving || loading || working;
  $('synara-settings-reload').textContent = L('Reload settings', 'Cargar ajustes'); $('synara-settings-reload').disabled = saving || loading || working;
 }
 function render(next = ctx) {
  ctx = next;
  $('synara-intro').textContent = L('A separate Synara workspace with the accounts and models you choose. Kilo Proxy names and green account indicators identify Kilo accounts.', 'Un espacio Synara separado con las cuentas y modelos que elijas. Los nombres Kilo Proxy y los indicadores verdes identifican las cuentas Kilo.');
  const sameProfile = preferencesSignature(preferences) === preferencesSignature(profile?.preferences);
  const codexModels = sameProfile ? profile?.effectiveCodexModels ?? [] : [], claudeModels = sameProfile ? profile?.effectiveClaudeModels ?? [] : [];
  const openCodeModels = openCodeUsesKilo(preferences) && sameProfile ? profile?.effectiveOpenCodeModels ?? [] : [];
  $('synara-summary').textContent = `Kilo Proxy · Codex: ${codexModels.length} · Kilo Proxy · Claude: ${claudeModels.length}` + (openCodeUsesKilo(preferences) ? ` · Kilo Proxy · OpenCode: ${openCodeModels.length}` : '');
  $('synara-models').replaceChildren();
  for (const [name, models] of [['Kilo Proxy · Codex',defaultFirst(codexModels,profile?.library?.defaultModel)],['Kilo Proxy · Claude',defaultFirst(claudeModels,profile?.library?.defaultModel)],['Kilo Proxy · OpenCode',openCodeModels]]) {
   for (const model of models) { const item = document.createElement('li'); const label = model.name ?? model.displayName; item.textContent = name+' · '+(label ? label+' · ' : '')+model.id; $('synara-models').append(item); }
  }
  $('synara-library-help').textContent = L('Choose official models or your saved shared library in Synara settings below. Existing Codex and Claude accounts keep their CLI login.', 'Elige modelos oficiales o tu biblioteca compartida guardada en los ajustes de Synara. Las cuentas existentes de Codex y Claude conservan su sesión CLI.');
  $('synara-agents-title').textContent = L('Enabled accounts', 'Cuentas activadas');
  $('synara-agents').replaceChildren();
  const accounts = [];
  if (preferences?.codex) accounts.push(L('Codex · Existing Codex CLI login', 'Codex · Sesión existente de Codex CLI'));
  if (preferences?.kiloCodex) accounts.push(L('Kilo Proxy · Codex · Models through the proxy', 'Kilo Proxy · Codex · Modelos a través del proxy'));
  if (preferences?.claude) accounts.push(L('Claude · Existing Claude Code login', 'Claude · Sesión existente de Claude Code'));
  if (preferences?.kiloClaude) accounts.push(L('Kilo Proxy · Claude · Models through the proxy', 'Kilo Proxy · Claude · Modelos a través del proxy'));
  for (const provider of visibleProviders(preferences)) if (preferences?.providers.includes(provider.id)) {
   let label = provider.label;
   if (provider.id === 'opencode') label = (openCodeUsesKilo(preferences) ? 'Kilo Proxy · ' : '')+(preferences.openCode2 ? 'OpenCode 2' : 'OpenCode')+(openCodeUsesKilo(preferences) ? L(' · Shared models through the proxy', ' · Modelos compartidos a través del proxy') : L(' · Own login and catalog', ' · Sesión y catálogo propios'));
   accounts.push(label+(provider.supported?'':L(' · unavailable in this build',' · no disponible en esta compilación')));
  }
  for (const text of accounts) { const item = document.createElement('li'); item.textContent = text; $('synara-agents').append(item); }
  $('synara-options').textContent = L('Advanced', 'Avanzado');
  $('synara-requirements').textContent = L('Supports official Synara stable and Beta releases. Preparation checks the installed contract and CLIs required by your enabled accounts. Codex requires file-based CLI authentication; keyring/auto authentication is not supported. Existing accounts use CLI sessions, not Desktop app logins.', 'Admite versiones oficiales estables y Beta de Synara. La preparación comprueba el contrato instalado y los CLI que requieren las cuentas activadas. Codex requiere autenticación CLI en archivo; no se admite autenticación keyring/auto. Las cuentas existentes usan sesiones CLI, no los inicios de sesión de las apps Desktop.') + (profile?.version ? L(' Detected: ', ' Detectado: ') + profile.version + '.' : '');
  $('synara-compatibility').textContent = L('Choose an agent for each new chat. Existing chats keep their account; start a new chat to switch between native and Kilo accounts. Select the exact gateway model ID shown in the prepared list and check the selected model before continuing. The Kilo Claude picker shows only prepared models. A compatible native Claude choice maps to your exact gateway ID when its family and version match one prepared model. For custom Claude gateway IDs, compatible reasoning defaults come from Kilo Models. Close and reopen Synara · Kilo after changing them; saved levels require Claude Code 2.1.251 or newer (2.1.267 or newer for Opus/Sonnet 5.5). Generation and tools depend on the model and provider.', 'Elige un agente para cada chat nuevo. Los chats existentes conservan su cuenta; inicia uno nuevo para cambiar entre cuentas nativas y Kilo. Selecciona el ID exacto del gateway mostrado en la lista preparada y revisa el modelo elegido antes de continuar. El selector Claude Kilo sólo muestra los modelos preparados. Una opción nativa compatible de Claude se asigna a tu ID exacto del gateway cuando su familia y versión coinciden con un modelo preparado. Para IDs Claude personalizados del gateway, el razonamiento compatible inicial viene de Modelos de Kilo. Cierra y reabre Synara · Kilo tras cambiarlos; los niveles guardados requieren Claude Code 2.1.251 o posterior (2.1.267 o posterior para Opus/Sonnet 5.5). La generación y las herramientas dependen del modelo y del proveedor.');
  $('synara-voice').textContent = L('You can disable unused accounts without blocking the other agents. Voice notes require Codex enabled and signed in with ChatGPT through codex login, even in Claude Kilo chats. Allow Synara microphone access when prompted.', 'Puedes desactivar las cuentas que no uses sin bloquear los demás agentes. Las notas de voz requieren Codex activado y autenticado con ChatGPT mediante codex login, incluso en chats Claude Kilo. Concede acceso al micrófono a Synara cuando lo solicite.');
  $('synara-privacy').textContent = L('Your regular Synara workspace and chats stay separate and can remain open. Normal agents reference your existing CLI sessions; Kilo Proxy does not copy login credentials. Quit only Synara · Kilo before changing models or the connection, then reopen it.', 'Tu espacio Synara habitual y sus chats siguen separados y pueden permanecer abiertos. Los agentes normales referencian tus sesiones CLI existentes; Kilo Proxy no copia credenciales de inicio de sesión. Cierra solo Synara · Kilo antes de cambiar los modelos o la conexión y vuelve a abrirlo.');
  $('synara-linux').textContent = L('On Linux, a detected AppImage is a package candidate, not a prepared installation. Install a verified Kilo copy or select a persistent extracted Synara executable in Application and CLI installations. Kilo never starts an AppImage during detection and keeps the sandbox enabled. Ubuntu AppArmor/user-namespace restrictions can block a private Electron copy. The private macOS runtime’s account-aware delegation has not been validated on Linux.', 'En Linux, un AppImage detectado es un paquete candidato, no una instalación preparada. Instala una copia Kilo verificada o selecciona un ejecutable de Synara extraído en una ubicación permanente en Instalaciones de aplicaciones y CLI. Kilo nunca inicia un AppImage durante la detección y mantiene el sandbox activado. Las restricciones de AppArmor y los espacios de nombres de usuario de Ubuntu pueden bloquear una copia privada de Electron. La delegación entre cuentas del runtime privado de macOS no se ha validado en Linux.');
  $('synara-reload').textContent = loading ? L('Loading…', 'Cargando…') : L('Refresh models', 'Actualizar modelos'); $('synara-reload').disabled = loading || working || saving;
  $('synara-prepare').textContent = working ? L('Preparing…', 'Preparando…') : L('Prepare without opening', 'Preparar sin abrir'); $('synara-prepare').disabled = working || loading || !ready() || !nativeOnly() && !connectionReady(ctx.state);
  $('synara-install').textContent = L('Get Synara ↗', 'Obtener Synara ↗');
  $('synara-prepared').textContent = prepared() ? L('Your saved Synara accounts and models are prepared.', 'Las cuentas y modelos guardados de Synara están preparados.') : L('Prepare or open Synara · Kilo to apply your saved accounts and models.', 'Prepara o abre Synara · Kilo para aplicar las cuentas y modelos guardados.');
  $('synara-status').textContent = translate(error || (working ? L('Preparing Synara accounts…', 'Preparando cuentas de Synara…') : (!profile?.prepared || prepared()) ? profile?.message || '' : ''), ctx.language); $('synara-status').classList.toggle('error', !!error);
  renderPreferences(); onChange();
  if (!loaded && !loading) void reload();
 }
 async function reload() {
  if (loading || working || saving) return;
  loading = true; error = ''; preferencesError = ''; const currentGeneration = ++generation, connection = connectionSignature(); render();
  try {
   const [libraryResult, profileResult, preferencesResult] = await Promise.allSettled([api('model-library'), api('clients/synara'), api('clients/synara/preferences')]);
   if (generation !== currentGeneration) return;
   if (preferencesResult.status !== 'fulfilled') throw preferencesResult.reason;
   if (profileResult.status !== 'fulfilled') throw profileResult.reason;
   acceptPreferences(preferencesResult.value);
   if (libraryResult.status !== 'fulfilled' && !nativeOnly()) throw libraryResult.reason;
   saved = libraryResult.status === 'fulfilled' ? libraryResult.value : null; profile = profileResult.value; profileConnection = connection; pendingPreferences = null;
  } catch (failure) { if (generation === currentGeneration) { saved = null; profile = null; preferences = null; error = failure.message; preferencesError = failure.message; } }
  finally { if (generation === currentGeneration) { loaded = true; loading = false; render(); } }
 }
 async function savePreferences(next) {
  if (saving || working || loading || !synaraPreferencesValid(next)) return;
  const snapshot = normalizePreferences(next); saving = true; preferencesError = ''; pendingPreferences = snapshot; const currentGeneration = ++generation;
  if (profile) profile = {...profile, prepared:false}; render();
  try {
   const response = await api('clients/synara/preferences', snapshot);
   if (generation !== currentGeneration) return;
   if (preferencesSignature(response?.preferences) !== preferencesSignature(snapshot)) throw new Error(L('Synara did not confirm the saved preferences. Reload settings before preparing.', 'Synara no confirmó las preferencias guardadas. Carga los ajustes antes de preparar.'));
   acceptPreferences(response); requiresPreparation = true; pendingPreferences = null;
   const nextProfile = await api('clients/synara');
   if (generation === currentGeneration) { profile = nextProfile; profileConnection = connectionSignature(); }
  } catch (failure) { if (generation === currentGeneration) preferencesError = failure.message; }
  finally { if (generation === currentGeneration) { saving = false; render(); } }
 }
 async function prepare() {
  if (working || loading || !ready() || !nativeOnly() && !connectionReady(ctx.state)) throw new Error(reason());
  const native = nativeOnly();
  working = true; error = ''; const snapshot = native ? null : structuredClone(saved.library), preferencesSnapshot = structuredClone(preferences), connection = connectionSignature(); render();
  try {
   const [latest, settings] = await Promise.all([native ? Promise.resolve(null) : api('model-library'), api('clients/synara/preferences')]);
   if (!native && (!openMausBotLibraryReady(latest) || librarySignature(latest.library) !== librarySignature(snapshot))) { saved = latest; throw new Error(L('The shared models changed. Review the refreshed list and open again.', 'Los modelos compartidos han cambiado. Revisa la lista actualizada y vuelve a abrir.')); }
   if (preferencesSignature(settings.preferences) !== preferencesSignature(preferencesSnapshot)) { acceptPreferences(settings); throw new Error(L('Synara settings changed. Review them and open again.', 'Los ajustes de Synara han cambiado. Revísalos y vuelve a abrir.')); }
   profile = await api('clients/synara', native ? {preferences:preferencesSnapshot} : {library:snapshot, preferences:preferencesSnapshot}); profileConnection = connection;
   if (profile?.prepared !== true || preferencesSignature(profile.preferences) !== preferencesSignature(preferencesSnapshot)) throw new Error(L('Synara did not confirm the prepared accounts and models. Refresh before opening.','Synara no confirmó las cuentas y modelos preparados. Actualiza antes de abrir.'));
   const [current, currentSettings] = await Promise.all([native ? Promise.resolve(null) : api('model-library'), api('clients/synara/preferences')]);
   if (!native && (!openMausBotLibraryReady(current) || librarySignature(current.library) !== librarySignature(snapshot))) { saved = current; throw new Error(L('The shared models changed while preparing. Review the refreshed list and open again.', 'Los modelos compartidos cambiaron durante la preparación. Revisa la lista actualizada y vuelve a abrir.')); }
   if (preferencesSignature(currentSettings.preferences) !== preferencesSignature(preferencesSnapshot) || !native && connectionSignature() !== connection) { acceptPreferences(currentSettings); throw new Error(L('Synara settings or connection changed while preparing. Review them and open again.', 'Los ajustes o la conexión de Synara cambiaron durante la preparación. Revísalos y vuelve a abrir.')); }
   requiresPreparation = false; return profile;
  } catch (failure) { error = failure.message; throw failure; }
  finally { working = false; render(); }
 }
 for (const key of ['codex','claude','kiloCodex','kiloClaude']) $('synara-account-'+key).addEventListener('change', () => { if (preferences) void savePreferences({...structuredClone(preferences), [key]:$('synara-account-'+key).checked}); });
 for (const kind of ['codex','claude']) $('synara-'+kind+'-models').addEventListener('change', () => { if (preferences) void savePreferences({...structuredClone(preferences), [kind+'Models']:$('synara-'+kind+'-models').value}); });
 $('synara-opencode-connection').addEventListener('change', () => { if (!preferences) return; const value = $('synara-opencode-connection').value; if (!['native','kilo'].includes(value) || value === 'kilo' && !openCodeKiloSupported) {$('synara-opencode-connection').value=openCodeConnection(preferences);return;} void savePreferences({...structuredClone(preferences),openCodeConnection:value}); });
 $('synara-opencode-version').addEventListener('change', () => { if (!preferences) return; if ($('synara-opencode-version').value === '2' && !openCode2Supported) {$('synara-opencode-version').value=preferences.openCode2?'2':'1';return;} void savePreferences({...structuredClone(preferences), openCode2:$('synara-opencode-version').value === '2'}); });
 $('synara-settings-retry').addEventListener('click', () => { if (pendingPreferences) void savePreferences(pendingPreferences); });
 $('synara-settings-reload').addEventListener('click', () => { void reload(); });
 $('synara-reload').addEventListener('click', () => { void reload(); });
 $('synara-prepare').addEventListener('click', () => { void prepare().catch(() => {}); });
 return {render, reload, savePreferences, preferences:() => preferences && structuredClone(preferences), launchState:() => ({id:'synara', nativeOnly:nativeOnly(), count:(profile?.effectiveCodexModels?.length ?? 0)+(profile?.effectiveClaudeModels?.length ?? 0)+(openCodeUsesKilo(preferences) && preferencesSignature(preferences) === preferencesSignature(profile?.preferences) ? profile?.effectiveOpenCodeModels?.length ?? 0 : 0), ready:false, valid:ready() && (nativeOnly() || connectionReady(ctx.state)), reason:reason(), working:working || loading || saving, fingerprint:signature(), prepare})};
}
