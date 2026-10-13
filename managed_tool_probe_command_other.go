//go:build !windows

package main

import (
	"context"
	"os/exec"
)

func managedToolProbeCommand(ctx context.Context, binary string, environment []string) (*exec.Cmd, func(), error) {
	command := exec.CommandContext(ctx, binary, "--version")
	command.Env = environment
	return command, func() {}, nil
}
