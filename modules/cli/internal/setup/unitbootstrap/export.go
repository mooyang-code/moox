package unitbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type ExportRequest struct {
	Version         int                `json:"version"`
	DeploymentRoot  string             `json:"deployment_root"`
	ControlUnitRoot string             `json:"control_unit_root"`
	RuntimeSHA256   string             `json:"runtime_sha256"`
	ExportID        string             `json:"export_id"`
	Target          unitbundle.Options `json:"target"`
	Roles           []string           `json:"roles"`
}

type ExportResult struct {
	Version       int                       `json:"version"`
	RequestSHA256 string                    `json:"request_sha256"`
	Host          hostbundle.Metadata       `json:"host"`
	EventBus      hostbundle.ClientMetadata `json:"eventbus"`
}

// Optional checkpoints preserve absence while all other private-file errors
// remain redacted. ReadPrivate deliberately does not expose filesystem errors.
func readExportCheckpoint(root *os.Root, name string, max int64) ([]byte, error) {
	if _, err := root.Lstat(name); err != nil {
		return nil, err
	}
	return fsutil.ReadPrivate(root, name, max)
}

func ReadExportRequest(filename string) (ExportRequest, error) {
	var request ExportRequest
	root, err := fsutil.OpenPhysicalRoot(filepath.Dir(filename), false)
	if err != nil {
		return request, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, filepath.Base(filename), 4<<20)
	if err != nil {
		return request, err
	}
	return request, decode(raw, &request)
}

