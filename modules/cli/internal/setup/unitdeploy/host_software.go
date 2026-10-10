package unitdeploy

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

func prepareHostSoftware(ctx context.Context, root, state *os.Root, options CoreOptions, arch string, core coreSoftware) (unitpackage.Result, string, error) {
	if core.Architecture == arch {
		return core.Packages[0], core.Helper, nil
	}
	work, err := os.MkdirTemp(state.Name(), "software-")
	if err != nil {
		return unitpackage.Result{}, "", err
	}
	bin := options.BinaryDirectory
	if bin == "" {
		bin = filepath.Join(work, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			return unitpackage.Result{}, "", err
		}
		if err := buildHostLocally(ctx, options.RepositoryRoot, bin, arch, options.Log); err != nil {
			return unitpackage.Result{}, "", err
		}
	}
	software, err := unitpackage.Package(ctx, unitpackage.Options{RepositoryRoot: options.RepositoryRoot, BinaryDirectory: bin, Output: filepath.Join(work, "host.tar.gz"), Profile: "host", GOOS: "linux", GOARCH: arch})
	if err != nil {
		return software, "", err
	}
	raw, err := os.ReadFile(filepath.Join(bin, "moox-runtime"))
	if err != nil {
		return software, "", err
	}
	if !slices.ContainsFunc(software.Manifest.Files, func(file unitpackage.File) bool {
		return file.Path == "bin/moox-runtime" && file.SHA256 == "sha256:"+sha(raw)
	}) {
		return software, "", errors.New("host helper does not match its verified package")
	}
	name := filepath.Base(work) + "/moox-runtime"
	if err := fsutil.WritePrivate(state, name, raw, false); err != nil {
		return software, "", err
	}
	helper := filepath.Join(state.Name(), name)
	if !insideState(root.Name(), helper) {
		return software, "", errors.New("host helper escaped persistent native state")
	}
	if err := os.Chmod(helper, 0o700); err != nil {
		return software, "", err
	}
	file, err := os.Open(helper)
	if err != nil {
		return software, "", err
	}
	err = errors.Join(file.Sync(), file.Close())
	return software, helper, err
}

// Both host binaries and their helper are pure Go. This function executes
// only on the operator's machine and has no SSH/compile_host capability.
func buildHostLocally(ctx context.Context, repository, bin, arch string, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	var environment []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !slices.Contains([]string{"GOOS", "GOARCH", "CGO_ENABLED", "TARGET_GOOS", "TARGET_GOARCH", "BIN_DIR", "GOTOOLCHAIN"}, key) {
			environment = append(environment, value)
		}
	}
	for _, target := range []string{"host-gateway", "hostagent"} {
		command := exec.CommandContext(ctx, "bash", filepath.Join(repository, "scripts/build/build.sh"), target)
		command.Dir, command.Stdout, command.Stderr = repository, log, log
		command.Env = append(slices.Clone(environment), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "TARGET_GOOS=linux", "TARGET_GOARCH="+arch, "BIN_DIR="+bin)
		if err := command.Run(); err != nil {
			return errors.New("local host build failed; see build diagnostics")
		}
	}
	return nil
}
