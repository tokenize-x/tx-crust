package golang

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"text/template"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/tokenize-x/tx-crust/build/tools"
	"github.com/tokenize-x/tx-crust/build/types"
	"github.com/tokenize-x/tx-tools/pkg/libexec"
	"github.com/tokenize-x/tx-tools/pkg/logger"
	"github.com/tokenize-x/tx-tools/pkg/must"
)

type customLintersKey struct{}

var (
	//go:embed custom-gcl.tmpl
	customGclConfig string
	customGclTmpl   = template.Must(template.New(".custom-gcl.yml").Parse(customGclConfig))
	//go:embed "golangci.yaml"
	lintConfig                  string
	lintConfigTmpl              = template.Must(template.New("golangci.yaml").Parse(lintConfig))
	lintNewLinesSkipDirsRegexps = []string{
		`^\.`, `^vendor$`, `^target$`, `^tmp$`,
		`^.+\.db$`, // directories containing goleveldb
	}
	lintNewLinesSkipFilesRegexps = []string{`\.iml$`, `\.wasm$`, `\.png$`}
	customLintersCtxKey          = customLintersKey{}
)

// Lint runs linters and check that git status is clean.
func Lint(ctx context.Context, deps types.DepsFunc) error {
	if err := lint(ctx, deps); err != nil {
		return err
	}
	if err := lintNewLines(); err != nil {
		return err
	}
	return Tidy(ctx, deps)
}

func lint(ctx context.Context, deps types.DepsFunc) error {
	deps(EnsureGo, EnsureGolangCI)
	log := logger.Get(ctx)
	customLinters := getCustomLinters(ctx)
	config := lintConfigPath()

	linters := make([]string, len(customLinters))
	for i, customLinter := range customLinters {
		linters[i] = string(customLinter.GetName())
	}
	if err := storeLintConfig(linters); err != nil {
		return err
	}

	return onModule(repoPath, func(path string) error {
		goCodePresent, err := containsGoCode(path)
		if err != nil {
			return err
		}
		if !goCodePresent {
			log.Info("No code to lint", zap.String("path", path))
			return nil
		}

		log.Info("Running linter", zap.String("path", path))

		args := []string{"run", "--config", config}
		if len(customLinters) > 0 {
			if err = EnsureCustomGolangCI(ctx, customLinters); err != nil {
				return err
			}

			for _, customLinter := range customLinters {
				args = append(args, "--enable="+string(customLinter.GetName()))
			}
		}

		cmd := exec.Command(must.String(filepath.Abs("bin/golangci-lint")), args...)
		cmd.Dir = repoPath
		if err := libexec.Exec(ctx, cmd); err != nil {
			return errors.Wrapf(err, "linter errors found in module '%s'", path)
		}
		return nil
	})
}

