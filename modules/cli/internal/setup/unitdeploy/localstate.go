package unitdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

// ReadLocalState 只读取这台电脑上的部署状态：不建目录、不生成密钥，文件不存在就算没有。
func ReadLocalState(stateDir, operatorDir string) deployscan.LocalState {
	state := deployscan.LocalState{WipePending: deployscan.ReadPending(operatorDir) != nil}
	raw, err := os.ReadFile(filepath.Join(stateDir, "runtime-identity.json"))
	if err == nil {
		var identity runtimeIdentity
		if json.Unmarshal(raw, &identity) == nil && identity.HealthSecret != "" {
			state.RuntimeSHA256 = runtimeBinding(identity)
		}
	}
	state.CoreReady = fileExists(filepath.Join(stateDir, "core-ready.json"))
	state.ControlReady = fileExists(filepath.Join(stateDir, "control-ready.json"))
	if pin, err := unitbundle.ReadOperatorPin(operatorDir); err == nil && pin.ControlHostID != "" {
		state.OperatorPin = true
	}
	for _, pattern := range []string{"units/*/*/operation.json", "units/*/*/releases/*/operation.json"} {
		if matches, _ := filepath.Glob(filepath.Join(stateDir, pattern)); len(matches) != 0 {
			state.UnitRecords = true
		}
	}
	return state
}

func fileExists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

// TrialPackage 按全部单元试打包到临时目录，确认产物包能用，走的是和正式部署相同的路径。
// 没有预编译二进制（源码目录）时跳过，部署时才在本机构建。
func TrialPackage(ctx context.Context, options CoreOptions, arch string) error {
	if options.BinaryDirectory == "" {
		return nil
	}
	work, err := os.MkdirTemp("", "moox-trial-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for _, profile := range append([]string{"storage"}, releaseProfiles...) {
		_, err := unitpackage.Package(ctx, unitpackage.Options{RepositoryRoot: options.RepositoryRoot, BinaryDirectory: options.BinaryDirectory, Output: filepath.Join(work, profile+".tar.gz"), Profile: profile, GOOS: "linux", GOARCH: arch})
		if err != nil {
			return fmt.Errorf("bundle_incomplete: 产物包打不出 %s 单元，没有清除任何主机: %w", profile, err)
		}
	}
	return nil
}
