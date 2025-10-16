package build

import (
	"github.com/tokenize-x/tx-crust/build/golang"
	"github.com/tokenize-x/tx-crust/build/lint"
	txcrust "github.com/tokenize-x/tx-crust/build/tx-crust"
	"github.com/tokenize-x/tx-crust/build/types"
)

// Commands is a definition of commands available in build system.
var Commands = map[string]types.Command{
	"build/me":   {Fn: txcrust.BuildBuilder, Description: "Builds the builder"},
	"build/znet": {Fn: txcrust.BuildTXCrustZNet, Description: "Builds znet binary"},
	"lint":       {Fn: lint.Lint, Description: "Lints code and docs"},
	"test":       {Fn: golang.Test, Description: "Runs unit tests"},
	"tidy":       {Fn: golang.Tidy, Description: "Runs go mod tidy"},
	"remove":     {Fn: txcrust.Remove, Description: "Removes all artifacts created by tx-crust"},
}
