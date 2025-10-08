package znet

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/errors"

	"github.com/tokenize-x/crust/znet/infra"
	"github.com/tokenize-x/crust/znet/infra/apps"
	"github.com/tokenize-x/crust/znet/infra/apps/txd"
	"github.com/tokenize-x/crust/znet/infra/targets"
	"github.com/tokenize-x/tx-chain/v6/pkg/config/constant"
	"github.com/tokenize-x/tx-tools/pkg/libexec"
	"github.com/tokenize-x/tx-tools/pkg/must"
	"github.com/tokenize-x/tx-tools/pkg/parallel"
)

var exe = must.String(filepath.EvalSymlinks(must.String(os.Executable())))

// Activate starts preconfigured shell environment.
func Activate(ctx context.Context, configF *infra.ConfigFactory) error {
	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return errors.WithStack(err)
	}
	defer watcher.Close()

	// To be notified about directory being removed we must observe parent directory
	if err := watcher.Add(filepath.Dir(config.HomeDir)); err != nil {
		return errors.WithStack(err)
	}

	saveWrapper(config.WrapperDir, "start", "start")
	saveWrapper(config.WrapperDir, "stop", "stop")
	saveWrapper(config.WrapperDir, "remove", "remove")
	// `test` can't be used here because it is a reserved keyword in bash
	saveWrapper(config.WrapperDir, "tests", "test")
	saveWrapper(config.WrapperDir, "spec", "spec")
	saveWrapper(config.WrapperDir, "console", "console")
	saveLogsWrapper(config.WrapperDir, config.EnvName, "logs")

	shell, promptVar, err := shellConfig(config.EnvName)
	if err != nil {
		return err
	}
	shellCmd := osexec.Command(shell)
	shellCmd.Env = append(os.Environ(),
		"PATH="+config.WrapperDir+":"+os.Getenv("PATH"),
		"CRUST_ZNET_ENV="+configF.EnvName,
		"CRUST_ZNET_PROFILES="+strings.Join(configF.Profiles, ","),
		"CRUST_ZNET_TXD_VERSION="+configF.TXdVersion,
		"CRUST_ZNET_HOME="+configF.HomeDir,
		"CRUST_ZNET_ROOT_DIR="+configF.RootDir,
	)
	if promptVar != "" {
		shellCmd.Env = append(shellCmd.Env, promptVar)
	}
	shellCmd.Dir = config.HomeDir
	shellCmd.Stdin = os.Stdin

	return parallel.Run(ctx, func(ctx context.Context, spawn parallel.SpawnFn) error {
		spawn("session", parallel.Exit, func(ctx context.Context) error {
			err = libexec.Exec(ctx, shellCmd)
			if shellCmd.ProcessState != nil && shellCmd.ProcessState.ExitCode() != 0 {
				// shell returns non-exit code if command executed in the shell failed
				return nil
			}
			return err
		})
		spawn("fsnotify", parallel.Exit, func(ctx context.Context) error {
			defer func() {
				if shellCmd.Process != nil {
					// Shell exits only if SIGHUP is received. All the other signals are caught and passed to process
					// running inside the shell.
					_ = shellCmd.Process.Signal(syscall.SIGHUP)
				}
			}()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case event := <-watcher.Events:
					// Rename is here because on some OSes removing is done by moving file to trash
					if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && event.Name == config.HomeDir {
						return nil
					}
				case err := <-watcher.Errors:
					return errors.WithStack(err)
				}
			}
		})
		return nil
	})
}

// Start starts environment.
func Start(ctx context.Context, configF *infra.ConfigFactory) error {
	if err := apps.ValidateProfiles(configF.Profiles); err != nil {
		return err
	}

	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	if err := spec.Verify(); err != nil {
		return err
	}

	target := targets.NewDocker(config, spec)
	appF := apps.NewFactory(config, spec)

	appSet, _, err := apps.BuildAppSet(ctx, appF, config.Profiles, config.TXdVersion)
	if err != nil {
		return err
	}

	return target.Deploy(ctx, appSet)
}

// Stop stops environment.
func Stop(ctx context.Context, configF *infra.ConfigFactory) (retErr error) {
	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	defer func() {
		for _, app := range spec.Apps {
			app.SetInfo(infra.DeploymentInfo{Status: infra.AppStatusStopped})
		}
		if err := spec.Save(); retErr == nil {
			retErr = err
		}
	}()

	target := targets.NewDocker(config, spec)
	return target.Stop(ctx)
}

