package txd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CoreumFoundation/coreum-tools/pkg/libexec"
	"github.com/CoreumFoundation/coreum-tools/pkg/must"

	"github.com/tokenize-x/crust/znet/infra"
)

const (
	// ExportedGenesisFile is the name of the exported genesis file.
	ExportedGenesisFile = "exported_genesis.json"
)

// ExportGenesis exports the genesis file for the specified app.
func ExportGenesis(ctx context.Context, appName string, config infra.Config, modulesToExport []string) (string, error) {
	must.OK(os.MkdirAll(config.DumpDir, 0o700))

	exportedGenesisPath := filepath.Join(config.AppDir, appName, ExportedGenesisFile)

	txdWrapperPath := filepath.Join(config.WrapperDir, appName)
	fullArgs := []string{
		"export",
		"--modules-to-export", strings.Join(modulesToExport, ","),
		"--output-document", exportedGenesisPath,
	}

	return exportedGenesisPath, libexec.Exec(
		ctx,
		exec.Command(txdWrapperPath, fullArgs...),
	)
}
