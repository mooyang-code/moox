package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

type eventBusClientsResult = hostbundle.ClientMetadata

func eventBusClientDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// exportEventBusClients takes one database snapshot and publishes only selected
// client roles plus the public CA. Broker ACLs, server keys and the CA private
// key never enter this export. It does not create or rotate credentials.
func exportEventBusClients(ctx context.Context, db *gorm.DB, nodeID, directory, requested string, stdout io.Writer) error {
	roles := strings.Split(requested, ",")
	files := eventBusRoleFiles()
	canonical := map[string]string{}
	for role, name := range files {
		canonical[strings.TrimSuffix(name, ".yaml")] = role
	}
	seen := map[string]bool{}
	for _, role := range roles {
		if canonical[role] == "" || seen[role] {
			return errors.New("export-clients requires unique canonical client roles")
		}
		seen[role] = true
	}
	slices.Sort(roles)
	var result eventBusClientsResult
	payloads := map[string][]byte{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		endpoint, err := eventBusNATSURL(tx, nodeID)
		if err != nil {
			return err
		}
		rows, err := listEventbus(dao.NewSecretDAO(tx), ctx)
		if err != nil {
			return err
		}
		if err := validateEventBusTLS(rows, endpoint); err != nil {
			return err
		}
		ca := []byte(rows["eventbus_tls_ca"].SecretValue)
		payloads["ca.pem"] = ca
		result = eventBusClientsResult{Version: 1, Status: "ok", OutputDir: directory, Roles: roles, CA: eventBusClientDigest(ca)}
		for _, name := range roles {
			role := canonical[name]
			row, ok := rows[eventBusKeys[role]]
			if !ok || len(row.SecretValue) < 32 || len(row.SecretValue) > 4096 || strings.ContainsFunc(row.SecretValue, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
				return errors.New("selected EventBus client credential is missing or invalid")
			}
			credential := struct {
				Version      int      `yaml:"version"`
				URLs         []string `yaml:"urls"`
				Username     string   `yaml:"username"`
				Token        string   `yaml:"token,omitempty"`
				HostToken    string   `yaml:"eventbus_token,omitempty"`
				MonitorToken string   `yaml:"monitor_eventbus_token,omitempty"`
				CAFile       string   `yaml:"ca_file"`
			}{Version: 1, URLs: []string{endpoint}, Username: role, CAFile: "ca.pem"}
			switch role {
			case "hostagent-publisher":
				credential.HostToken = row.SecretValue
			case "monitor-observability-consumer":
				credential.MonitorToken = row.SecretValue
			default:
				credential.Token = row.SecretValue
			}
			raw, err := yaml.Marshal(credential)
			if err != nil {
				return err
			}
			payloads[name+".yaml"] = raw
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
		return errors.New("client export requires a new absolute private directory")
	}
	parent, err := privatefiles.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return err
	}
	defer parent.Close()
	if _, err := parent.Lstat(filepath.Base(directory)); !os.IsNotExist(err) {
		return errors.New("client export refuses an existing destination")
	}
	stage, err := os.MkdirTemp(filepath.Dir(directory), ".eventbus-clients-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	names := make([]string, 0, len(payloads))
	for name := range payloads {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		raw := payloads[name]
		if err := atomicSecretFile(filepath.Join(stage, name), raw); err != nil {
			return err
		}
		result.Files = append(result.Files, hostbundle.File{Path: name, SHA256: eventBusClientDigest(raw), Size: int64(len(raw))})
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := atomicSecretFile(filepath.Join(stage, "clients.json"), append(raw, '\n')); err != nil {
		return err
	}
	directoryFile, err := os.Open(stage)
	if err != nil {
		return err
	}
	syncErr := directoryFile.Sync()
	directoryFile.Close()
	if syncErr != nil {
		return syncErr
	}
	if err := parent.Rename(filepath.Base(stage), filepath.Base(directory)); err != nil {
		return err
	}
	if err := privatefiles.SyncDirectory(parent); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
