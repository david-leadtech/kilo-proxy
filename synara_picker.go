package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const synaraPrivateWebRevision = 3
const synaraPrivateWebManifest = "web-runtime.json"
const synaraPrivateWebLimit = 128 << 20
const synaraPrivateWebFileLimit = 32 << 20

// The verified published clients contain 2,834/2,812 regular assets (including
// fonts and language chunks); keep a separate finite count and byte bound.
const synaraPrivateWebCountLimit = 4096

// Both published desktop channels use this resolver for the protocol and the
// backend's SYNARA_STATIC_DIR. An inherited variable alone does not change the
// desktop protocol's root. The runtime owner replaces this complete live block.
const synaraDesktopStaticRootAnchor = "function resolveServedStaticRoot() {\n\tif (servedStaticRootCache === void 0) servedStaticRootCache = computeServedStaticRoot();\n\treturn servedStaticRootCache;\n}"

type synaraPickerModel struct {
	Slug            string   `json:"slug"`
	Name            string   `json:"name"`
	NativeSlug      string   `json:"nativeSlug,omitempty"`
	GatewayID       string   `json:"gatewayId"`
	ReasoningLevels []string `json:"reasoningLevels"`
	DefaultEffort   string   `json:"defaultEffort,omitempty"`
}

type synaraPickerPolicy struct {
	InstanceID     string                      `json:"instanceId"`
	Models         []synaraPickerModel         `json:"models"`
	OpenCodeModels []synaraPickerOpenCodeModel `json:"openCodeModels,omitempty"`
}

type synaraPickerOpenCodeModel struct {
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	GatewayID      string   `json:"gatewayId"`
	VariantLevels  []string `json:"variantLevels"`
	DefaultVariant string   `json:"defaultVariant,omitempty"`
}

func synaraPrivateWebPolicyEnabled(policy synaraPickerPolicy) bool {
	return policy.InstanceID != "" || len(policy.OpenCodeModels) > 0
}

type synaraPrivateWebOptions struct {
	RootDir string
	Policy  synaraPickerPolicy
}

// Only public assets and model descriptors are copied. Files is relative to
// Root; the caller binds this record into its private runtime identity.
type synaraPrivateWeb struct {
	Revision           int               `json:"revision"`
	Root               string            `json:"root"`
	SourceArchive      string            `json:"sourceArchive"`
	SourceHeaderSHA256 string            `json:"sourceHeaderSHA256"`
	SourceSHA256       string            `json:"sourceSHA256"`
	PolicySHA256       string            `json:"policySHA256"`
	PickerEntry        string            `json:"pickerEntry"`
	ComposerEntry      string            `json:"composerEntry"`
	Files              map[string]string `json:"files"`
}

func validateSynaraPickerPolicy(policy synaraPickerPolicy) error {
	if !synaraPrivateWebPolicyEnabled(policy) && len(policy.Models) == 0 {
		return nil
	}
	if (policy.InstanceID != "" && policy.InstanceID != synaraClaudeProxyID) || (policy.InstanceID == "" && len(policy.Models) != 0) || (policy.InstanceID != "" && len(policy.Models) == 0) || len(policy.Models) > t3CodeCustomModelLimit {
		return errors.New("Invalid private Synara Claude picker selection.")
	}
	slugs, gateways := map[string]bool{}, map[string]bool{}
	for _, model := range policy.Models {
		if model.Slug == "" || model.GatewayID == "" || model.Name == "" || len(model.Slug) > 256 || len(model.GatewayID) > 256 || len(model.Name) > 512 || strings.ContainsAny(model.Slug+model.GatewayID+model.Name, "\x00\r\n") || slugs[model.Slug] || gateways[model.GatewayID] {
			return errors.New("Invalid private Synara Claude picker model.")
		}
		slugs[model.Slug], gateways[model.GatewayID] = true, true
		if model.NativeSlug != "" {
			if model.Slug != model.NativeSlug || t3CodeNativeClaudeModelID(model.GatewayID) != model.NativeSlug {
				return errors.New("Synara Claude picker native IDs must match their exact gateway model.")
			}
		} else if model.Slug != model.GatewayID {
			return errors.New("Synara custom picker models must retain their exact gateway ID.")
		}
		levels := map[string]bool{}
		for _, level := range model.ReasoningLevels {
			if levels[level] || !validClaudeEffort(model.GatewayID, level) || model.NativeSlug == "" {
				return errors.New("Invalid native Synara Claude picker effort.")
			}
			levels[level] = true
		}
		if model.DefaultEffort != "" && !levels[model.DefaultEffort] {
			return errors.New("Synara Claude picker default effort is outside its selected levels.")
		}
	}
	openCodeLibrary := modelLibrary{SchemaVersion: 1, Models: []modelLibraryItem{}}
	for _, model := range policy.OpenCodeModels {
		if model.Slug != "kilo-local/"+model.GatewayID || model.Name == "" || len(model.Slug) > 512 || len(model.Name) > 512 || strings.ContainsAny(model.Slug+model.Name, "\x00\r\n") {
			return errors.New("Invalid private Synara OpenCode picker model.")
		}
		openCodeLibrary.Models = append(openCodeLibrary.Models, modelLibraryItem{ID: model.GatewayID, ReasoningCustom: true, ReasoningLevels: append([]string{}, model.VariantLevels...), ReasoningEffort: model.DefaultVariant})
		if openCodeLibrary.DefaultModel == "" {
			openCodeLibrary.DefaultModel = model.GatewayID
		}
	}
	if err := validateModelLibrary(openCodeLibrary); err != nil {
		return err
	}
	return nil
}

