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
)

func buildLocalTargets(ctx context.Context, repository, bin, arch string, log io.Writer, targets []string) error {
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
	for _, target := range targets {
		command := exec.CommandContext(ctx, "bash", filepath.Join(repository, "scripts/build/build.sh"), target)
		command.Dir, command.Stdout, command.Stderr = repository, log, log
		command.Env = append(slices.Clone(environment), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "TARGET_GOOS=linux", "TARGET_GOARCH="+arch, "BIN_DIR="+bin)
		if err := command.Run(); err != nil {
			return errors.New("local host build failed; see build diagnostics")
		}
	}
	return nil
}
