import {contextLimits} from './context-policy.mjs';
import {validModelID} from './model-helper.mjs';

const levels=['minimal','low','medium','high','xhigh','max'];
export function ompEfforts(model) {
 if(model.reasoning===false)return [];
 const declared=Array.isArray(model.reasoningEfforts)?model.reasoningEfforts:[];
 return levels.filter(level=>declared.includes(level));
}
export function ompPayload(models,initial) {
 return {models:models.map(model=>{
  const reasoningEfforts=ompEfforts(model);
  return {
   id:model.id,name:(model.displayName||model.name||model.id).slice(0,80),
   contextWindow:contextLimits(model).contextWindow,
   maxOutputTokens:contextLimits(model).maxOutputTokens,
   reasoning:reasoningEfforts.length>0,
   ...(reasoningEfforts.length?{reasoningEfforts}:{}),
   ...(reasoningEfforts.includes(model.effort)?{effort:model.effort}:{}),
   ...(Array.isArray(model.inputModalities)?{inputModalities:['text',...(model.inputModalities.includes('image')?['image']:[])]}:{}),
   ...(typeof model.inputPrice==='number'&&Number.isFinite(model.inputPrice)&&model.inputPrice>=0?{inputPrice:model.inputPrice}:{}),
   ...(typeof model.outputPrice==='number'&&Number.isFinite(model.outputPrice)&&model.outputPrice>=0?{outputPrice:model.outputPrice}:{}),
  };
 }),initial};
}
// JSON is valid YAML; use quoted strings to preserve exact model IDs and keys.
export function ompConfig({baseURL,key,selectedModels=[]}) {
 const {models}=ompPayload(selectedModels.filter(model=>validModelID(model.id)),'');
 return JSON.stringify({providers:{'kilo-local':{
  baseUrl:baseURL,api:'openai-responses',apiKey:key,authHeader:true,
  compat:{supportsStore:false,supportsReasoningSummary:false,supportsStrictMode:false},
  models:models.map(model=>({
   id:model.id,name:model.name,reasoning:model.reasoning,
   input:model.inputModalities||['text'],contextWindow:model.contextWindow,
   maxTokens:model.maxOutputTokens>0?model.maxOutputTokens:Math.min(8192,model.contextWindow),
   preferWebsockets:false,
   ...(model.reasoningEfforts?.length?{thinking:{mode:'effort',efforts:model.reasoningEfforts,...(model.effort?{defaultLevel:model.effort}:{})}}:{}),
  })),
 }}},null,2);
}
const quote=value=>"'"+String(value).replaceAll("'","'\\''")+"'";
const ps=value=>"'"+String(value).replaceAll("'","''")+"'";
export function ompLaunch(profileDir,model,shell='unix') {
 if(shell==='powershell')return `& {
  $kiloOmpProfile = ${ps(profileDir)}
  if (!(Test-Path -LiteralPath (Join-Path $kiloOmpProfile 'models.yml') -PathType Leaf)) { throw 'Prepare Oh My Pi first.' }
  $kiloNames = @('PI_CODING_AGENT_DIR', 'OMP_PROFILE', 'PI_PROFILE', 'PI_OPENAI_STATEFUL')
  $kiloPrevious = @{}
  foreach ($kiloName in $kiloNames) { $kiloPrevious[$kiloName] = [Environment]::GetEnvironmentVariable($kiloName, 'Process') }
  try {
    $env:PI_CODING_AGENT_DIR = $kiloOmpProfile
    Remove-Item -LiteralPath Env:OMP_PROFILE -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath Env:PI_PROFILE -ErrorAction SilentlyContinue
    $env:PI_OPENAI_STATEFUL = '0'
    omp --model ${ps('kilo-local/'+model)}
  } finally {
    foreach ($kiloName in $kiloNames) {
      if ($null -eq $kiloPrevious[$kiloName]) {
        Remove-Item -LiteralPath "Env:$kiloName" -ErrorAction SilentlyContinue
      } else {
        Set-Item -LiteralPath "Env:$kiloName" -Value $kiloPrevious[$kiloName]
      }
    }
  }
}`;
 return `(\n  kilo_omp_profile=${quote(profileDir)}\n  [ -f "$kilo_omp_profile/models.yml" ] || { printf '%s\\n' 'Prepare Oh My Pi first.' >&2; exit 1; }\n  PI_CODING_AGENT_DIR="$kilo_omp_profile" OMP_PROFILE='' PI_PROFILE='' PI_OPENAI_STATEFUL=0 omp --model ${quote('kilo-local/'+model)}\n)`;
}
