package main

import (
	"errors"

	"github.com/pelletier/go-toml/v2"
)

// Open Design starts Codex with approvalPolicy=never and cannot display an MCP
// approval dialog. Enabling Kilo images authorizes this one managed image tool
// in its private profile. Never relax the global policy or other tools, and
// preserve explicit server/tool approval choices made in that private profile.
func mergeOpenDesignCodexImageApproval(data []byte, images imageGenerationSettings) ([]byte, error) {
	if !images.Enabled {
		return data, nil
	}
	var before, after map[string]any
	if toml.Unmarshal(data, &before) != nil || toml.Unmarshal(data, &after) != nil {
		return nil, errors.New("Invalid Codex TOML; Open Design image settings were not saved")
	}
	servers, ok := after["mcp_servers"].(map[string]any)
	if !ok || !managedCodexImages(servers[codexImagesServer]) {
		return nil, errors.New("Open Design image approvals require the managed local Kilo images server")
	}
	server := servers[codexImagesServer].(map[string]any)
	if _, exists := server["default_tools_approval_mode"]; exists {
		return data, nil
	}
	tools, err := configTable(server, "tools")
	if err != nil {
		return nil, err
	}
	tool, err := configTable(tools, "generate_image")
	if err != nil {
		return nil, err
	}
	if _, exists := tool["approval_mode"]; exists {
		return data, nil
	}
	tool["approval_mode"] = "approve"
	return editCodexTOML(data, before, after)
}