// ExportHost runs on the control host without stopping Admin. It pins its
// active release, runtime identity and CA under the host maintenance lock;
// the Admin CLI takes a consistent database transaction for each export.
func ExportHost(ctx context.Context, request ExportRequest) (ExportResult, error) {
	var result ExportResult
	if request.Version != 1 || request.Target.AllowOperator || request.Target.ExpectedHash != "" || !validAttempt(request.ExportID) || !validDigest(request.RuntimeSHA256) || len(request.Roles) == 0 || len(request.Roles) > 16 || filepath.Dir(request.ControlUnitRoot) != request.DeploymentRoot {
		return result, errors.New("host export requires normalized target identity and a client-only role selection")
	}
	for _, role := range request.Roles {
		if !validAttempt(role) || strings.Contains(role, ".") {
			return result, errors.New("host export role selection is invalid")
		}
	}
	if !slices.IsSorted(request.Roles) || len(slices.Compact(slices.Clone(request.Roles))) != len(request.Roles) {
		return result, errors.New("host export requires sorted unique client roles")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	result.Version, result.RequestSHA256 = 1, hash(raw)
	err = unitruntime.WithMaintenance(ctx, request.DeploymentRoot, request.Target.ControlHostID, unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
		deployment, err := fsutil.OpenPhysicalRoot(request.DeploymentRoot, false)
		if err != nil {
			return err
		}
		defer deployment.Close()
		identity, err := fsutil.OpenPhysicalRoot(filepath.Join(request.DeploymentRoot, "identity"), true)
		if err != nil {
			return err
		}
		defer identity.Close()
		bindingRaw, err := fsutil.ReadPrivate(identity, "runtime.json", 4096)
		if err != nil {
			return err
		}
		var binding runtimeBinding
		if decode(bindingRaw, &binding) != nil || binding.Version != 1 || binding.HostID != request.Target.ControlHostID || binding.SHA256 != request.RuntimeSHA256 {
			return errors.New("host export requires the original persistent runtime identity")
		}
		control, err := current(ctx, request.ControlUnitRoot, request.DeploymentRoot, request.Target.ControlHostID, "control")
		if err != nil {
			return err
		}
		if control.Directory == "" || !slices.Contains(control.Components, "admin") {
			return errors.New("host export requires an active managed Admin release")
		}
		if err := validateAdminState(control.Directory); err != nil {
			return err
		}
		if err := privateDirectory(deployment, "material-exports"); err != nil {
			return err
		}
		parent, err := fsutil.OpenPhysicalRoot(filepath.Join(request.DeploymentRoot, "material-exports"), true)
		if err != nil {
			return err
		}
		defer parent.Close()
		if err := parent.Mkdir(request.ExportID, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		parentDirectory, err := parent.Open(".")
		if err != nil {
			return err
		}
		if err := errors.Join(parentDirectory.Sync(), parentDirectory.Close()); err != nil {
			return err
		}
		directory := filepath.Join(parent.Name(), request.ExportID)
		export, err := fsutil.OpenPhysicalRoot(directory, true)
		if err != nil {
			return err
		}
		defer export.Close()
		requestRaw := append(slices.Clone(raw), '\n')
		if old, err := readExportCheckpoint(export, "request.json", 4<<20); err == nil {
			if !bytes.Equal(old, requestRaw) {
				return errors.New("host export retry requires its original request")
			}
		} else if os.IsNotExist(err) {
			if err := fsutil.WritePrivate(export, "request.json", requestRaw, false); err != nil {
				return err
			}
		} else {
			return err
		}
		validate := func() error {
			options := request.Target
			options.ExpectedHash = result.Host.ExpectedHash
			if filepath.Dir(result.Host.BundleDir) != directory || result.EventBus.OutputDir != filepath.Join(directory, "eventbus") || result.Version != 1 || result.RequestSHA256 != hash(raw) {
				return errors.New("saved host export belongs to another request")
			}
			if _, err := unitbundle.Load(ctx, result.Host.BundleDir, options); err != nil {
				return err
			}
			return unitbundle.LoadEventBusClients(ctx, result.EventBus, request.Roles)
		}
		if saved, err := readExportCheckpoint(export, "export.json", 256<<10); err == nil {
			if err := decode(saved, &result); err != nil {
				return err
			}
			return validate()
		} else if !os.IsNotExist(err) {
			return err
		}
		binary := filepath.Join(control.Directory, "bin/moox-admin-cli")
		database := filepath.Join(control.Directory, "admin/data/admin.db")
		master := filepath.Join(identity.Name(), "admin-master.key")
		return guard.UseLock(ctx, func(lock unitruntime.Options) error {
			args := []string{"host-bundle", "--host-id", request.Target.HostID, "--control-host-id", request.Target.ControlHostID, "--db-path", database, "--encryption-key-file", master, "--pki-dir", filepath.Join(identity.Name(), "pki"), "--output-dir", directory}
			checkpoint, checkpointErr := readExportCheckpoint(export, "host.json", 128<<10)
			if checkpointErr != nil && !os.IsNotExist(checkpointErr) {
				return checkpointErr
			}
			if os.IsNotExist(checkpointErr) {
				checkpoint, err = runOffline(ctx, exec.CommandContext(ctx, binary, args...), master, lock)
				if err != nil {
					return err
				}
			}
			decoder := json.NewDecoder(bytes.NewReader(checkpoint))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&result.Host) != nil || decoder.Decode(new(any)) != io.EOF {
				return errors.New("Admin returned invalid public host export metadata")
			}
			options := request.Target
			options.ExpectedHash = result.Host.ExpectedHash
			if _, err := unitbundle.Load(ctx, result.Host.BundleDir, options); err != nil {
				return err
			}
			if filepath.Dir(result.Host.BundleDir) != directory {
				return errors.New("host export escaped its private operation directory")
			}
			if os.IsNotExist(checkpointErr) {
				encoded, err := json.Marshal(result.Host)
				if err != nil {
					return err
				}
				if err := fsutil.WritePrivate(export, "host.json", append(encoded, '\n'), false); err != nil {
					return err
				}
			}
			args = []string{"eventbus-credentials", "export-clients", "--db-path", database, "--encryption-key-file", master, "--node-id", request.Target.ControlHostID, "--roles", strings.Join(request.Roles, ","), "--output-dir", filepath.Join(directory, "eventbus")}
			clientRaw, clientErr := readExportCheckpoint(export, "eventbus/clients.json", 128<<10)
			if clientErr != nil && !os.IsNotExist(clientErr) {
				return clientErr
			}
			if os.IsNotExist(clientErr) {
				clientRaw, err = runOffline(ctx, exec.CommandContext(ctx, binary, args...), master, lock)
				if err != nil {
					return err
				}
			}
			decoder = json.NewDecoder(bytes.NewReader(clientRaw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&result.EventBus) != nil || decoder.Decode(new(any)) != io.EOF || result.EventBus.Version != 1 || result.EventBus.Status != "ok" || result.EventBus.OutputDir != filepath.Join(directory, "eventbus") || !slices.Equal(result.EventBus.Roles, request.Roles) {
				return errors.New("Admin returned invalid public EventBus client metadata")
			}
			if err := validate(); err != nil {
				return err
			}
			if err := fsutil.SyncPrivateTree(ctx, export); err != nil {
				return err
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return err
			}
			return fsutil.WritePrivate(export, "export.json", append(encoded, '\n'), false)
		})
	})
	return result, err
}
