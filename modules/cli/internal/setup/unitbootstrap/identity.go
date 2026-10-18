package unitbootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

type runtimeBinding struct {
	Version int    `json:"version"`
	HostID  string `json:"host_id"`
	SHA256  string `json:"sha256"`
}

// Bind the fleet's runtime credentials outside release snapshots before any
// process stops. Losing native state never authorizes replacing health/JWT
// identities on an already initialized deployment.
func bindRuntimeIdentity(deployment *os.Root, input inputs) error {
	admin := input.request.Control.Environment["admin"]
	keys := []string{"MOOX_HEALTH_AUTH_VERSION", "MOOX_HEALTH_AUTH_ACCESS_KEY", "MOOX_HEALTH_AUTH_SECRET_KEY", "MOOX_ADMIN_JWT_SECRET_KEY"}
	if admin[keys[0]] != "moox-health-v1" || admin[keys[1]] == "" || len(admin[keys[2]]) < 32 || len(admin[keys[3]]) < 32 {
		return errors.New("bootstrap requires valid persistent health and JWT identities")
	}
	for _, key := range keys {
		value := admin[key]
		if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return errors.New("bootstrap runtime identity must contain bounded canonical secrets")
		}
	}
	for _, unit := range []Unit{input.request.Host, input.request.Control} {
		for _, values := range unit.Environment {
			for _, key := range keys[:3] {
				if values[key] != admin[key] {
					return errors.New("bootstrap components must share the fleet health identity")
				}
			}
		}
	}
	values := map[string]string{}
	for _, key := range keys {
		values[key] = admin[key]
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	wanted := runtimeBinding{Version: 1, HostID: input.request.Topology.ControlHostID, SHA256: hash(raw)}
	if _, err := deployment.Lstat("identity/runtime.json"); err == nil {
		identity, err := fsutil.OpenPhysicalRoot(filepath.Join(deployment.Name(), "identity"), true)
		if err != nil {
			return err
		}
		defer identity.Close()
		raw, err := fsutil.ReadPrivate(identity, "runtime.json", 4096)
		if err != nil {
			return err
		}
		var stored runtimeBinding
		if decode(raw, &stored) != nil || stored != wanted {
			return errors.New("runtime identity differs from the persistent deployment binding; restore original native state")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, unit := range []Unit{input.request.Host, input.request.Control} {
		root, err := fsutil.OpenPhysicalRoot(unit.UnitRoot, false)
		if err != nil {
			return err
		}
		_, statErr := root.Lstat("current")
		root.Close()
		if !os.IsNotExist(statErr) {
			return errors.New("existing deployment lacks its persistent runtime identity binding")
		}
	}
	if err := privateDirectory(deployment, "identity"); err != nil {
		return err
	}
	raw, err = json.Marshal(wanted)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(deployment, "identity/runtime.json", append(raw, '\n'), false)
}
