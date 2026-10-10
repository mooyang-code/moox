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

// Export metadata comes from Admin; caller names never serve as KeyIDs.
func exportRemoteExternalCaller(ctx context.Context, transport setupssh.Client, root, caller string) (gatewayauth.Credentials, error) {
	if transport == nil || !filepath.IsAbs(root) {
		return gatewayauth.Credentials{}, fmt.Errorf("external caller export requires an SSH transport and absolute installation root")
	}
	switch caller {
	case "scf-collector", "factor-engine", "moox-skill":
	default:
		return gatewayauth.Credentials{}, fmt.Errorf("unknown external caller")
	}
	output := filepath.Join(root, "secrets", "external", caller)
	result, err := transport.Run(ctx, []string{"sh", "-lc", `exec "$1" keys export --caller "$2" --output-dir "$3" --db-path "$4" --encryption-key-file "$HOME/.config/moox/credentials/admin-encryption-key"`, "moox-external-export", filepath.Join(root, "bin/moox-admin-cli"), caller, output, filepath.Join(root, "data/admin.db")}, nil)
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
	raw, err := readRemoteSkillSecret(ctx, transport, metadata.KeyFile)
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
