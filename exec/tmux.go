package exec

import (
	"context"
	"io"
	"os/exec"
)

// TMux runs tmux command.
func TMux(ctx context.Context, args ...string) *exec.Cmd {
	return toolCmd(ctx, "tmux", args)
}

// TMuxNoOut runs tmux command with discarded outputs.
func TMuxNoOut(ctx context.Context, args ...string) *exec.Cmd {
	cmd := TMux(ctx, args...)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	return cmd
}