// These complete expression contracts come from the official Stable/Beta
// client bundles. Binding identifiers differ; selecting a single full variant
// avoids treating a matching label or a partial function as compatibility.
var synaraPickerInstanceAnchors = []string{
	"r[n.instanceId]=u&&(u.length>0||d)?re({provider:n.provider,staticOptions:c,dynamicModels:u}):c,n.provider===`omp`",
	"r[n.instanceId]=u&&(u.length>0||d)?me({provider:n.provider,staticOptions:c,dynamicModels:u}):c,n.provider===`omp`",
}
var synaraPickerProviderAnchors = []string{
	"return Be&&(t.omp=e.omp.filter(e=>e.isCustom===!0)),t.omp=t.omp.filter(e=>!ce(e.slug,t.omp)),t},[F.data,M.data,N.data,P.data,X,d,L.data,I.data,i,R.data,z.data,B.data,Y.data,Be]),We=",
	"return Be&&(t.omp=e.omp.filter(e=>e.isCustom===!0)),t.omp=t.omp.filter(e=>!oe(e.slug,t.omp)),t},[F.data,M.data,N.data,P.data,X,d,L.data,I.data,i,R.data,z.data,B.data,Y.data,Be]),We=",
}

const synaraPickerRuntimeInstanceAnchor = "for(let t of f)e[t.instanceId]=p[t.instanceId]?.models??[];return e}"
const synaraPickerRuntimeProviderAnchor = "claudeAgent:M.data?.models??[],codex:N.data?.models??[]"
const synaraPickerRuntimeDependencies = "[F.data?.models,M.data?.models,N.data?.models,X,L.data?.models,I.data?.models,R.data?.models,z.data?.models,B.data?.models,Y.data?.models]),Ge="
const synaraPickerSelectedInstanceAnchor = "g=e=>({instanceId:(t===e?n?.trim():void 0)||e})"
const synaraPickerHookAnchor = "function Ce(e){let{selectedProvider:t,selectedProviderInstanceId:n,discoveryEnabled:r,modelHintByProvider:i}=e"

