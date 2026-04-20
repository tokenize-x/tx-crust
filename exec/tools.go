package exec

import (
	"context"
	"os/exec"
)

// Docker runs docker command.
func Docker(ctx context.Context, args ...string) *exec.Cmd {
	return toolCmd(ctx, "docker", args)
}

// Go runs go command.
func Go(ctx context.Context, args ...string) *exec.Cmd {
	return toolCmd(ctx, "go", args)
}
