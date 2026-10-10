package unitinstall

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type Deployment struct {
	Version       int                  `json:"version"`
	HostID        string               `json:"host_id"`
	RequestSHA256 string               `json:"request_sha256"`
	Directory     string               `json:"directory"`
	Previous      string               `json:"previous"`
	Phase         string               `json:"phase"`
	Components    []unitruntime.Status `json:"components"`
}

func deploymentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DeploymentHash binds the generated request to the actual issued inventories
// and override bytes. Changing a file at the same path is a different request.
func DeploymentHash(ctx context.Context, options PrepareOptions) (string, error) {
	material, err := unitbundle.Load(ctx, options.MaterialDirectory, options.MaterialOptions)
	if err != nil {
		return "", err
	}
	var clients hostbundle.ClientMetadata
	if options.EventBusDirectory != "" {
		if err := readGeneratedRequest(filepath.Join(options.EventBusDirectory, "clients.json"), &clients); err != nil {
			return "", err
		}
		if err := unitbundle.LoadEventBusClientsAt(ctx, clients, clients.Roles, options.EventBusDirectory); err != nil {
			return "", err
		}
	}
	overrides := map[string]string{}
	for name, filename := range options.Overrides {
		root, err := fsutil.OpenPhysicalRoot(filepath.Dir(filename), false)
		if err != nil {
			return "", err
		}
		raw, err := fsutil.ReadPrivate(root, filepath.Base(filename), 4<<20)
		root.Close()
		if err != nil {
			return "", err
		}
		overrides[name] = deploymentHash(raw)
	}
	return DeploymentRequestHash(options, material.Metadata(), clients, overrides)
}

func DeploymentRequestHash(options PrepareOptions, material hostbundle.Metadata, clients hostbundle.ClientMetadata, overrides map[string]string) (string, error) {
	raw, err := json.Marshal(struct {
		Options   PrepareOptions
		Material  hostbundle.Metadata
		EventBus  hostbundle.ClientMetadata
		Overrides map[string]string
	}{options, material, clients, overrides})
	if err != nil {
		return "", err
	}
	return deploymentHash(raw), nil
}