// Capability polling reads only the desktop entry and the unique picker chunk.
// Preparing the full immutable web copy remains an explicit operation.
func synaraInstalledPickerSupported(archive *t3CodeArchive) bool {
	policy := synaraPickerPolicy{InstanceID: synaraClaudeProxyID, Models: []synaraPickerModel{{Slug: "claude-opus-5-5", NativeSlug: "claude-opus-5-5", GatewayID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5", ReasoningLevels: []string{"low", "high"}, DefaultEffort: "low"}}}
	return synaraInstalledAccountPickerSupported(archive, policy)
}

func synaraInstalledOpenCodePickerSupported(archive *t3CodeArchive) bool {
	policy := synaraPickerPolicy{OpenCodeModels: []synaraPickerOpenCodeModel{{Slug: "kilo-local/openai/gpt-synthetic", GatewayID: "openai/gpt-synthetic", Name: "Synthetic model", VariantLevels: []string{"low", "high"}, DefaultVariant: "low"}}}
	return synaraInstalledAccountPickerSupported(archive, policy)
}

func synaraInstalledAccountPickerSupported(archive *t3CodeArchive, policy synaraPickerPolicy) bool {
	if archive == nil {
		return false
	}
	metadata, err := archive.read("package.json", 64<<10)
	if err != nil {
		return false
	}
	_, mainEntry, err := synaraRuntimePackageData(metadata)
	if err != nil {
		return false
	}
	main, err := archive.read(mainEntry, synaraRuntimeSourceLimit)
	if err != nil || !synaraLiveSourceAnchor(string(main), synaraDesktopStaticRootAnchor) || !synaraLiveSourceAnchor(string(main), synaraDesktopSpawnAnchor) {
		return false
	}
	assets := archive.tree
	for _, part := range []string{"apps", "server", "dist", "client", "assets"} {
		if assets.Link != "" || assets.Unpacked {
			return false
		}
		assets = assets.Files[part]
	}
	if assets.Files == nil || assets.Link != "" || assets.Unpacked || len(assets.Files) > synaraPrivateWebCountLimit {
		return false
	}
	picker, composer := "", ""
	for name, entry := range assets.Files {
		kind := ""
		if strings.HasPrefix(name, "useProviderModelCatalog-") && strings.HasSuffix(name, ".js") {
			kind = "picker"
		}
		if strings.HasPrefix(name, "SidechatDockPane-") && strings.HasSuffix(name, ".js") {
			kind = "composer"
		}
		if kind != "" {
			if (kind == "picker" && picker != "") || (kind == "composer" && composer != "") || entry.Files != nil || entry.Link != "" || entry.Unpacked || strings.ContainsAny(name, "/\\:\x00\r\n") {
				return false
			}
			if kind == "picker" {
				picker = name
			} else {
				composer = name
			}
		}
	}
	if picker == "" || composer == "" {
		return false
	}
	source, err := archive.read("apps/server/dist/client/assets/"+picker, synaraPrivateWebFileLimit)
	if err != nil {
		return false
	}
	if _, err = patchSynaraPickerSource(source, policy); err != nil {
		return false
	}
	source, err = archive.read("apps/server/dist/client/assets/"+composer, synaraPrivateWebFileLimit)
	if err != nil {
		return false
	}
	_, err = patchSynaraComposerSource(source, policy)
	return err == nil
}

const synaraNativeDefaultsConsumer = "function providerDefaultModel(provider) {\n\treturn provider === \"pi\" || provider === \"omp\" ? null : DEFAULT_MODEL_BY_PROVIDER[provider];\n}"

// Defaults are optional metadata, never a claim that a native account has a
// model. Bind the live consumer to its actual imported/exported shared literal;
// a same-named declaration in another chunk is not enough.
func synaraInstalledNativeDefaults(archive *t3CodeArchive) map[string]string {
	if archive == nil {
		return nil
	}
	server, err := archive.read("apps/server/dist/index.mjs", synaraRuntimeSourceLimit)
	if err != nil || !synaraLiveSourceAnchor(string(server), synaraNativeDefaultsConsumer) {
		return nil
	}
	source := string(server)
	imports := regexp.MustCompile(`import \{([^;\r\n]+)\} from "(\./MigrationBackup-[A-Za-z0-9_-]+\.mjs)";`)
	bindings := regexp.MustCompile(`(?:^|,)\s*([A-Za-z_$][A-Za-z0-9_$]*) as DEFAULT_MODEL_BY_PROVIDER\s*(?:,|$)`)
	var module, alias string
	mask := synaraContractCode(source)
	for _, match := range imports.FindAllStringSubmatchIndex(source, -1) {
		binding := bindings.FindAllStringSubmatchIndex(source[match[2]:match[3]], -1)
		if len(binding) == 0 {
			continue
		}
		if module != "" || len(binding) != 1 || mask[match[0]:match[0]+6] != "import" {
			return nil
		}
		begin, end := match[2]+binding[0][0], match[2]+binding[0][1]
		if mask[begin:end] != source[begin:end] {
			return nil
		}
		module = strings.TrimPrefix(source[match[4]:match[5]], "./")
		alias = source[match[2]+binding[0][2] : match[2]+binding[0][3]]
	}
	if module == "" {
		return nil
	}
	chunk, err := archive.read("apps/server/dist/"+module, synaraRuntimeSourceLimit)
	if err != nil {
		return nil
	}
	text := string(chunk)
	exports := regexp.MustCompile(`export \{([^;\r\n]+)\};`)
	exportBinding := regexp.MustCompile(`(?:^|,)\s*DEFAULT_MODEL_BY_PROVIDER as ` + regexp.QuoteMeta(alias) + `\s*(?:,|$)`)
	exported := 0
	mask = synaraContractCode(text)
	for _, match := range exports.FindAllStringSubmatchIndex(text, -1) {
		if binding := exportBinding.FindStringIndex(text[match[2]:match[3]]); binding != nil {
			begin, end := match[2]+binding[0], match[2]+binding[1]
			if mask[match[0]:match[0]+6] != "export" || mask[begin:end] != text[begin:end] {
				return nil
			}
			exported++
		}
	}
	const declaration = "const DEFAULT_MODEL_BY_PROVIDER = {"
	if exported != 1 || !synaraLiveSourceAnchor(text, declaration) {
		return nil
	}
	start := strings.Index(text, declaration) + len(declaration) - 1
	body, ok := t3CodeContractBody(text, start)
	if !ok || len(body) > 4096 || start+len(body) >= len(text) || text[start+len(body):start+len(body)+1] != ";" {
		return nil
	}
	literal := strings.TrimSpace(text[start+1 : start+len(body)-1])
	entry := regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*):\s*("(?:\\.|[^"\\])*")\s*(,|$)`)
	allowed := map[string]bool{"codex": true, "claudeAgent": true, "cursor": true, "devin": true, "antigravity": true, "grok": true, "droid": true, "opencode": true}
	defaults := map[string]string{}
	for literal != "" {
		match := entry.FindStringSubmatchIndex(literal)
		if match == nil {
			return nil
		}
		key := literal[match[2]:match[3]]
		var value string
		if !allowed[key] || defaults[key] != "" || json.Unmarshal([]byte(literal[match[4]:match[5]]), &value) != nil || value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			return nil
		}
		defaults[key] = value
		literal = strings.TrimSpace(literal[match[1]:])
	}
	if len(defaults) != len(allowed) {
		return nil
	}
	return defaults
}

