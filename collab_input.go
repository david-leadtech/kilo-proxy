package main

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tailscale/hujson"
)

// Codex V1 accepts message OR items, but its schema exposes both as independent
// optional properties. Empty items still count as present in parse_collab_input.
// Only register these declared tools; unrelated tools and Codex V2 are untouched.
func (b *schemaBridge) registerCollabTools(tools []any, namespace string) {
	for _, entry := range tools {
		tool := object(entry)
		if stringValue(tool["type"]) == "namespace" {
			nested, _ := tool["tools"].([]any)
			b.registerCollabTools(nested, stringValue(tool["name"]))
			continue
		}
		if stringValue(tool["type"]) != "function" {
			continue
		}
		name := stringValue(tool["name"])
		toolNamespace := namespace
		if toolNamespace == "" && strings.HasPrefix(name, "multi_agent_v1.") {
			toolNamespace, name = "multi_agent_v1", strings.TrimPrefix(name, "multi_agent_v1.")
		}
		if toolNamespace != "multi_agent_v1" || (name != "spawn_agent" && name != "send_input") {
			continue
		}
		properties := object(object(tool["parameters"])["properties"])
		if stringValue(object(properties["message"])["type"]) != "string" || stringValue(object(properties["items"])["type"]) != "array" {
			continue
		}
		// Gateways can represent a declared namespace in either of these forms.
		b.collabTools[toolKey(toolNamespace, name)] = true
		b.collabTools[toolKey("", toolNamespace+"."+name)] = true
		tool["description"] = stringValue(tool["description"]) + "\nProvide exactly one of message or items. When using message, omit items entirely, including an empty array."
	}
}

func (b *schemaBridge) matchesCollab(item map[string]any) bool {
	return stringValue(item["type"]) == "function_call" && b.collabTools[toolKey(stringValue(item["namespace"]), stringValue(item["name"]))]
}

func adaptToolArguments(value any, envelope, collab bool) (string, error) {
	args, ok := value.(string)
	if !ok {
		return "", errors.New("invalid tool arguments")
	}
	if envelope {
		var err error
		args, err = unwrapArguments(args)
		if err != nil {
			return "", err
		}
	}
	if collab {
		args = normalizeCollabInput(args)
	}
	return args, nil
}

func normalizeCollabInput(args string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &fields) != nil || fields == nil {
		return args
	}
	var message string
	if json.Unmarshal(fields["message"], &message) != nil || strings.TrimSpace(message) == "" {
		return args
	}
	var items []json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil || items == nil || len(items) != 0 {
		return args
	}
	// A map would silently choose the last occurrence of duplicate keys. Leave
	// such calls for Codex to reject instead of choosing between instructions.
	tree, err := hujson.Parse([]byte(args))
	if err != nil || !uniqueEditorJSON(tree) {
		return args
	}
	// Do not choose between two substantive inputs, repair malformed arguments,
	// or modify message/model/reasoning/fork settings. RawMessage preserves numbers.
	delete(fields, "items")
	normalized, err := json.Marshal(fields)
	if err != nil {
		return args
	}
	return string(normalized)
}
