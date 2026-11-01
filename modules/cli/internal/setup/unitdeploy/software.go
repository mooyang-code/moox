package unitdeploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type adminSoftware struct {
	Version      int                  `json:"version"`
	Architecture string               `json:"architecture"`
	Packages     []unitpackage.Result `json:"packages"`
	Helper       string               `json:"helper"`
	HelperSHA256 string               `json:"helper_sha256"`
}

func fileSHA(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<30 {
		return "", errors.New("deployment artifact must be a bounded regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func readAdminSoftware(ctx context.Context, root *os.Root, arch string) (adminSoftware, bool, error) {
	var stored adminSoftware
	if _, err := root.Lstat("admin-software.json"); os.IsNotExist(err) {
		return stored, false, nil
	} else if err != nil {
		return stored, false, err
	}
	raw, err := fsutil.ReadPrivate(root, "admin-software.json", 1<<20)
	if err != nil {
		return stored, true, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || decoder.Decode(new(any)) != io.EOF || stored.Version != 1 || stored.Architecture != arch || len(stored.Packages) != 2 {
		return stored, true, errors.New("persisted admin software does not match this target")
	}
	for i, profile := range []string{"host", "control"} {
		item := stored.Packages[i]
		if item.Archive == "" {
			if item.Manifest.Profile != profile {
				return stored, true, errors.New("persisted admin artifact failed independent verification")
			}
			continue
		}
		if !insideState(root.Name(), item.Archive) {
			return stored, true, errors.New("persisted admin artifact is outside private deployment state")
		}
		verified, err := unitpackage.Inspect(ctx, item.Archive)
		if err != nil || verified.SHA256 != item.SHA256 || verified.Manifest.Profile != profile || verified.Manifest.GOOS != "linux" || verified.Manifest.GOARCH != arch {
			return stored, true, errors.New("persisted admin artifact failed independent verification")
		}
		stored.Packages[i] = verified
	}
	digest := stored.HelperSHA256
	if stored.Helper != "" {
		if !insideState(root.Name(), stored.Helper) {
			return stored, true, errors.New("persisted helper is outside private deployment state")
		}
		if digest, err = fileSHA(stored.Helper); err != nil || digest != stored.HelperSHA256 {
			return stored, true, errors.New("persisted runtime helper changed")
		}
	}
	files := stored.Packages[0].Manifest.Files
	i := slices.IndexFunc(files, func(file unitpackage.File) bool { return file.Path == "bin/moox-runtime" })
	if i < 0 || files[i].SHA256 != "sha256:"+digest {
		return stored, true, errors.New("persisted runtime helper differs from the verified host package")
	}
	return stored, true, nil
}

func insideState(directory, filename string) bool {
	relative, err := filepath.Rel(directory, filename)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && filepath.IsAbs(filename) && filepath.Clean(filename) == filename
}

func prepareAdminSoftware(ctx context.Context, root *os.Root, options DeployOptions, arch string) ([]unitpackage.Result, string, error) {
	stored, exists, err := readAdminSoftware(ctx, root, arch)
	if err != nil || exists {
		return stored.Packages, stored.Helper, err
	}
	if options.downloads() {
		return recordBundleAdminSoftware(root, options, arch)
	}
	work, err := os.MkdirTemp(root.Name(), "software-")
	if err != nil {
		return nil, "", err
	}
	// Retain published artifacts for exact retries. An interrupted build has
	// no receipt and cannot be mistaken for a complete software set.
	bin := options.BinaryDirectory
	if bin == "" {
		bin = filepath.Join(work, "bin")
		if err := buildAdminLocally(ctx, options.RepositoryRoot, bin, arch, options.Log); err != nil {
			return nil, "", err
		}
	}
	stored = adminSoftware{Version: 1, Architecture: arch}
	for _, profile := range []string{"host", "control"} {
		result, err := unitpackage.Package(ctx, unitpackage.Options{RepositoryRoot: options.RepositoryRoot, BinaryDirectory: bin, Output: filepath.Join(work, profile+".tar.gz"), Profile: profile, GOOS: "linux", GOARCH: arch})
		if err != nil {
			return nil, "", err
		}
		stored.Packages = append(stored.Packages, result)
	}
	helperSource := filepath.Join(bin, "moox-runtime")
	helperSHA, err := fileSHA(helperSource)
	if err != nil {
		return nil, "", err
	}
	files := stored.Packages[0].Manifest.Files
	i := slices.IndexFunc(files, func(file unitpackage.File) bool { return file.Path == "bin/moox-runtime" })
	if i < 0 || files[i].SHA256 != "sha256:"+helperSHA {
		return nil, "", errors.New("runtime helper does not match the independently verified host package")
	}
	raw, err := os.ReadFile(helperSource)
	if err != nil {
		return nil, "", err
	}
	name := filepath.Base(work) + "/moox-runtime"
	if err := fsutil.WritePrivate(root, name, raw, false); err != nil {
		return nil, "", err
	}
	stored.Helper, stored.HelperSHA256 = filepath.Join(root.Name(), name), helperSHA
	if err := os.Chmod(stored.Helper, 0o700); err != nil {
		return nil, "", err
	}
	for _, name := range []string{stored.Helper, work} {
		file, err := os.Open(name)
		if err != nil {
			return nil, "", err
		}
		syncErr := file.Sync()
		file.Close()
		if syncErr != nil {
			return nil, "", syncErr
		}
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, "", err
	}
	if err := fsutil.WritePrivate(root, "admin-software.json", append(encoded, '\n'), false); err != nil {
		return nil, "", err
	}
	return stored.Packages, stored.Helper, nil
}

// recordBundleAdminSoftware 记下产物包里的 host、control 软件，重试时用同一份。
func recordBundleAdminSoftware(root *os.Root, options DeployOptions, arch string) ([]unitpackage.Result, string, error) {
	stored := adminSoftware{Version: 1, Architecture: arch}
	for _, profile := range []string{"host", "control"} {
		item, err := bundleSoftware(options.Bundle, profile, arch)
		if err != nil {
			return nil, "", err
		}
		stored.Packages = append(stored.Packages, item)
	}
	digest, err := packageHelperSHA(stored.Packages[0])
	if err != nil {
		return nil, "", err
	}
	stored.HelperSHA256 = digest
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, "", err
	}
	if err := fsutil.WritePrivate(root, "admin-software.json", append(encoded, '\n'), false); err != nil {
		return nil, "", err
	}
	return stored.Packages, "", nil
}

// No SSH capability is available to this builder. Every
// component in the host/control profiles is pure Go; frontend assets use Node.
func buildAdminLocally(ctx context.Context, repository, bin, arch string, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	run := func(directory, executable string, arguments ...string) error {
		command := exec.CommandContext(ctx, executable, arguments...)
		command.Dir, command.Stdout, command.Stderr = directory, log, log
		var environment []string
		for _, value := range os.Environ() {
			key, _, _ := strings.Cut(value, "=")
			if !slices.Contains([]string{"GOOS", "GOARCH", "CGO_ENABLED", "TARGET_GOOS", "TARGET_GOARCH", "BIN_DIR", "GOTOOLCHAIN"}, key) {
				environment = append(environment, value)
			}
		}
		command.Env = append(environment, "CGO_ENABLED=0", "GOTOOLCHAIN=local", "TARGET_GOOS=linux", "TARGET_GOARCH="+arch, "BIN_DIR="+bin)
		if err := command.Run(); err != nil {
			return errors.New("local admin build failed; see build diagnostics")
		}
		return nil
	}
	// 前端使用 pnpm 与 pnpm-lock.yaml（仓库没有 package-lock.json）。
	if err := run(filepath.Join(repository, "web"), "pnpm", "install", "--frozen-lockfile", "--config.confirmModulesPurge=false"); err != nil {
		return err
	}
	if err := run(filepath.Join(repository, "web"), "pnpm", "run", "build:prod"); err != nil {
		return err
	}
	if err := run(filepath.Join(repository, "web-host"), "make", "statik"); err != nil {
		return err
	}
	for _, target := range []string{"host-gateway", "hostagent", "admin", "eventbus", "console-proxy", "web-host", "monitor", "collector", "cloudfunction", "factor-mgr", "archive-mgr", "strategy"} {
		if err := run(repository, "bash", filepath.Join(repository, "scripts/build/build.sh"), target); err != nil {
			return err
		}
	}
	return nil
}