func replaceSynaraPickerContract(source string, candidates []string, replacement func(string) string) (string, error) {
	mask := t3CodeContractCode(source)
	matched := ""
	for _, candidate := range candidates {
		count := strings.Count(source, candidate)
		if count > 1 {
			return "", errors.New("Ambiguous private Synara picker contract.")
		}
		if count == 1 {
			index := strings.Index(source, candidate)
			// Verify executable bytes before the first literal. A case clause's
			// provider literal starts immediately after its live `case` token.
			length := min(8, len(candidate))
			if literal := strings.IndexAny(candidate[:length], "'\"`"); literal >= 0 {
				length = literal
			}
			prefix := candidate[:length]
			if prefix == "" || matched != "" || mask[index:index+len(prefix)] != prefix {
				return "", errors.New("Invalid private Synara picker contract.")
			}
			matched = candidate
		}
	}
	if matched == "" {
		return "", errors.New("This Synara picker does not match the verified private account contract.")
	}
	return strings.Replace(source, matched, replacement(matched), 1), nil
}

func synaraPickerHelpers(policy synaraPickerPolicy) string {
	copy := policy
	copy.Models = append([]synaraPickerModel(nil), policy.Models...)
	for i := range copy.Models {
		copy.Models[i].ReasoningLevels = append([]string{}, copy.Models[i].ReasoningLevels...)
	}
	copy.OpenCodeModels = append([]synaraPickerOpenCodeModel{}, policy.OpenCodeModels...)
	for i := range copy.OpenCodeModels {
		copy.OpenCodeModels[i].VariantLevels = append([]string{}, copy.OpenCodeModels[i].VariantLevels...)
	}
	encoded, _ := json.Marshal(copy)
	return "\nconst __kiloSynaraClaudePolicy=" + string(encoded) + ";\n" +
		"function __kiloSynaraClaudeOptions(id,models){if(id===\"kilo_opencode_proxy\"&&__kiloSynaraClaudePolicy.openCodeModels?.length)return __kiloSynaraClaudePolicy.openCodeModels.map(row=>({...models.find(m=>m.slug===row.slug),slug:row.slug,name:row.name,isCustom:true}));if(!__kiloSynaraClaudePolicy.instanceId||id!==__kiloSynaraClaudePolicy.instanceId)return models;return __kiloSynaraClaudePolicy.models.map(row=>{const old=models.find(m=>m.slug===row.slug);return {...old,slug:row.slug,name:row.name,isCustom:!row.nativeSlug};});}\n" +
		"function __kiloSynaraClaudeRuntime(id,models){if(id===\"kilo_opencode_proxy\"&&__kiloSynaraClaudePolicy.openCodeModels?.length)return __kiloSynaraClaudePolicy.openCodeModels.map(row=>{const old=models.find(m=>m.slug===row.slug);const result={...old,slug:row.slug,name:row.name,kiloSynaraInstanceId:id,kiloSynaraVariantLevels:[...row.variantLevels],supportedReasoningEfforts:row.variantLevels.map(value=>({value,description:value})),optionDescriptors:[...(old?.optionDescriptors??[]).filter(d=>d.id!==\"variant\"),{id:\"variant\",label:\"Variant\",type:\"select\",options:row.variantLevels.map(id=>({id,label:id,...(id===row.defaultVariant?{isDefault:true}:{})}))}]};delete result.defaultReasoningEffort;delete result.kiloSynaraDefaultVariant;if(row.defaultVariant){result.defaultReasoningEffort=row.defaultVariant;result.kiloSynaraDefaultVariant=row.defaultVariant;}return result;});if(!__kiloSynaraClaudePolicy.instanceId||id!==__kiloSynaraClaudePolicy.instanceId)return models;return __kiloSynaraClaudePolicy.models.map(row=>{const old=models.find(m=>m.slug===row.slug);const result={...old,slug:row.slug,name:row.name,kiloSynaraInstanceId:id,supportedReasoningEfforts:row.reasoningLevels.map(value=>({value,description:value})),optionDescriptors:[...(old?.optionDescriptors??[]).filter(d=>d.id!==\"effort\"),{id:\"effort\",label:\"Reasoning effort\",type:\"select\",options:row.reasoningLevels.map(id=>({id,label:id,...(id===row.defaultEffort?{isDefault:true}:{})}))}]};delete result.defaultReasoningEffort;if(row.defaultEffort)result.defaultReasoningEffort=row.defaultEffort;return result;});}\n"
}

