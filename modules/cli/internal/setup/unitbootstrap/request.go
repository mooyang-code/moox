// Package unitbootstrap coordinates offline Admin initialization and both
// control-host deployment units under one recoverable maintenance workflow.
package unitbootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type Unit struct {
	Archive     string                       `json:"archive"`
	SHA256      string                       `json:"sha256"`
	UnitRoot    string                       `json:"unit_root"`
	Environment map[string]map[string]string `json:"environment"`
	Overrides   map[string]string            `json:"overrides"`
}

type Request struct {
	Version        int                 `json:"version"`
	DeploymentRoot string              `json:"deployment_root"`
	Topology       hostbundle.Topology `json:"topology"`
	Host           Unit                `json:"host"`
	Control        Unit                `json:"control"`
}

func (Request) String() string     { return "MooXBootstrapRequest{private inputs omitted}" }
func (r Request) GoString() string { return r.String() }

func ReadRequest(filename string) (Request, error) {
	var request Request
	root, err := fsutil.OpenPhysicalRoot(filepath.Dir(filename), false)
	if err != nil {
		return request, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, filepath.Base(filename), 4<<20)
	if err != nil {
		return request, err
	}
	if err := decode(raw, &request); err != nil {
		return request, err
	}
	return request, nil
}

func decode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("bootstrap requires one generated JSON document with known fields")
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return errors.New("bootstrap metadata requires canonical JSON encoding")
	}
	return nil
}

type inputs struct {
	request    Request
	digest     string
	overrides  [2]map[string][]byte
	components []string
}

func hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validate(ctx context.Context, request Request) (inputs, error) {
	result := inputs{request: request}
	if request.Version != 1 || request.Topology.Version != 1 {
		return result, errors.New("bootstrap requires request and topology version 1")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return result, err
	}
	topology := servicecatalog.Topology{ControlHostID: request.Topology.ControlHostID}
	for _, host := range request.Topology.Hosts {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: host.HostID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Status: servicecatalog.Enabled})
		for _, id := range []string{"host-gateway", "host-agent"} {
			topology.Placements = append(topology.Placements, servicecatalog.Placement{HostID: host.HostID, ComponentID: id, Status: servicecatalog.Enabled})
		}
		for _, id := range host.Components {
			topology.Placements = append(topology.Placements, servicecatalog.Placement{HostID: host.HostID, ComponentID: id, Status: servicecatalog.Enabled})
		}
		if host.HostID == topology.ControlHostID {
			result.components = slices.Clone(host.Components)
		}
	}
	if catalog.ValidateTopology(topology) != nil {
		return result, errors.New("bootstrap topology does not satisfy the service catalog")
	}
	// Host Agent needs EventBus before it can become ready on an empty host.
	if !slices.Contains(result.components, "admin") || !slices.Contains(result.components, "eventbus") {
		return result, errors.New("control bootstrap requires Admin and EventBus on the control host")
	}
	if request.Host.UnitRoot == request.Control.UnitRoot {
		return result, errors.New("bootstrap requires separate host and control units")
	}
	for _, name := range []string{request.DeploymentRoot, request.Host.UnitRoot, request.Control.UnitRoot} {
		root, err := fsutil.OpenPhysicalRoot(name, false)
		if err != nil {
			return result, err
		}
		root.Close()
	}
	for _, name := range []string{request.Host.UnitRoot, request.Control.UnitRoot} {
		if filepath.Dir(name) != request.DeploymentRoot || filepath.Base(name) == "bootstrap" || filepath.Base(name) == "identity" || filepath.Base(name) == "run" {
			return result, errors.New("bootstrap unit roots must be separate direct children of deployment root")
		}
	}
	available, err := unitpackage.Components("control")
	if err != nil {
		return result, err
	}
	for _, id := range result.components {
		if !slices.ContainsFunc(available, func(c unitpackage.Component) bool { return c.ID == id }) {
			return result, errors.New("control host placements must belong to the control software unit")
		}
	}
	type binding struct {
		Request   Request
		Overrides [2]map[string]string
	}
	bound := binding{Request: request}
	for i, unit := range []Unit{request.Host, request.Control} {
		profile, ids := "host", []string{"host-gateway", "host-agent"}
		if i == 1 {
			profile, ids = "control", result.components
		}
		if len(unit.Environment) != len(ids) || len(unit.Overrides) > 128 {
			return result, errors.New("bootstrap requires explicit environment for every selected component")
		}
		for id := range unit.Environment {
			if !slices.Contains(ids, id) {
				return result, errors.New("bootstrap environment contains an unrelated component")
			}
		}
		software, err := unitpackage.Inspect(ctx, unit.Archive)
		if err != nil {
			return result, err
		}
		if software.SHA256 != unit.SHA256 || software.Manifest.Profile != profile || software.Manifest.GOOS != runtime.GOOS || software.Manifest.GOARCH != runtime.GOARCH {
			return result, errors.New("bootstrap software does not match its producer digest, profile or platform")
		}
		result.overrides[i], bound.Overrides[i] = map[string][]byte{}, map[string]string{}
		var total int
		for name, filename := range unit.Overrides {
			root, err := fsutil.OpenPhysicalRoot(filepath.Dir(filename), false)
			if err != nil {
				return result, err
			}
			raw, err := fsutil.ReadPrivate(root, filepath.Base(filename), 1<<20)
			root.Close()
			if err != nil {
				return result, err
			}
			total += len(raw)
			if total > 16<<20 {
				return result, errors.New("bootstrap overrides exceed private input limit")
			}
			result.overrides[i][name], bound.Overrides[i][name] = raw, hash(raw)
		}
	}
	raw, err := json.Marshal(bound)
	if err != nil {
		return result, err
	}
	result.digest = hash(raw)
	return result, nil
}
