package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// exportRemoteExternalCaller 在控制主机上用原生 control 单元当前发布的 moox-admin-cli 只读导出外部调用方签名密钥。
// 元数据来自 Admin，调用方名称不作为 KeyID。密钥写到部署根的私有运行目录（不写入受完整性校验的发布目录），
// 读取后立即删除。
func exportRemoteExternalCaller(ctx context.Context, transport setupssh.Client, controlRoot, deployRoot, caller string) (gatewayauth.Credentials, error) {
	if transport == nil || !filepath.IsAbs(controlRoot) || !filepath.IsAbs(deployRoot) {
		return gatewayauth.Credentials{}, fmt.Errorf("external caller export requires an SSH transport and absolute installation root")
	}
	switch caller {
	case "scf-collector", "factor-engine", "moox-skill":
	default:
		return gatewayauth.Credentials{}, fmt.Errorf("unknown external caller")
	}
	output := filepath.Join(deployRoot, "run", "external-callers", caller)
	result, err := transport.Run(ctx, []string{"sh", "-lc", `exec "$1" keys export --caller "$2" --output-dir "$3" --db-path "$4" --encryption-key-file "$5"`, "moox-external-export",
		filepath.Join(controlRoot, "current/bin/moox-admin-cli"), caller, output,
		filepath.Join(controlRoot, "current/admin/data/admin.db"), filepath.Join(deployRoot, "identity/admin-master.key")}, nil)
	if err != nil || result.ExitCode != 0 || len(result.Stdout) > 16384 {
		return gatewayauth.Credentials{}, fmt.Errorf("Admin external caller export unavailable")
	}
	var metadata struct {
		Caller  string `json:"caller"`
		KeyID   string `json:"key_id"`
		KeyFile string `json:"key_file"`
	}
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return gatewayauth.Credentials{}, fmt.Errorf("invalid Admin external caller metadata")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return gatewayauth.Credentials{}, fmt.Errorf("invalid Admin external caller metadata")
	}
	if metadata.Caller != caller || metadata.KeyID == "" || len(metadata.KeyID) > 128 || strings.ContainsAny(metadata.KeyID, "/\\") || strings.ContainsFunc(metadata.KeyID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || metadata.KeyFile != filepath.Join(output, "caller-"+caller+".key") {
		return gatewayauth.Credentials{}, fmt.Errorf("invalid Admin external caller identity")
	}
	raw, err := readRemotePrivateFile(ctx, transport, metadata.KeyFile, 4096)
	// 无论读取成功与否都删除远端导出的密钥副本；删除失败不影响本次导出结果。
	_, _ = transport.Run(ctx, []string{"sh", "-c", `rm -f -- "$1"`, "moox-external-export-cleanup", metadata.KeyFile}, nil)
	if err != nil {
		return gatewayauth.Credentials{}, fmt.Errorf("external caller signing key unavailable")
	}
	defer clear(raw)
	key := strings.TrimSpace(string(raw))
	if len(key) < 32 || len(key) > 4096 || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return gatewayauth.Credentials{}, fmt.Errorf("invalid external caller signing key")
	}
	return gatewayauth.Credentials{Caller: caller, KeyID: metadata.KeyID, Secret: key}, nil
}
