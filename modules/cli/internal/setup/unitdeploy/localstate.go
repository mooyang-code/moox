package unitdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

// Binding 记录一次首次安装用的配置和版本。本机状态里的软件和身份只对这份配置、这个版本有效。
type Binding struct {
	ConfigSHA256 string `json:"config_sha256"`
	ConfigPath   string `json:"config_path"`
	Version      string `json:"version"`
}

const bindingFile = "install-binding.json"

// WriteBinding 在首次安装开始时记下配置和版本。
func WriteBinding(stateDir string, binding Binding) error {
	root, err := privateRoot(stateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return saveJSON(root, bindingFile, binding, true)
}

// MarkStep 记下一个已完成的安装步骤，next_steps 据此跳过它。
func MarkStep(stateDir, name string) error {
	root, err := privateRoot(stateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return saveJSON(root, name, map[string]string{"completed_at": time.Now().UTC().Format(time.RFC3339)}, true)
}

// MarkHostReady 记下主机服务和看护都装好了。
func MarkHostReady(stateDir, hostID string) error {
	root, err := privateRoot(filepath.Join(stateDir, "hosts", hostID))
	if err != nil {
		return err
	}
	defer root.Close()
	return saveJSON(root, unitReadyFile, map[string]string{"stage": "host-ready"}, true)
}

// 已完成步骤的记录文件。
const (
	StepInitStorage = "init-storage.json"
	StepInitReady   = "init-ready.json"
)

// ReadLocalState 只读取这台电脑上的部署状态：不建目录、不生成密钥，文件不存在就算没有。
// current 是这次命令用的配置和版本；本机没有记录或记录不一致时，这份状态不能用来继续安装。
func ReadLocalState(stateDir, operatorDir string, current Binding) deployscan.LocalState {
	state := deployscan.LocalState{WipePending: deployscan.ReadPending(operatorDir) != nil, StaleBinding: true}
	if raw, err := os.ReadFile(filepath.Join(stateDir, bindingFile)); err == nil {
		var recorded Binding
		if json.Unmarshal(raw, &recorded) == nil {
			state.BindingConfig = recorded.ConfigPath
			state.StaleBinding = recorded.ConfigSHA256 != current.ConfigSHA256 || recorded.Version != current.Version
		}
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "runtime-identity.json"))
	if err == nil {
		var identity runtimeIdentity
		if json.Unmarshal(raw, &identity) == nil && identity.HealthSecret != "" {
			state.RuntimeSHA256 = runtimeBinding(identity)
		}
	}
	state.AdminReady = fileExists(filepath.Join(stateDir, "admin-ready.json"))
	state.ControlReady = fileExists(filepath.Join(stateDir, "control-ready.json"))
	state.InitStorage = fileExists(filepath.Join(stateDir, StepInitStorage))
	state.InitReady = fileExists(filepath.Join(stateDir, StepInitReady))
	state.OperatorPin = fileExists(filepath.Join(operatorDir, "gateway-bootstrap.json"))
	for _, pattern := range []string{"units/*/*/operation.json", "units/*/*/releases/*/operation.json"} {
		if matches, _ := filepath.Glob(filepath.Join(stateDir, pattern)); len(matches) != 0 {
			state.UnitRecords = true
		}
	}
	state.Units = map[string]bool{}
	if matches, err := filepath.Glob(filepath.Join(stateDir, "units", "*", "*", unitReadyFile)); err == nil {
		for _, match := range matches {
			profile := filepath.Dir(match)
			state.Units[filepath.Base(filepath.Dir(profile))+"/"+filepath.Base(profile)] = true
		}
	}
	// 带 --release 部署的记录在版本目录下。
	if matches, err := filepath.Glob(filepath.Join(stateDir, "units", "*", "*", "releases", "*", unitReadyFile)); err == nil {
		for _, match := range matches {
			profile := filepath.Dir(filepath.Dir(filepath.Dir(match)))
			state.Units[filepath.Base(filepath.Dir(profile))+"/"+filepath.Base(profile)] = true
		}
	}
	// 主机单元的记录在 hosts/<主机>/ 下，不在 units 下。
	if matches, err := filepath.Glob(filepath.Join(stateDir, "hosts", "*", unitReadyFile)); err == nil {
		for _, match := range matches {
			state.Units[filepath.Base(filepath.Dir(match))+"/host"] = true
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
func TrialPackage(ctx context.Context, options DeployOptions, arch string) error {
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
