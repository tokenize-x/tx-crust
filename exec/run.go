package exec

import (
	"context"
	"os/exec"

	"github.com/pkg/errors"
)

func toolCmd(ctx context.Context, tool string, args []string) *exec.Cmd {
	verifyTool(tool)
	return exec.CommandContext(ctx, tool, args...)
}

func verifyTool(tool string) {
	if _, err := exec.LookPath(tool); err != nil {
		panic(errors.Errorf("%s is not available, please install it", tool))
	}
}