// Deploy owns a host or business unit's prepare/activate/retry sequence. A failed activation
// is recovered before preparing a fresh candidate; mutable retired data is
// never reused. A completed retry repairs missing processes and preserves pause.
func Deploy(ctx context.Context, options PrepareOptions) (result Deployment, returnErr error) {
	relative, rootErr := filepath.Rel(options.DeploymentRoot, options.UnitRoot)
	if !slices.Contains([]string{"host", "access", "egress-proxy", "trade", "storage"}, options.Profile) || options.MaterialOptions.AllowOperator || options.EventBusDirectory == "" || options.EventBusURL == "" || !validReleaseID(options.ReleaseID) || len(options.ReleaseID) > 40 || rootErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return result, errors.New("deploy requires a client-only host or business unit and a bounded release prefix")
	}
	digest, err := DeploymentHash(ctx, options)
	if err != nil {
		return result, err
	}
	err = unitruntime.WithMaintenance(ctx, options.DeploymentRoot, options.MaterialOptions.HostID, unitruntime.Options{}, func(guard *unitruntime.Maintenance) error {
		unit, err := fsutil.OpenPhysicalRoot(options.UnitRoot, false)
		if err != nil {
			return err
		}
		defer unit.Close()
		current, err := currentRelease(unit, options.UnitRoot)
		if err != nil {
			return err
		}
		var last Deployment
		exists := false
		if _, err := unit.Lstat("deployment.json"); err == nil {
			exists = true
			if err := readGeneratedRequest(filepath.Join(options.UnitRoot, "deployment.json"), &last); err != nil {
				return err
			}
			if last.Version != 1 || last.HostID != options.MaterialOptions.HostID || !slices.Contains([]string{"preparing", "prepared", "activating", "complete", "rolled-back"}, last.Phase) || filepath.Dir(last.Directory) != filepath.Join(options.UnitRoot, "releases") || !validReleaseID(filepath.Base(last.Directory)) || last.Previous != "" && (filepath.Dir(last.Previous) != filepath.Join(options.UnitRoot, "releases") || !validReleaseID(filepath.Base(last.Previous))) {
				return errors.New("deployment journal does not match this host unit")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		write := func(phase string) error {
			result.Phase = phase
			raw, err := json.Marshal(result)
			if err != nil {
				return err
			}
			return fsutil.WritePrivate(unit, "deployment.json", append(raw, '\n'), true)
		}
		fresh := func() error {
			result = Deployment{Version: 1, HostID: options.MaterialOptions.HostID, RequestSHA256: digest, Directory: filepath.Join(options.UnitRoot, "releases", options.ReleaseID+"-"+rand.Text()[:12]), Previous: current, Components: []unitruntime.Status{}}
			return write("preparing")
		}
		if exists && last.RequestSHA256 != digest && last.Phase != "complete" && last.Phase != "rolled-back" {
			return errors.New("unfinished deployment requires its original request and material")
		}
		if !exists || last.RequestSHA256 != digest {
			if err := fresh(); err != nil {
				return err
			}
		} else {
			result = last
			if result.Phase == "complete" && current != result.Directory {
				return errors.New("completed deployment no longer matches current")
			}
			if result.Phase != "complete" && current != result.Previous && current != result.Directory {
				return errors.New("interrupted deployment refuses an unrelated current release")
			}
		}
		return guard.UseLock(ctx, func(lock unitruntime.Options) error {
			activation, hasActivation, err := readActivation(unit)
			if err != nil {
				return err
			}
			if hasActivation && activation.Directory == result.Directory {
				if activation.Phase != "active" && activation.Phase != "rolled-back" {
					if _, err := Recover(ctx, options.UnitRoot, lock); err != nil {
						return err
					}
					activation, _, err = readActivation(unit)
					if err != nil {
						return err
					}
				}
				if activation.Phase == "rolled-back" {
					current, err = currentRelease(unit, options.UnitRoot)
					if err != nil {
						return err
					}
					if err := fresh(); err != nil {
						return err
					}
				} else if current == result.Directory {
					result.Phase = "complete"
				}
			}
			if result.Phase != "complete" {
				var prepared Prepared
				_, err := unit.Lstat("releases/" + filepath.Base(result.Directory))
				if os.IsNotExist(err) {
					candidate := options
					candidate.ReleaseID = filepath.Base(result.Directory)
					prepared, err = Prepare(ctx, candidate, lock)
				} else if err == nil {
					prepared, err = ReadPrepared(ctx, result.Directory)
				}
				if err != nil {
					return err
				}
				if prepared.HostID != result.HostID || prepared.UnitRoot != options.UnitRoot || prepared.PackageSHA256 != options.SHA256 || prepared.Identity.CA != options.MaterialOptions.ExpectedCA || prepared.Identity.Snapshot != options.MaterialOptions.ExpectedHash {
					return errors.New("deployment candidate does not match its original software")
				}
				if err := write("prepared"); err != nil {
					return err
				}
				if err := write("activating"); err != nil {
					return err
				}
				if _, err := Activate(ctx, ActivateOptions{Directory: result.Directory}, lock); err != nil {
					latest, ok, readErr := readActivation(unit)
					if readErr == nil && ok && latest.Directory == result.Directory && latest.Phase == "rolled-back" {
						return errors.Join(err, write("rolled-back"))
					}
					return err
				}
			}
			prepared, err := ReadInstalled(ctx, result.Directory)
			if err != nil {
				return err
			}
			if prepared.HostID != result.HostID || prepared.UnitRoot != options.UnitRoot {
				return errors.New("installed deployment belongs to another host")
			}
			// Activate also clears an interrupted installation marker on this
			// exact current release before repair can start any component.
			if _, err := Activate(ctx, ActivateOptions{Directory: result.Directory}, lock); err != nil {
				return err
			}
			status, err := unitruntime.Execute(ctx, planPath(prepared), "start", prepared.Components, lock)
			if err != nil {
				return err
			}
			if len(status.Components) != len(prepared.Components) {
				return errors.New("deployment readiness result is incomplete")
			}
			for _, component := range status.Components {
				if component.State != "paused" && (component.State != "running" || !component.Ready) {
					return errors.New("deployed host component is not ready")
				}
			}
			result.Components = status.Components
			return write("complete")
		})
	})
	return result, err
}