var synaraComposerFunctionAnchors = []string{
	"function q1(e){let{provider:t,model:n,runtimeModel:r,prompt:i,modelOptions:a}=e",
	"function VQ(e){let{provider:t,model:n,runtimeModel:r,prompt:i,modelOptions:a}=e",
}
var synaraComposerClaudeAnchors = []string{
	"case`claudeAgent`:{let e=a?.claudeAgent;s=ti(e?.effort),c=bi(n,e);break}",
	"case`claudeAgent`:{let e=a?.claudeAgent;s=Zr(e?.effort),c=_i(n,e);break}",
}

const synaraComposerResultAnchor = "return{provider:t,promptEffort:f,modelOptionsForDispatch:c"

var synaraComposerOpenCodeAnchors = []string{
	"case`opencode`:{let e=a?.opencode;s=ti(e?.variant);let t=o.variantOptions??[],n=s&&t.some(e=>e.value===s)?s:void 0,r=ti(e?.agent);if(t.length>0){let e={...n?{variant:n}:{},...r?{agent:r}:{}};c=Object.keys(e).length>0?e:void 0;break}c=ei(e);break}",
	"case`opencode`:{let e=a?.opencode;s=Zr(e?.variant);let t=o.variantOptions??[],n=s&&t.some(e=>e.value===s)?s:void 0,r=Zr(e?.agent);if(t.length>0){let e={...n?{variant:n}:{},...r?{agent:r}:{}};c=Object.keys(e).length>0?e:void 0;break}c=Xr(e);break}",
}

func synaraComposerHelpers() string {
	return "\nfunction __kiloSynaraClaudeEffort(runtime,raw){const levels=runtime.supportedReasoningEfforts??[];const value=typeof raw===\"string\"?raw.trim():\"\";return levels.some(row=>row.value===value)?value:levels.some(row=>row.value===runtime.defaultReasoningEffort)?runtime.defaultReasoningEffort:undefined;}\n" +
		"function __kiloSynaraClaudeDispatch(runtime,raw,normalized){if(runtime?.kiloSynaraInstanceId!==\"kilo_claude_proxy\")return normalized;const result={...normalized};delete result.effort;const effort=__kiloSynaraClaudeEffort(runtime,raw?.effort);if(effort)result.effort=effort;return Object.keys(result).length?result:undefined;}\n" +
		"function __kiloSynaraClaudePrompt(runtime,original,raw){return runtime?.kiloSynaraInstanceId===\"kilo_claude_proxy\"?__kiloSynaraClaudeEffort(runtime,raw)??null:original;}\n"
}

func synaraComposerOpenCodeHelpers() string {
	return "\nfunction __kiloSynaraOpenCodeVariant(runtime,raw){const levels=runtime.kiloSynaraVariantLevels??[];const value=typeof raw===\"string\"?raw.trim():\"\";return levels.includes(value)?value:levels.includes(runtime.kiloSynaraDefaultVariant)?runtime.kiloSynaraDefaultVariant:undefined;}\n" +
		"function __kiloSynaraOpenCodeDispatch(runtime,raw,normalized){if(runtime?.kiloSynaraInstanceId!==\"kilo_opencode_proxy\")return normalized;const result={...normalized};delete result.variant;const variant=__kiloSynaraOpenCodeVariant(runtime,raw?.variant);if(variant)result.variant=variant;return Object.keys(result).length?result:undefined;}\n" +
		"function __kiloSynaraOpenCodePrompt(runtime,original,raw){return runtime?.kiloSynaraInstanceId===\"kilo_opencode_proxy\"?__kiloSynaraOpenCodeVariant(runtime,raw)??null:original;}\n"
}

