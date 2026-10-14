package main

import (
	"encoding/json"
	"slices"
)

func synaraRuntimePickerOptions(options synaraProfileOptions) synaraPrivateWebOptions {
	result := synaraPrivateWebOptions{RootDir: options.RootDir}
	if options.Preferences == nil || options.Policy == nil {
		return result
	}
	policy := synaraOptionsPolicy(options)
	if options.Preferences.KiloClaude {
		picker := t3CodeClaudePickerLibrary(policy.ClaudeLibrary, policy.ClaudeRoutes)
		result.Policy.InstanceID = synaraClaudeProxyID
		for i, choice := range terminalLibraryChoices(policy.ClaudeLibrary, options.Catalog) {
			row := synaraPickerModel{Slug: picker.Models[i].ID, GatewayID: choice.Model.ID, Name: nativeCodexDisplayName(choice), ReasoningLevels: []string{}}
			if row.Slug != row.GatewayID {
				row.NativeSlug = row.Slug
				levels, initial := nativeReasoningFor(choice)
				for _, level := range levels {
					if validClaudeEffort(choice.Model.ID, level) {
						row.ReasoningLevels = append(row.ReasoningLevels, level)
					}
				}
				if slices.Contains(row.ReasoningLevels, initial) {
					row.DefaultEffort = initial
				}
			}
			result.Policy.Models = append(result.Policy.Models, row)
		}
	}
	if synaraOpenCodeKiloEnabled(*options.Preferences) {
		for _, choice := range terminalLibraryChoices(policy.OpenCodeLibrary, options.Catalog) {
			levels, initial := nativeReasoningFor(choice)
			row := synaraPickerOpenCodeModel{Slug: "kilo-local/" + choice.Model.ID, GatewayID: choice.Model.ID, Name: nativeCodexDisplayName(choice), VariantLevels: append([]string{}, levels...)}
			if slices.Contains(levels, initial) {
				row.DefaultVariant = initial
			}
			result.Policy.OpenCodeModels = append(result.Policy.OpenCodeModels, row)
		}
	}
	return result
}

func synaraRuntimeWebIdentity(webs []synaraPrivateWeb) any {
	if len(webs) == 0 || webs[0].Root == "" {
		return nil
	}
	return webs[0]
}

func synaraRuntimeStaticRootOverride(webRoot string) string {
	if webRoot == "" {
		return ""
	}
	root, _ := json.Marshal(webRoot)
	anchor, _ := json.Marshal(synaraDesktopStaticRootAnchor)
	replacement, _ := json.Marshal("function resolveServedStaticRoot() {\n\treturn {dir:" + string(root) + ",snapshotted:true};\n}")
	return "const staticAnchor=" + string(anchor) + ";if(source.split(staticAnchor).length!==2)throw Error('Synara desktop assets changed; prepare again.');source=source.replace(staticAnchor," + string(replacement) + ");\n"
}
