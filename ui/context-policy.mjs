export const contextPresets = ['recommended', 'low', 'maximum', 'custom'];
export const recommendedContext = 272000;
const tokens = value => Number.isSafeInteger(value) && value >= 1024 && value <= 100000000;

// Catalog limits and user choices have different lifetimes. In-memory helpers
// keep the observed maximum separately; models.json stores only the choice.
export function contextModel(model, catalogModel, saved = false) {
 const fresh = catalogModel !== undefined;
 const maximumOutputTokens = fresh ? (Number.isSafeInteger(catalogModel.maxOutputTokens) && catalogModel.maxOutputTokens > 0 ? catalogModel.maxOutputTokens : 0) : model.maximumOutputTokens ?? (!saved && Number.isSafeInteger(model.maxOutputTokens) && model.maxOutputTokens > 0 ? model.maxOutputTokens : 0);
 const maximum = fresh ? (tokens(catalogModel.contextWindow) ? catalogModel.contextWindow : 0) : Object.hasOwn(model,'contextMaximum') ? model.contextMaximum : !saved && tokens(model.contextWindow) ? model.contextWindow : 0;
 const preset = model.contextPreset || (saved && tokens(model.contextWindow) ? 'custom' : 'recommended');
 return {...model, contextPreset:preset, contextTokens:model.contextTokens ?? (preset === 'custom' ? model.contextWindow || recommendedContext : 0), contextMaximum:maximum,maximumOutputTokens};
}

export function resolveContextPolicy(preset = 'recommended', customTokens = 0, knownMaximum = 0, outputTokens = 0) {
 if (!contextPresets.includes(preset)) throw new Error('Choose a valid context preset.');
 const maximum = tokens(knownMaximum) ? knownMaximum : 0;
 if (preset === 'maximum' && !maximum) throw new Error('Refresh the catalog to discover this model’s maximum context, or choose another preset.');
 if (preset === 'custom' && !tokens(customTokens)) throw new Error('Custom context must be a whole number between 1,024 and 100,000,000 tokens.');
 const requested = preset === 'low' ? 128000 : preset === 'maximum' ? maximum : preset === 'custom' ? customTokens : recommendedContext;
 const contextWindow = maximum ? Math.min(requested, maximum) : requested;
 const maxOutputTokens = Math.min(Number.isSafeInteger(outputTokens) && outputTokens > 0 ? outputTokens : 8192, Math.floor(contextWindow / 4));
 const autoCompactTokenLimit = Math.min(Math.floor(contextWindow * .9), contextWindow - maxOutputTokens - Math.min(8192, Math.floor(contextWindow / 20)));
 return {contextWindow, maxOutputTokens, autoCompactTokenLimit};
}
export function contextLimits(model) {
 const choice = contextModel(model);
 const output = Number(model.maxOutputTokens) > 0 ? Number(model.maxOutputTokens) : 8192;
 return resolveContextPolicy(choice.contextPreset, choice.contextTokens, choice.contextMaximum, choice.maximumOutputTokens > 0 ? Math.min(output,choice.maximumOutputTokens) : output);
}
export function contextLibraryFields(model) {
 const choice = contextModel(model);
 return {contextPreset:choice.contextPreset, ...(choice.contextPreset === 'custom' ? {contextWindow:choice.contextTokens} : {})};
}

export function syncContextModels(models, catalog) {
 const byID = new Map(catalog.map(model => [model.id,model]));
 for (const model of models.values()) Object.assign(model,contextModel(model,byID.get(model.id) || {id:model.id}));
}
export function contextError(models) {
 try {for (const model of models.values()) contextLimits(model);return '';}
 catch(error) {return error.message;}
}
export function contextPreview(build) {try{return build();}catch(error){return {error:error.message};}}

// Shared by the browser helpers; no client-specific duplicate controls.
export function contextControls(model, {language = 'en', catalogModel, disabled = false, onChange = () => {}} = {}) {
 Object.assign(model, contextModel(model, catalogModel));
 const L = (en, es) => language === 'es' ? es : en;
 const field = document.createElement('fieldset'), title = document.createElement('legend'), buttons = document.createElement('div');
 field.className = 'context-policy'; title.textContent = L('Context window', 'Ventana de contexto'); buttons.className = 'context-preset-buttons';
 field.append(title, buttons);
 const custom = document.createElement('label'), input = document.createElement('input'), summary = document.createElement('small');
 custom.textContent = L('Context tokens', 'Tokens de contexto'); input.type = 'number'; input.min = '1024'; input.max = String(model.contextMaximum || 100000000); input.step = '1';
 input.value = String(model.contextPreset==='custom' ? model.contextTokens : Math.min(recommendedContext, model.contextMaximum || recommendedContext));
 input.setAttribute('aria-label', L('Context tokens: ', 'Tokens de contexto: ') + model.id); input.dataset.focus = 'context-custom:' + model.id;
 custom.append(input); summary.className = 'context-policy-summary'; summary.setAttribute('aria-live', 'polite');
 field.append(custom, summary);
 const labels = {recommended:L('Recommended · 272K', 'Recomendado · 272K'), low:L('Low · 128K','Bajo · 128K'), maximum:L('Maximum', 'Máximo'), custom:L('Custom', 'Personalizado')};
 function refresh() {
  for (const button of buttons.children) {button.setAttribute('aria-pressed', String(button.dataset.contextPreset === model.contextPreset)); button.disabled = disabled || button.dataset.contextPreset === 'maximum' && !model.contextMaximum;}
  custom.hidden = model.contextPreset !== 'custom'; input.disabled = disabled;
  try {
   const effective = contextLimits(model), count = n => n.toLocaleString(language);
   summary.textContent = L('Working: ', 'Trabajo: ') + count(effective.contextWindow) + L(' · Maximum: ', ' · Máximo: ') + (model.contextMaximum ? count(model.contextMaximum) : L('unknown', 'desconocido')) + L(' · Output cap: ', ' · Salida máxima: ') + count(effective.maxOutputTokens);
   input.setCustomValidity('');
  } catch (error) {summary.textContent = error.message; input.setCustomValidity(error.message);}
 }
 for (const preset of contextPresets) {
  const button = document.createElement('button'); button.type = 'button'; button.textContent = labels[preset]; button.dataset.contextPreset = preset; button.dataset.focus = 'context-' + preset + ':' + model.id;
  if (preset === 'maximum' && !model.contextMaximum) button.title = L('The catalog has not reported a maximum for this model.', 'El catálogo no ha publicado el máximo de este modelo.');
  button.addEventListener('click', () => {model.contextPreset = preset; if (preset === 'custom' && !model.contextTokens) model.contextTokens = Math.min(recommendedContext, model.contextMaximum || recommendedContext); input.value = String(model.contextTokens || recommendedContext); refresh(); onChange();});
  buttons.append(button);
 }
 input.addEventListener('input', () => {const value = Number(input.value); if (!tokens(value)) {model.contextTokens=value;input.setCustomValidity(L('Enter a whole number of at least 1,024 tokens.', 'Introduce un número entero de al menos 1.024 tokens.'));onChange();return;} model.contextTokens = value; refresh(); onChange();});
 refresh(); return field;
}