func patchSynaraComposerSource(source []byte, policy synaraPickerPolicy) ([]byte, error) {
	if err := validateSynaraPickerPolicy(policy); err != nil {
		return nil, err
	}
	if !synaraPrivateWebPolicyEnabled(policy) {
		return append([]byte(nil), source...), nil
	}
	if len(source) == 0 || len(source) > synaraPrivateWebFileLimit || strings.Contains(string(source), "__kiloSynaraClaude") {
		return nil, errors.New("Invalid private Synara composer source.")
	}
	text, start, variant := string(source), -1, -1
	for i, anchor := range synaraComposerFunctionAnchors {
		if strings.Contains(text, anchor) {
			if start >= 0 || !synaraLiveSourceAnchor(text, anchor) {
				return nil, errors.New("Ambiguous private Synara composer contract.")
			}
			start, variant = strings.Index(text, anchor)+strings.Index(anchor, "{"), i
		}
	}
	if start < 0 {
		return nil, errors.New("This Synara composer does not match the verified reasoning contract.")
	}
	body, ok := t3CodeContractBody(text, start)
	if !ok {
		return nil, errors.New("Invalid private Synara composer body.")
	}
	before, after := text[:start], text[start+len(body):]
	text = text[start : start+len(body)]
	var err error
	if policy.InstanceID != "" {
		text, err = replaceSynaraPickerContract(text, []string{synaraComposerClaudeAnchors[variant]}, func(s string) string {
			if variant == 0 {
				return strings.Replace(s, "c=bi(n,e)", "c=__kiloSynaraClaudeDispatch(r,e,bi(n,e))", 1)
			}
			return strings.Replace(s, "c=_i(n,e)", "c=__kiloSynaraClaudeDispatch(r,e,_i(n,e))", 1)
		})
	}
	if err != nil {
		return nil, err
	}
	if len(policy.OpenCodeModels) > 0 {
		text, err = replaceSynaraPickerContract(text, []string{synaraComposerOpenCodeAnchors[variant]}, func(s string) string {
			old := "c=Object.keys(e).length>0?e:void 0"
			s = strings.Replace(s, old, "c=__kiloSynaraOpenCodeDispatch(arguments[0].runtimeModel,arguments[0].modelOptions?.opencode,Object.keys(e).length>0?e:void 0)", 1)
			fallback := "ei(e)"
			if variant == 1 {
				fallback = "Xr(e)"
			}
			return strings.Replace(s, "c="+fallback, "c=__kiloSynaraOpenCodeDispatch(arguments[0].runtimeModel,e,"+fallback+")", 1)
		})
		if err != nil {
			return nil, err
		}
	}
	text, err = replaceSynaraPickerContract(text, []string{synaraComposerResultAnchor}, func(s string) string {
		value := "f"
		if policy.InstanceID != "" {
			value = "__kiloSynaraClaudePrompt(r," + value + ",s)"
		}
		if len(policy.OpenCodeModels) > 0 {
			value = "__kiloSynaraOpenCodePrompt(r," + value + ",s)"
		}
		return strings.Replace(s, "promptEffort:f", "promptEffort:"+value, 1)
	})
	if err != nil {
		return nil, err
	}
	return []byte(before + text + after + synaraComposerHelpers() + synaraComposerOpenCodeHelpers()), nil
}

func patchSynaraPickerSource(source []byte, policy synaraPickerPolicy) ([]byte, error) {
	if err := validateSynaraPickerPolicy(policy); err != nil {
		return nil, err
	}
	if !synaraPrivateWebPolicyEnabled(policy) {
		return append([]byte(nil), source...), nil
	}
	if len(source) == 0 || len(source) > synaraPrivateWebFileLimit || strings.Contains(string(source), "__kiloSynaraClaude") {
		return nil, errors.New("Invalid private Synara picker source.")
	}
	text := string(source)
	index := strings.Index(text, synaraPickerHookAnchor)
	if index < 0 || strings.Count(text, synaraPickerHookAnchor) != 1 || !strings.HasPrefix(t3CodeContractCode(text)[index:], "function Ce(e){let{selectedProvider:t,selectedProviderInstanceId:n,") {
		return nil, errors.New("This Synara picker does not expose the verified selected-account binding.")
	}
	start := index + len("function Ce(e)")
	body, ok := t3CodeContractBody(text, start)
	if !ok {
		return nil, errors.New("Invalid private Synara picker function body.")
	}
	before, after := text[:start], text[start+len(body):]
	text = text[start : start+len(body)]
	contracts := []struct {
		before []string
		after  func(string) string
	}{
		{[]string{synaraPickerSelectedInstanceAnchor}, func(s string) string { return s }},
		{synaraPickerInstanceAnchors, func(s string) string {
			return strings.Replace(s, ",n.provider===`omp`", ",r[n.instanceId]=__kiloSynaraClaudeOptions(n.instanceId,r[n.instanceId]),n.provider===`omp`", 1)
		}},
		{synaraPickerProviderAnchors, func(s string) string {
			s = strings.Replace(s, ",t},[F.data", ",t.claudeAgent=__kiloSynaraClaudeOptions(g(`claudeAgent`).instanceId,t.claudeAgent),t},[F.data", 1)
			if len(policy.OpenCodeModels) > 0 {
				s = strings.Replace(s, ",t},[F.data", ",t.opencode=__kiloSynaraClaudeOptions(g(`opencode`).instanceId,t.opencode),t},[F.data", 1)
			}
			return strings.Replace(s, ",Be]),We=", ",Be,t,n]),We=", 1)
		}},
		{[]string{synaraPickerRuntimeInstanceAnchor}, func(string) string {
			return "for(let t of f)e[t.instanceId]=__kiloSynaraClaudeRuntime(t.instanceId,p[t.instanceId]?.models??[]);return e}"
		}},
		{[]string{synaraPickerRuntimeProviderAnchor}, func(string) string {
			return "claudeAgent:__kiloSynaraClaudeRuntime(g(`claudeAgent`).instanceId,M.data?.models??[]),codex:N.data?.models??[]"
		}},
		{[]string{synaraPickerRuntimeDependencies}, func(s string) string {
			return strings.Replace(s, "]),Ge=", ",t,n]),Ge=", 1)
		}},
	}
	if len(policy.OpenCodeModels) > 0 {
		contracts = append(contracts, struct {
			before []string
			after  func(string) string
		}{[]string{"opencode:R.data?.models??[],pi:z.data?.models??[]"}, func(string) string {
			return "opencode:__kiloSynaraClaudeRuntime(g(`opencode`).instanceId,R.data?.models??[]),pi:z.data?.models??[]"
		}})
	}
	for _, contract := range contracts {
		var err error
		text, err = replaceSynaraPickerContract(text, contract.before, contract.after)
		if err != nil {
			return nil, err
		}
	}
	return []byte(before + text + after + synaraPickerHelpers(policy)), nil
}

