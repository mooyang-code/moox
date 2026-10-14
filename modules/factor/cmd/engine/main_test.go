package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeEngineConfig 以仓库里的示例配置为底，把账本目录指到临时目录。
func writeEngineConfig(t *testing.T, ledgerDir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "engine.yaml"))
	require.NoError(t, err)
	text := strings.Replace(string(raw), "ledger_dir: ./data/gap", "ledger_dir: "+ledgerDir, 1)
	require.NotEqual(t, string(raw), text, "示例配置里应有 ledger_dir")
	text = strings.Replace(text, `key_id: ""`, "key_id: test-key", 1) // 示例配置里故意留空的必填项
	path := filepath.Join(t.TempDir(), "engine.yaml")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

func TestResetGapLedgerRemovesLedgerFilesOnlyWhenEngineIsStoppedAndConfirmed(t *testing.T) {
	ledgerDir := t.TempDir()
	ledgerFile := filepath.Join(ledgerDir, "factor-gap-202610.jsonl")
	require.NoError(t, os.WriteFile(ledgerFile, []byte("{}\n"), 0o640))
	config := writeEngineConfig(t, ledgerDir)
	stopped := "127.0.0.1:1" // 没有进程监听

	var out bytes.Buffer
	err := run([]string{"reset-gap-ledger", "--config", config, "--health-addr", stopped}, &out)
	require.ErrorContains(t, err, "--yes")
	require.FileExists(t, ledgerFile, "没有确认时不删")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	err = run([]string{"reset-gap-ledger", "--config", config, "--health-addr", listener.Addr().String(), "--yes"}, &out)
	require.ErrorContains(t, err, "仍在运行")
	require.FileExists(t, ledgerFile, "引擎还在运行时不删")

	require.NoError(t, run([]string{"reset-gap-ledger", "--config", config, "--health-addr", stopped, "--yes"}, &out))
	require.NoFileExists(t, ledgerFile)
	require.Contains(t, out.String(), `"files_removed":1`)
}
