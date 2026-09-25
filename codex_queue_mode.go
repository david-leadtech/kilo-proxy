package main

import (
	"errors"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

const (
	codexQueueModeQueue = "queue"
	codexQueueModeSteer = "steer"
)

func validCodexQueueMode(mode string) bool {
	return mode == codexQueueModeQueue || mode == codexQueueModeSteer
}

func mergeCodexQueueMode(data []byte, mode string) ([]byte, error) {
	if !validCodexQueueMode(mode) {
		return nil, errors.New("Queue mode must be queue or steer")
	}
	var before, after map[string]any
	if toml.Unmarshal(data, &before) != nil || toml.Unmarshal(data, &after) != nil {
		return nil, errors.New("Invalid Codex TOML; queue mode was not saved")
	}
	if after == nil {
		after = map[string]any{}
	}
	desktop, err := configTable(after, "desktop")
	if err != nil {
		return nil, err
	}
	desktop["followUpQueueMode"] = mode
	return editCodexTOML(data, before, after)
}

func codexQueueModeFromConfig(dir string) string {
	data, err := readCatalogFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		return codexQueueModeQueue
	}
	var config map[string]any
	if toml.Unmarshal(data, &config) != nil {
		return codexQueueModeQueue
	}
	value, ok := tomlAt(config, []string{"desktop", "followUpQueueMode"})
	mode, ok := value.(string)
	if !ok || !validCodexQueueMode(mode) {
		return codexQueueModeQueue
	}
	return mode
}

func codexQueueModeTransform(mode string) func([]byte) ([]byte, error) {
	return func(data []byte) ([]byte, error) { return mergeCodexQueueMode(data, mode) }
}