func planSynaraPrivateWeb(options synaraPrivateWebOptions, binary, platform string) (synaraPrivateWeb, map[string][]byte, error) {
	var empty synaraPrivateWeb
	if err := validateSynaraPickerPolicy(options.Policy); err != nil {
		return empty, nil, err
	}
	if !synaraPrivateWebPolicyEnabled(options.Policy) {
		return empty, nil, nil
	}
	if (platform != "macos" && platform != "darwin") || !filepath.IsAbs(options.RootDir) || strings.ContainsAny(options.RootDir, "\x00\r\n") {
		return empty, nil, errors.New("The private Synara Claude picker currently requires a separate macOS workspace.")
	}
	if _, err := synaraPackageMetadata(binary, platform); err != nil {
		return empty, nil, err
	}
	archive, err := t3CodeOpenPackage(binary, platform)
	if err != nil {
		return empty, nil, err
	}
	defer archive.file.Close()
	client := "apps/server/dist/client"
	entry := archive.tree
	for _, part := range strings.Split(client, "/") {
		if entry.Link != "" || entry.Unpacked {
			return empty, nil, errors.New("Invalid Synara client asset tree.")
		}
		entry = entry.Files[part]
	}
	if entry.Files == nil {
		return empty, nil, errors.New("Synara does not contain the verified private client bundle.")
	}
	files, original := map[string][]byte{}, map[string]string{}
	var names []string
	var total int64
	nodes := 0
	var walk func(t3CodeASAREntry, string) error
	walk = func(node t3CodeASAREntry, prefix string) error {
		nodes++
		if nodes > synaraPrivateWebCountLimit*4 || strings.Count(prefix, "/") > 32 || len(prefix) > 4096 {
			return errors.New("Synara private client asset paths exceed their bounds.")
		}
		if node.Link != "" || node.Unpacked {
			return errors.New("Synara private client assets cannot contain links or unpacked entries.")
		}
		for name, child := range node.Files {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00\r\n") {
				return errors.New("Unsafe Synara private client asset path.")
			}
			relative := prefix + name
			if relative == synaraPrivateWebManifest {
				return errors.New("Synara client assets use a reserved private manifest path.")
			}
			if child.Files != nil {
				if err := walk(child, relative+"/"); err != nil {
					return err
				}
				continue
			}
			if child.Link != "" || child.Unpacked || child.Size < 0 || child.Size > synaraPrivateWebFileLimit {
				return errors.New("Invalid Synara private client asset entry.")
			}
			total += child.Size
			if total > synaraPrivateWebLimit || len(names) >= synaraPrivateWebCountLimit {
				return errors.New("Synara private client assets exceed their bounds.")
			}
			data := []byte{}
			if child.Size > 0 {
				var err error
				data, err = archive.read(client+"/"+relative, synaraPrivateWebFileLimit)
				if err != nil {
					return err
				}
			}
			files[relative], original[relative] = data, openDesignHash(data)
			names = append(names, relative)
		}
		return nil
	}
	if err := walk(entry, ""); err != nil {
		return empty, nil, err
	}
	if len(files["index.html"]) == 0 {
		return empty, nil, errors.New("Missing Synara private client index.")
	}
	picker, composer := "", ""
	for _, name := range names {
		if strings.HasPrefix(name, "assets/useProviderModelCatalog-") && strings.HasSuffix(name, ".js") {
			if picker != "" {
				return empty, nil, errors.New("Ambiguous Synara private model picker assets.")
			}
			picker = name
		}
		if strings.HasPrefix(name, "assets/SidechatDockPane-") && strings.HasSuffix(name, ".js") {
			if composer != "" {
				return empty, nil, errors.New("Ambiguous Synara private composer assets.")
			}
			composer = name
		}
	}
	if picker == "" || composer == "" {
		return empty, nil, errors.New("Missing Synara private model picker asset.")
	}
	files[picker], err = patchSynaraPickerSource(files[picker], options.Policy)
	if err != nil {
		return empty, nil, err
	}
	files[composer], err = patchSynaraComposerSource(files[composer], options.Policy)
	if err != nil {
		return empty, nil, err
	}
	encoded, _ := json.Marshal(options.Policy)
	header := openDesignHash(archive.header)
	identity, _ := json.Marshal([]any{synaraPrivateWebRevision, binary, header, openDesignHash(encoded), original, map[string]string{picker: openDesignHash(files[picker]), composer: openDesignHash(files[composer])}})
	saved := synaraPrivateWeb{Revision: synaraPrivateWebRevision, Root: filepath.Join(options.RootDir, "runtime-web", openDesignHash(identity)), SourceArchive: archive.file.Name(), SourceHeaderSHA256: header, SourceSHA256: openDesignHash(identity), PolicySHA256: openDesignHash(encoded), PickerEntry: picker, ComposerEntry: composer, Files: map[string]string{}}
	for name, data := range files {
		saved.Files[name] = openDesignHash(data)
	}
	manifest, _ := json.Marshal(saved)
	if len(manifest) > 1<<20 {
		return empty, nil, errors.New("Synara private client manifest exceeds its bound.")
	}
	return saved, files, nil
}

