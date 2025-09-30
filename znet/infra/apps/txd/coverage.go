package txd

import (
	"context"
	"path/filepath"

	"github.com/CoreumFoundation/coreum-tools/pkg/libexec"
	"github.com/CoreumFoundation/coreum-tools/pkg/logger"
	"go.uber.org/zap"

	"github.com/tokenize-x/crust/exec"
	"github.com/tokenize-x/crust/znet/infra/targets"
)

const covdataDirName = "covdatafiles"

// CoverageConvert converts and stores txd coverage data in text format.
func CoverageConvert(ctx context.Context, txdHomeDir, dstFilePath string) error {
	srcCovdataDir := filepath.Join(txdHomeDir, covdataDirName)

	cmd := exec.Go("tool", "covdata", "textfmt", "-i="+srcCovdataDir, "-o="+dstFilePath)

	if err := libexec.Exec(ctx, cmd); err != nil {
		return err
	}

	logger.Get(ctx).Info(
		"Successfully converted and stored coverage data in text format",
		zap.String("source covdata dir", srcCovdataDir),
		zap.String("destination text file", dstFilePath),
	)
	return nil
}

// GoCoverDir returns go coverage data directory inside container.
func (c TXd) GoCoverDir() string {
	return filepath.Join(targets.AppHomeDir, string(c.config.GenesisInitConfig.ChainID), covdataDirName)
}
