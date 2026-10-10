package unitbundle

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// Projection is public evidence of the identities selected for one unit.
// It never contains an operator identity or private payload bytes.
type Projection struct {
	Version       int                     `json:"version"`
	HostID        string                  `json:"host_id"`
	ControlHostID string                  `json:"control_host_id"`
	CA            string                  `json:"ca_sha256"`
	Snapshot      string                  `json:"snapshot_sha256"`
	Credentials   []hostbundle.Credential `json:"credentials"`
	Files         []hostbundle.File       `json:"files"`
}

// Inject writes only this unit's identities into a private software release.
// Callers hold the host maintenance lock and own an unpublished release. The
// host template's config is the only replaceable file; identities never replace
// existing files. A failed release must be discarded by the caller.
func (m *Material) Inject(ctx context.Context, directory string, components []string) (Projection, error) {
	if m == nil || len(m.files) == 0 || len(components) == 0 {
		return Projection{}, errors.New("identity injection requires verified material and unit components")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return Projection{}, err
	}
	wanted := map[string]bool{}
	host := false
	for _, id := range components {
		component, ok := catalog.Component(id)
		if !ok || wanted[id] || id == "moox-cli" {
			return Projection{}, errors.New("identity projection requires distinct service components")
		}
		if component.Scope == servicecatalog.ScopeHost {
			host = true
		}
		wanted[id] = true
		if id == "console-proxy" {
			wanted["console"] = true
		}
	}
	if host && (!wanted["host-agent"] || !wanted["host-gateway"] || len(components) != 2) {
		return Projection{}, errors.New("host identities must be installed together in the host unit")
	}
	if host {
		delete(wanted, "host-gateway")
		wanted["host-gateway@"+m.metadata.HostID] = true
	}
	projection := Projection{Version: 1, HostID: m.metadata.HostID, ControlHostID: m.metadata.ControlHostID, CA: m.metadata.CA.SHA256, Snapshot: m.metadata.ExpectedHash}
	selected := map[string]bool{}
	for _, credential := range m.metadata.Credentials {
		if wanted[credential.Caller] {
			projection.Credentials = append(projection.Credentials, credential)
			selected[credential.KeyFile] = true
			delete(wanted, credential.Caller)
		}
	}
	if len(wanted) != 0 {
		return Projection{}, errors.New("identity projection lacks a placed component credential")
	}
	if host {
		for _, name := range []string{m.metadata.ConfigFile, "certs/moox-ca.crt", "certs/host-gateway/server.crt", "certs/host-gateway/server.key"} {
			selected[name] = true
		}
	}
	if slices.Contains(components, "access") {
		if m.metadata.VerificationFile == "" {
			return Projection{}, errors.New("Access projection lacks its verification registry")
		}
		for name := range m.files {
			if strings.HasPrefix(name, "secrets/access/") {
				selected[name] = true
			}
		}
	}
	for _, file := range m.metadata.Files {
		if selected[file.Path] {
			projection.Files = append(projection.Files, file)
		}
	}
	if len(projection.Files) != len(selected) {
		return Projection{}, errors.New("identity projection lacks a declared payload")
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return Projection{}, err
	}
	defer root.Close()
	for _, file := range projection.Files {
		if err := ctx.Err(); err != nil {
			return Projection{}, err
		}
		if err := fsutil.WritePrivate(root, file.Path, m.files[file.Path], host && file.Path == m.metadata.ConfigFile); err != nil {
			return Projection{}, err
		}
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return Projection{}, err
	}
	if err := fsutil.WritePrivate(root, "identity.json", append(raw, '\n'), false); err != nil {
		return Projection{}, err
	}
	return projection, nil
}