func synaraPrivateWebFilesReady(expected synaraPrivateWeb) bool {
	if len(expected.Files) == 0 || len(expected.Files) > synaraPrivateWebCountLimit {
		return false
	}
	base := filepath.Dir(filepath.Dir(expected.Root))
	info, rootErr := os.Lstat(base)
	if expected.Root == "" || rootErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !safeLaunchDir(expected.Root, base) {
		return false
	}
	data, err := readOpenDesignShimFile(filepath.Join(expected.Root, synaraPrivateWebManifest), 1<<20)
	var record synaraPrivateWeb
	if err != nil || json.Unmarshal(data, &record) != nil || !reflect.DeepEqual(record, expected) {
		return false
	}
	// Only directories implied by the manifest are legitimate. This also bounds
	// walking empty/deep directories added after preparation.
	directories := map[string]bool{".": true}
	for name := range expected.Files {
		if name == "" || len(name) > 4096 || strings.Count(name, "/") > 32 || strings.ContainsAny(name, "\\:\x00\r\n") || strings.HasPrefix(name, "/") || filepath.ToSlash(filepath.Clean(name)) != name {
			return false
		}
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			directories[parent] = true
		}
	}
	count, nodes := 0, 0
	err = filepath.WalkDir(expected.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		nodes++
		if nodes > synaraPrivateWebCountLimit*4 || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("Linked Synara private client asset.")
		}
		rel, err := filepath.Rel(expected.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if len(rel) > 4096 || strings.Count(rel, "/") > 32 {
			return errors.New("Synara private client asset paths exceed their bounds.")
		}
		if entry.IsDir() {
			if !directories[rel] {
				return errors.New("Unexpected Synara private client directory.")
			}
			return nil
		}
		if rel == synaraPrivateWebManifest {
			return nil
		}
		wanted, ok := expected.Files[rel]
		if !ok {
			return errors.New("Unexpected Synara private client asset.")
		}
		bytes, err := readOpenDesignShimFile(path, synaraPrivateWebFileLimit)
		if err != nil || openDesignHash(bytes) != wanted {
			return errors.New("Changed Synara private client asset.")
		}
		count++
		return nil
	})
	return err == nil && count == len(expected.Files)
}

func synaraPrivateWebReady(options synaraPrivateWebOptions, binary, platform string, saved synaraPrivateWeb) bool {
	expected, _, err := planSynaraPrivateWeb(options, binary, platform)
	return err == nil && expected.Root != "" && reflect.DeepEqual(saved, expected) && synaraPrivateWebFilesReady(expected)
}

func prepareSynaraPrivateWeb(options synaraPrivateWebOptions, binary, platform string) (synaraPrivateWeb, error) {
	saved, files, err := planSynaraPrivateWeb(options, binary, platform)
	if err != nil || saved.Root == "" {
		return saved, err
	}
	cache := filepath.Dir(saved.Root)
	if err = safeEditorDir(options.RootDir, cache); err != nil {
		return synaraPrivateWeb{}, err
	}
	if _, err = os.Lstat(saved.Root); err == nil {
		if synaraPrivateWebFilesReady(saved) {
			return saved, nil
		}
		return synaraPrivateWeb{}, errors.New("The private Synara client changed. Preserve it and prepare a fresh workspace.")
	} else if !os.IsNotExist(err) {
		return synaraPrivateWeb{}, err
	}
	stage, err := os.MkdirTemp(cache, ".preparing-")
	if err != nil {
		return synaraPrivateWeb{}, err
	}
	defer os.RemoveAll(stage)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return synaraPrivateWeb{}, err
		}
		if err = os.WriteFile(path, files[name], 0600); err != nil {
			return synaraPrivateWeb{}, err
		}
	}
	encoded, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return synaraPrivateWeb{}, err
	}
	if err = os.WriteFile(filepath.Join(stage, synaraPrivateWebManifest), append(encoded, '\n'), 0600); err != nil {
		return synaraPrivateWeb{}, err
	}
	if err = os.Rename(stage, saved.Root); err != nil {
		return synaraPrivateWeb{}, err
	}
	if !synaraPrivateWebFilesReady(saved) {
		return synaraPrivateWeb{}, errors.New("Cannot verify the prepared private Synara client.")
	}
	return saved, nil
}
