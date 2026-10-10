package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/caddyserver/caddy/v2"
	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
)

// InitializeState is an offline operation on a stopped, exclusively owned
// staging directory. The deployment coordinator durably consumes authorization
// before calling it and publishes the completed state before any service starts.
// Only the PKI app is provisioned: no listeners, ACME or OS trust installation.
func InitializeState(ctx context.Context, cfg config.Config, importExisting bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	ownership.Lock()
	defer ownership.Unlock()
	if ownership.current != nil {
		return "", errors.New("offline initialization requires the process Caddy engine to be stopped")
	}
	if importExisting {
		if _, err := CheckImportState(cfg); err != nil {
			return "", err
		}
		cert, err := verifyCA(cfg, true)
		if err != nil {
			return "", err
		}
		if cert == nil {
			return "", errors.New("import requires existing trusted CA state")
		}
	} else {
		// Public installations need no internal CA. Checking in public mode also
		// rejects remnants before a fresh internal installation is authorized.
		empty := cfg
		empty.TLS.Mode = "public"
		cert, err := verifyCA(empty, false)
		if err != nil {
			return "", err
		}
		if cert != nil {
			return "", errors.New("initialization never replaces existing CA state; use explicit import")
		}
		if cfg.TLS.Mode == "internal" {
			type obj = map[string]any
			raw, err := json.Marshal(obj{
				"admin":   obj{"disabled": true, "config": obj{"persist": false}},
				"storage": obj{"module": "file_system", "root": cfg.TLS.StorageRoot},
				"apps":    obj{"pki": obj{"certificate_authorities": obj{"local": obj{"install_trust": false}}}},
			})
			if err != nil {
				return "", err
			}
			loadErr := caddy.Load(raw, true)
			stopErr := caddy.Stop()
			if err := errors.Join(loadErr, stopErr); err != nil {
				return "", err
			}
			if _, err := publishCA(cfg, nil); err != nil {
				return "", err
			}
		}
	}
	// Caddy's generated key files and all newly created directories must reach
	// disk before the coordinator publishes its authorization receipt.
	for _, root := range []string{cfg.TLS.StorageRoot, filepath.Dir(cfg.TLS.CABaseline), cfg.TLS.CAPublishDir} {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.IsDir() && !entry.Type().IsRegular() {
				return errors.New("offline CA state must contain only physical files and directories")
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			return errors.Join(file.Sync(), file.Close())
		}); err != nil {
			return "", err
		}
	}
	return CheckState(cfg)
}
