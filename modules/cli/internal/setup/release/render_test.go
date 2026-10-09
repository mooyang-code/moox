package release

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

var updateSnapshot = flag.Bool("update", false, "重写渲染快照")

const repositoryRoot = "../../../../.."

// exampleManifest 解析仓库中的 moox.toml.example，并填上示例里留空的密码和云凭据。
func exampleManifest(t *testing.T) setupconfig.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot, "moox.toml.example"))
	require.NoError(t, err)
	text := strings.ReplaceAll(string(raw), `password = ""`, `password = "test-password"`)
	text = strings.Replace(text, `secret_id = ""`, `secret_id = "test-secret-id"`, 1)
	text = strings.Replace(text, `secret_key = ""`, `secret_key = "test-secret-key"`, 1)
	manifest, err := setupconfig.Parse([]byte(text))
	require.NoError(t, err)
	return manifest
}

// snapshotSummary 是快照中每台主机的 plan.json：组件的运行规格（不含文件内容）与部署登记内容。
type snapshotSummary struct {
	Host         string             `json:"host"`
	Root         string             `json:"root"`
	Registration snapshotHostRecord `json:"registration"`
	Components   []Component        `json:"components"`
}

type snapshotHostRecord struct {
	HostID         string   `json:"host_id"`
	Address        string   `json:"address"`
	PrivateAddress string   `json:"private_address"`
	Region         string   `json:"region"`
	Components     []string `json:"components"`
}

// TestRenderSnapshot 用 moox.toml.example 渲染三台主机的配置、运行规格和部署登记内容，与 testdata/snapshot 比对。
// 改动渲染规则或模块配置后用 go test -run TestRenderSnapshot -update 重新生成，并检查差异。
func TestRenderSnapshot(t *testing.T) {
	manifest := exampleManifest(t)
	got := map[string][]byte{}
	for _, hostID := range manifest.HostIDs() {
		plan, err := Render(manifest, hostID, Options{RepositoryRoot: repositoryRoot, Version: "test"})
		require.NoError(t, err)
		for _, file := range plan.Files {
			got[hostID+"/"+file.Path] = file.Data
		}
		host, _ := manifest.Host(hostID)
		summary := snapshotSummary{
			Host: hostID, Root: plan.Root, Components: plan.Components,
			Registration: snapshotHostRecord{
				HostID: hostID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
				Components: append([]string{}, manifest.Components(hostID)...),
			},
		}
		encoded, err := json.MarshalIndent(summary, "", "  ")
		require.NoError(t, err)
		got[hostID+"/plan.json"] = append(encoded, '\n')
	}
	dir := filepath.Join("testdata", "snapshot")
	if *updateSnapshot {
		require.NoError(t, os.RemoveAll(dir))
		for path, data := range got {
			target := filepath.Join(dir, filepath.FromSlash(path))
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
			require.NoError(t, os.WriteFile(target, data, 0o644))
		}
	}
	want := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		want[filepath.ToSlash(rel)] = data
		return nil
	}), "快照不存在时用 go test -run TestRenderSnapshot -update 生成")
	require.Equal(t, sortedKeys(want), sortedKeys(got), "渲染出的文件与快照不一致")
	for path, data := range got {
		require.True(t, bytes.Equal(want[path], data), "文件 %s 与快照不一致", path)
	}
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestShellCommandKeepsReleaseReference(t *testing.T) {
	require.Equal(t,
		`"${RELEASE}"/'bin/moox-storage-cli' 'init' '--schema-path='"${RELEASE}"/'storage-primary/schema/metadata.sql' 'it'\''s'`,
		shellCommand("$RELEASE/bin/moox-storage-cli", "init", "--schema-path=$RELEASE/storage-primary/schema/metadata.sql", "it's"))
}

func TestResolveTLSMode(t *testing.T) {
	require.Equal(t, TLSModeInternal, ResolveTLSMode("", "10.0.0.5"))
	require.Equal(t, TLSModeInternal, ResolveTLSMode("auto", "localhost"))
	require.Equal(t, TLSModePublic, ResolveTLSMode("auto", "106.53.107.122"))
	require.Equal(t, TLSModeInternal, ResolveTLSMode("internal", "106.53.107.122"))
}

func TestRenderRejectsUnknownHost(t *testing.T) {
	_, err := Render(exampleManifest(t), "missing", Options{RepositoryRoot: repositoryRoot})
	require.ErrorContains(t, err, "没有主机 missing")
}