// Remove removes environment.
func Remove(ctx context.Context, configF *infra.ConfigFactory) (retErr error) {
	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	target := targets.NewDocker(config, spec)
	if err := target.Remove(ctx); err != nil {
		return err
	}

	// It may happen that some files are flushed to disk even after processes are terminated
	// so let's try to delete dir a few times
	var err error
	for range 3 {
		if err = os.RemoveAll(config.HomeDir); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.WithStack(err)
}

// Spec prints specification of running environment.
func Spec(spec *infra.Spec) error {
	fmt.Println(spec)
	return nil
}

// CoverageConvert converts and stores coverage from the first txd app we find.
func CoverageConvert(ctx context.Context, configF *infra.ConfigFactory) error {
	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	for appName, app := range spec.Apps {
		if app.Type() != txd.AppType {
			continue
		}

		if app.Info().Status != infra.AppStatusStopped {
			return errors.New("coverage convert can't be executed on top of running environment, stop it first")
		}

		dstCoverageDir := filepath.Dir(config.CoverageOutputFile)
		if err := os.MkdirAll(dstCoverageDir, os.ModePerm); err != nil {
			return errors.Wrapf(err, "failed to create coverage dir `%s`", dstCoverageDir)
		}

		txdAppHome := filepath.Join(config.AppDir, appName, string(constant.ChainIDDev))

		// We convert coverage from the first txd app we find since codecove results for all of them are identical
		// because of consensus.
		return txd.CoverageConvert(ctx, txdAppHome, config.CoverageOutputFile)
	}

	return errors.Errorf("no %s app found", txd.AppType)
}

// DumpAppDir dumps application directory to the specified destination.
func DumpAppDir(configF *infra.ConfigFactory, appType infra.AppType) (string, error) {
	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	for appName, app := range spec.Apps {
		if app.Type() != appType {
			continue
		}

		if app.Info().Status != infra.AppStatusStopped {
			return "", errors.New("directory dump can't be executed on top of running environment, stop it first")
		}

		txdAppHome := filepath.Join(config.AppDir, appName, string(constant.ChainIDDev))
		dumpDir := filepath.Join(config.DumpDir, appName)

		return dumpDir, copyDir(txdAppHome, dumpDir)
	}

	return "", errors.Errorf("no %s app found", appType)
}

// ExportGenesis exports the genesis file for one of the txd apps.
func ExportGenesis(ctx context.Context, configF *infra.ConfigFactory, modulesToExport []string) (string, error) {
	spec := infra.NewSpec(configF)
	config := NewConfig(configF, spec)

	for appName, app := range spec.Apps {
		if app.Type() != txd.AppType {
			continue
		}

		if app.Info().Status != infra.AppStatusStopped {
			return "", errors.New("directory dump can't be executed on top of running environment, stop it first")
		}

		return txd.ExportGenesis(ctx, appName, config, modulesToExport)
	}

	return "", errors.Errorf("no %s app found", txd.AppType)
}

func saveWrapper(dir, file, command string) {
	must.OK(os.WriteFile(dir+"/"+file, []byte(`#!/bin/bash
exec "`+exe+`" "`+command+`" "$@"
`), 0o700))
}

func saveLogsWrapper(dir, envName, file string) {
	must.OK(os.WriteFile(dir+"/"+file, []byte(`#!/bin/bash
if [ "$1" == "" ]; then
  echo "Provide the name of application"
  exit 1
fi
exec docker logs -f "`+envName+`-$1"
`), 0o700))
}

var supportedShells = map[string]func(envName string) string{
	"bash": func(envName string) string {
		return "PS1=(" + envName + `) [\u@\h \W]\$ `
	},
	"zsh": func(envName string) string {
		return "PROMPT=(" + envName + `) [%n@%m %1~]%# `
	},
}

func shellConfig(envName string) (string, string, error) {
	shell := os.Getenv("SHELL")
	if _, exists := supportedShells[filepath.Base(shell)]; !exists {
		var shells []string
		switch runtime.GOOS {
		case "darwin":
			shells = []string{"zsh", "bash"}
		default:
			shells = []string{"bash", "zsh"}
		}
		for _, s := range shells {
			if shell2, err := osexec.LookPath(s); err == nil {
				shell = shell2
				break
			}
		}
	}
	if shell == "" {
		return "", "", errors.New("custom shell not defined and supported shell not found")
	}

	var promptVar string
	if promptVarFn, exists := supportedShells[filepath.Base(shell)]; exists {
		promptVar = promptVarFn(envName)
	}
	return shell, promptVar, nil
}

// copyDir recursively copies a directory tree from src to dst.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Create destination directory
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			// Recursively copy subdirectory
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
