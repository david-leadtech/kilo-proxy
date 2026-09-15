package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// OpenCode merges this private file with Open Design's higher-priority inline
// MCP configuration. Own only kilo_images; never replace the daemon's other
// servers, permissions, tool switches or OPENCODE_CONFIG_CONTENT.
func mergeOpenDesignOpenCodeImages(data []byte, images imageGenerationSettings, port int, key string) ([]byte, error) {
	if err := validateImageGenerationSettings(images); err != nil {
		return nil, err
	}
	if images.Enabled && (port < 1024 || port > 65535 || key == "" || strings.ContainsAny(key, "\x00\r\n")) {
		return nil, errors.New("Invalid local connection for Open Design image tools.")
	}
	tree, err := hujson.Parse(data)
	if err != nil || tree.Value.Kind() != '{' || !uniqueEditorJSON(tree) {
		return nil, errors.New("OpenCode settings must be valid JSON/JSONC with unique keys; nothing saved.")
	}
	servers := tree.Find("/mcp")
	if servers == nil && !images.Enabled {
		return data, nil
	}
	if servers != nil && servers.Value.Kind() != '{' {
		return nil, errors.New("OpenCode MCP settings must be an object; nothing saved.")
	}
	current := tree.Find("/mcp/kilo_images")
	if current != nil && !managedOpenDesignOpenCodeImages(*current) {
		if !images.Enabled {
			return data, nil
		}
		return nil, errors.New("MCP server kilo_images is already used by another OpenCode configuration. Rename that server before enabling Kilo images; no files were changed.")
	}
	var operations []map[string]any
	if images.Enabled {
		if servers == nil {
			operations = append(operations, map[string]any{"op": "add", "path": "/mcp", "value": map[string]any{}})
		}
		operations = append(operations, map[string]any{"op": "add", "path": "/mcp/kilo_images", "value": map[string]any{
			"type": "remote", "url": "http://127.0.0.1:" + strconv.Itoa(port) + "/mcp/images",
			"headers": map[string]string{"Authorization": "Bearer " + key},
			"oauth":   false, "enabled": true, "timeout": 360000,
		}})
	} else if current != nil {
		operations = append(operations, map[string]any{"op": "remove", "path": "/mcp/kilo_images"})
	} else {
		return data, nil
	}
	patch, _ := json.Marshal(operations)
	if err := tree.Patch(patch); err != nil {
		return nil, errors.New("Cannot update OpenCode image settings; nothing saved.")
	}
	result := tree.Pack()
	if len(result) > catalogLimit {
		return nil, errors.New("Settings exceed the size limit.")
	}
	return result, nil
}

func managedOpenDesignOpenCodeImages(value hujson.Value) bool {
	value = value.Clone()
	value.Standardize()
	var server map[string]any
	if json.Unmarshal(value.Pack(), &server) != nil || server["type"] != "remote" || server["oauth"] != false {
		return false
	}
	for name := range server {
		switch name {
		case "type", "url", "headers", "oauth", "enabled", "timeout":
		default:
			return false
		}
	}
	headers, ok := server["headers"].(map[string]any)
	if !ok || len(headers) != 1 {
		return false
	}
	auth, ok := headers["Authorization"].(string)
	if !ok || !strings.HasPrefix(auth, "Bearer ") || len(auth) <= len("Bearer ") {
		return false
	}
	address, ok := server["url"].(string)
	if !ok {
		return false
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/mcp/images" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port >= 1024 && port <= 65535
}
