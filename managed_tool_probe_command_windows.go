//go:build windows

package main

import (
	"context"
	"os/exec"
	"syscall"
)

func managedToolProbeCommand(ctx context.Context, binary string, environment []string) (*exec.Cmd, func(), error) {
	// Reuse the launcher's batch argument handling and trusted system cmd.exe.
	// Rebuild the command with a context so the version-check deadline still
	// applies, preserving its fixed CmdLine and isolated environment.
	template, cleanup, err := clientProcessCommand(clientLaunchPlan{Executable: binary, Args: []string{"--version"}}, environment)
	if err != nil {
		return nil, nil, err
	}
	command := exec.CommandContext(ctx, template.Path, template.Args[1:]...)
	command.Env, command.SysProcAttr = template.Env, template.SysProcAttr
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	return command, cleanup, nil
}