// EnsureCustomGolangCI ensures that a customized go linter is available.
// To add custom linters to GolangCI, we need to add them to the .custom-gcl.yml file
// and use the "golangci-lint custom" to compile the customized linter and get the custom-gcl binary.
// This function ensures that the custom linter is available and links the custom-gcl binary to golangci-lint.
// The custom linter is added to the bin folder of each project that needs it and will be used instead of
// the original golangci-lint.
func EnsureCustomGolangCI(ctx context.Context, customLinters []tools.Tool) error {
	binDir := must.String(filepath.Abs("bin"))

	customLinterInstalled := false
	if _, customGclErr := os.Stat(filepath.Join(binDir, "custom-gcl")); customGclErr == nil {
		customLinterInstalled = true
	}

	stat, err := os.Stat(must.String(filepath.EvalSymlinks(filepath.Join(binDir, "golangci-lint"))))
	customLinterLinked := err == nil && stat.Name() == "custom-gcl"

	if !customLinterInstalled { //nolint:nestif
		golangCITool, err := tools.Get(tools.GolangCI)
		if err != nil {
			return err
		}

		linters := make([]map[string]interface{}, len(customLinters))
		for i, linter := range customLinters {
			paths := linter.GetBinaries(tools.TargetPlatformLocal)
			if linter.IsLocal() {
				linters[i] = map[string]interface{}{
					"Module": paths[0],
					"Local":  true,
					"Path":   paths[1],
				}
			} else {
				linters[i] = map[string]interface{}{
					"Module":  paths[0],
					"Local":   false,
					"Import":  paths[1],
					"Version": linter.GetVersion(),
				}
			}
		}

		buf := &bytes.Buffer{}
		if err = customGclTmpl.Execute(buf, map[string]interface{}{
			"GolangCIVersion": golangCITool.GetVersion(),
			"CustomLinters":   linters,
		}); err != nil {
			return err
		}

		err = errors.WithStack(os.WriteFile("bin/.custom-gcl.yml", buf.Bytes(), 0o600))
		if err != nil {
			return err
		}

		cmd := exec.Command(tools.Path("bin/golangci-lint", tools.TargetPlatformLocal), "custom")
		cmd.Dir = binDir
		if err = libexec.Exec(ctx, cmd); err != nil {
			return errors.Wrap(err, "could not make custom linter")
		}
	}

	if !customLinterLinked {
		_ = os.Remove(filepath.Join(binDir, "golangci-lint"))
		if err = os.Symlink(filepath.Join(binDir, "custom-gcl"), filepath.Join(binDir, "golangci-lint")); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

// getCustomLinters gets custom golangci-lint linters from context.
func getCustomLinters(ctx context.Context) []tools.Tool {
	customLinters := ctx.Value(customLintersCtxKey)
	if customLinters == nil {
		return []tools.Tool{}
	}

	return customLinters.([]tools.Tool)
}

// WithCustomLinters adds custom golangci-lint linters to context.
func WithCustomLinters(ctx context.Context, customLinters ...tools.Name) (context.Context, error) {
	currentCustomLinters := getCustomLinters(ctx)
	for _, customLinterName := range customLinters {
		isDuplicate := false
		for _, currentCustomLinter := range currentCustomLinters {
			if currentCustomLinter.GetName() == customLinterName {
				isDuplicate = true
			}
		}
		if !isDuplicate {
			customLinter, err := tools.Get(customLinterName)
			if err != nil {
				return nil, err
			}
			if _, ok := customLinter.(tools.CustomLinter); !ok {
				return nil, errors.Errorf("tool '%s' is not a custom linter", customLinterName)
			}
			currentCustomLinters = append(currentCustomLinters, customLinter)
		}
	}
	return context.WithValue(ctx, customLintersCtxKey, currentCustomLinters), nil
}

func lintNewLines() error {
	skipDirsRegexps, err := parseRegexps(lintNewLinesSkipDirsRegexps)
	if err != nil {
		return err
	}

	skipFilesRegexps, err := parseRegexps(lintNewLinesSkipFilesRegexps)
	if err != nil {
		return err
	}

	return filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			for _, reg := range skipDirsRegexps {
				if reg.MatchString(d.Name()) {
					return filepath.SkipDir
				}
			}

			return nil
		}
		info, err := d.Info()
		if err != nil {
			return errors.WithStack(err)
		}
		if info.Mode()&0o111 != 0 {
			// skip executable files
			return nil
		}

		for _, reg := range skipFilesRegexps {
			if reg.MatchString(info.Name()) {
				return nil
			}
		}

		f, err := os.Open(path)
		if err != nil {
			return errors.WithStack(err)
		}
		defer f.Close()

		if _, err := f.Seek(-2, io.SeekEnd); err != nil {
			return errors.WithStack(err)
		}

		buf := make([]byte, 2)
		if _, err := f.Read(buf); err != nil {
			return errors.WithStack(err)
		}
		if buf[1] != '\n' {
			return errors.Errorf("no empty line at the end of file '%s'", path)
		}
		if buf[0] == '\n' {
			return errors.Errorf("many empty lines at the end of file '%s'", path)
		}
		return nil
	})
}

func parseRegexps(strRegexps []string) ([]*regexp.Regexp, error) {
	compiledRegexps := make([]*regexp.Regexp, 0, len(strRegexps))
	for _, strReg := range strRegexps {
		r, err := regexp.Compile(strReg)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid regexp '%s'", strReg)
		}
		compiledRegexps = append(compiledRegexps, r)
	}

	return compiledRegexps, nil
}

func lintConfigPath() string {
	return filepath.Join("bin", "golangci.yaml")
}

func storeLintConfig(linters []string) error {
	buf := &bytes.Buffer{}
	if err := lintConfigTmpl.Execute(buf, map[string]interface{}{
		"CustomLinters": linters,
	}); err != nil {
		return err
	}
	return errors.WithStack(os.WriteFile(lintConfigPath(), buf.Bytes(), 0o600))
}
