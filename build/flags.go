package build

import (
	"os"

	"github.com/spf13/pflag"
)

// ResolveFlag returns the effective value for a registered string flag, applying
// the precedence: CLI flag > environment variable > flag default.
//
// Call this inside the context function returned by a FlagRegistrar (i.e. after
// flag parsing has completed). envVar is the name of the environment variable to
// check when the flag was not explicitly passed on the command line.
func ResolveFlag(fs *pflag.FlagSet, flagName, envVar string) string {
	if fs.Changed(flagName) {
		v, _ := fs.GetString(flagName)
		return v
	}
	if v, ok := os.LookupEnv(envVar); ok {
		return v
	}
	v, _ := fs.GetString(flagName)
	return v
}
