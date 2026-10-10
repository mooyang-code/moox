package unitbootstrap

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type Result struct {
	HostID           string `json:"host_id"`
	RequestSHA256    string `json:"request_sha256"`
	Phase            string `json:"phase"`
	HostDirectory    string `json:"host_directory"`
	ControlDirectory string `json:"control_directory"`
	ExpectedHash     string `json:"expected_hash"`
	CAFingerprint    string `json:"ca_fingerprint"`
	BundleDirectory  string `json:"bundle_directory"`
}

func public(j journal) Result {
	return Result{HostID: j.HostID, RequestSHA256: j.RequestSHA256, Phase: j.Phase, HostDirectory: j.Host.Candidate, ControlDirectory: j.Control.Candidate, ExpectedHash: j.ExpectedHash, CAFingerprint: j.CAFingerprint, BundleDirectory: j.BundleDirectory}
}

// Run executes on the target Linux host. The native CLI sends only normalized
// input and prebuilt software; the operator manifest never reaches this helper.
// Failed runs restore both original units before releasing the durable barrier.
// SIGKILL leaves the journal/barrier for the same request to recover next time.
func Run(ctx context.Context, request Request) (result Result, returnErr error) {
	if runtime.GOOS != "linux" {
		return result, errors.New("bootstrap runtime requires Linux; build pure Go artifacts on the operator machine")
	}
	input, err := validate(ctx, request)
	if err != nil {
		return result, err
	}
	err = unitruntime.WithMaintenance(ctx, request.DeploymentRoot, request.Topology.ControlHostID, unitruntime.Options{BootstrapID: input.digest}, func(guard *unitruntime.Maintenance) error {
		deployment, err := fsutil.OpenPhysicalRoot(request.DeploymentRoot, false)
		if err != nil {
			return err
		}
		defer deployment.Close()
		if err := bindRuntimeIdentity(deployment, input); err != nil {
			return err
		}
		if err := privateDirectory(deployment, "bootstrap"); err != nil {
			return err
		}
		root, err := fsutil.OpenPhysicalRoot(filepath.Join(request.DeploymentRoot, "bootstrap"), true)
		if err != nil {
			return err
		}
		defer root.Close()
		j, exists, err := readJournal(root, input)
		if err != nil {
			return err
		}
		if exists && (j.Phase == "complete" || j.Phase == "repairing") && j.RequestSHA256 == input.digest {
			for i, u := range []unitState{j.Host, j.Control} {
				profile := "host"
				if i == 1 {
					profile = "control"
				}
				active, err := current(ctx, u.Root, j.DeploymentRoot, j.HostID, profile)
				if err != nil {
					return err
				}
				if active.Directory != u.Candidate {
					return errors.New("completed bootstrap no longer matches current units")
				}
			}
			if err := guard.BeginBootstrap(ctx, input.digest); err != nil {
				return err
			}
			if err := guard.UseLock(ctx, func(lock unitruntime.Options) error { return repairComplete(ctx, root, &j, lock) }); err != nil {
				return err
			}
			if err := guard.EndBootstrap(); err != nil {
				return err
			}
			result = public(j)
			return nil
		}
		if exists && j.Phase != "complete" && j.Phase != "rolled-back" {
			if j.RequestSHA256 != input.digest {
				return errors.New("an interrupted bootstrap must be recovered with its original request")
			}
			if err := guard.BeginBootstrap(ctx, input.digest); err != nil {
				return err
			}
			if err := guard.UseLock(ctx, func(lock unitruntime.Options) error { return restoreWorkflow(ctx, root, &j, lock) }); err != nil {
				return err
			}
			if err := guard.EndBootstrap(); err != nil {
				return err
			}
		}
		// A completed rollback may still have its root marker after a power loss.
		if exists && j.Phase == "rolled-back" && j.RequestSHA256 == input.digest {
			if err := guard.BeginBootstrap(ctx, input.digest); err != nil {
				return err
			}
			if err := guard.EndBootstrap(); err != nil {
				return err
			}
		}
		j = journal{Version: 1, HostID: request.Topology.ControlHostID, DeploymentRoot: request.DeploymentRoot, RequestSHA256: input.digest, Attempt: "bootstrap-" + rand.Text()}
		j.Host, err = capture(ctx, guard, request.Host.UnitRoot, request.DeploymentRoot, j.HostID, "host")
		if err != nil {
			return err
		}
		j.Control, err = capture(ctx, guard, request.Control.UnitRoot, request.DeploymentRoot, j.HostID, "control")
		if err != nil {
			return err
		}
		if (j.Host.Previous == "") != (j.Control.Previous == "") {
			return errors.New("bootstrap requires both existing units or an empty control host")
		}
		if j.Control.Previous != "" {
			previous, err := unitinstall.ReadInstalled(ctx, j.Control.Previous)
			if err != nil {
				return err
			}
			control := request.Topology.Hosts[slices.IndexFunc(request.Topology.Hosts, func(h hostbundle.Host) bool { return h.HostID == j.HostID })]
			for _, id := range previous.Components {
				if slices.Contains(control.Components, id) && !slices.Contains(input.components, id) {
					return errors.New("bootstrap cannot narrow an installed control placement to a core subset")
				}
			}
		}
		if err := writeJournal(root, &j, "captured"); err != nil {
			return err
		}
		if err := guard.BeginBootstrap(ctx, input.digest); err != nil {
			return err
		}
		err = guard.UseLock(ctx, func(lock unitruntime.Options) error { return execute(ctx, root, input, &j, lock) })
		result = public(j)
		if err != nil {
			// Cancellation cannot abandon a partially switched host. Persisted
			// component stop/start budgets still bound all cleanup operations.
			recovery := guard.UseLock(context.WithoutCancel(ctx), func(lock unitruntime.Options) error {
				return restoreWorkflow(context.WithoutCancel(ctx), root, &j, lock)
			})
			if recovery == nil {
				recovery = guard.EndBootstrap()
			}
			result = public(j)
			return errors.Join(err, recovery)
		}
		if err := guard.EndBootstrap(); err != nil {
			return err
		}
		result = public(j)
		return nil
	})
	return result, err
}

func privateDirectory(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() || !fsutil.Owned(info) || info.Mode().Perm() != 0o700 {
		return errors.New("bootstrap work directories must be owned physical 0700 directories")
	}
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func execute(ctx context.Context, root *os.Root, input inputs, j *journal, lock unitruntime.Options) error {
	if err := root.Mkdir(j.Attempt, 0o700); err != nil {
		return err
	}
	attempt, err := fsutil.OpenPhysicalRoot(filepath.Join(root.Name(), j.Attempt), true)
	if err != nil {
		return err
	}
	defer attempt.Close()
	request := input.request
	extracted, err := unitpackage.Extract(ctx, unitpackage.ExtractOptions{Archive: request.Control.Archive, ExpectedSHA256: request.Control.SHA256, Profile: "control", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Destination: filepath.Join(attempt.Name(), "software")})
	if err != nil {
		return err
	}
	if err := writeJournal(root, j, "stopping"); err != nil {
		return err
	}
	for _, u := range []unitState{j.Control, j.Host} {
		if u.Previous != "" {
			if _, err := lifecycle(ctx, u.Previous, "stop", nil, lock); err != nil {
				return err
			}
		}
	}
	checkpoint := filepath.Join(attempt.Name(), "offline")
	if j.Control.Previous == "" {
		if err := privateDirectory(root, "initial"); err != nil {
			return err
		}
		checkpoint = filepath.Join(root.Name(), "initial")
	} else if err := attempt.Mkdir("offline", 0o700); err != nil {
		return err
	}
	checkpointRoot, err := fsutil.OpenPhysicalRoot(checkpoint, true)
	if err != nil {
		return err
	}
	defer checkpointRoot.Close()
	if j.Control.Previous != "" {
		if err := unitinstall.CopyOfflineState(ctx, j.Control.Previous, checkpoint, []string{"admin/data"}); err != nil {
			return err
		}
	}
	if err := checkpointRoot.MkdirAll("admin/data", 0o700); err != nil {
		return err
	}
	topology, err := json.Marshal(request.Topology)
	if err != nil {
		return err
	}
	if err := fsutil.WritePrivate(attempt, "topology.json", append(topology, '\n'), false); err != nil {
		return err
	}
	deployment, err := fsutil.OpenPhysicalRoot(request.DeploymentRoot, false)
	if err != nil {
		return err
	}
	if err := privateDirectory(deployment, "identity"); err != nil {
		deployment.Close()
		return err
	}
	deployment.Close()
	master := filepath.Join(request.DeploymentRoot, "identity/admin-master.key")
	pki := filepath.Join(request.DeploymentRoot, "identity/pki")
	if err := writeJournal(root, j, "offline"); err != nil {
		return err
	}
	metadata, err := offlineAdmin(ctx, filepath.Join(extracted.Directory, "bin/moox-admin-cli"), filepath.Join(attempt.Name(), "topology.json"), filepath.Join(checkpoint, "admin/data/admin.db"), master, pki, filepath.Join(attempt.Name(), "bundles"), lock)
	if err != nil {
		return err
	}
	eventBusDirectory := filepath.Join(attempt.Name(), "eventbus")
	if err := offlineEventBus(ctx, filepath.Join(extracted.Directory, "bin/moox-admin-cli"), filepath.Join(checkpoint, "admin/data/admin.db"), master, j.HostID, eventBusDirectory, lock); err != nil {
		return err
	}
	if err := fsutil.SyncPrivateTree(ctx, checkpointRoot); err != nil {
		return err
	}
	control := request.Topology.Hosts[slices.IndexFunc(request.Topology.Hosts, func(h hostbundle.Host) bool { return h.HostID == j.HostID })]
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	eventBus, found := catalog.Component("eventbus")
	if !found || len(eventBus.Ports) != 1 {
		return errors.New("EventBus catalog endpoint is invalid")
	}
	eventBusURL := "tls://" + net.JoinHostPort(control.Address, strconv.Itoa(eventBus.Ports[0]))
	materialOptions := unitbundle.Options{HostID: j.HostID, ControlHostID: j.HostID, Address: control.Address, PrivateAddress: control.PrivateAddress, ControlAddress: control.Address, Components: control.Components, ExpectedCA: metadata.CA.SHA256, ExpectedHash: metadata.ExpectedHash, AllowOperator: true}
	if _, err := unitbundle.Load(ctx, metadata.BundleDir, materialOptions); err != nil {
		return err
	}
	j.ExpectedHash, j.CAFingerprint, j.BundleDirectory = metadata.ExpectedHash, metadata.CA.SHA256, metadata.BundleDir
	if err := writeJournal(root, j, "preparing"); err != nil {
		return err
	}
	prepared := make([]unitinstall.Prepared, 2)
	for i, unit := range []Unit{request.Host, request.Control} {
		profile, components := "host", []string{"host-gateway", "host-agent"}
		if i == 1 {
			profile, components = "control", input.components
		}
		overrides := map[string]string{}
		names := make([]string, 0, len(input.overrides[i]))
		for name := range input.overrides[i] {
			names = append(names, name)
		}
		slices.Sort(names)
		for n, name := range names {
			filename := profile + "-overrides/" + strconv.Itoa(n)
			if err := fsutil.WritePrivate(attempt, filename, input.overrides[i][name], false); err != nil {
				return err
			}
			overrides[name] = filepath.Join(attempt.Name(), filename)
		}
		environment := make(map[string]map[string]string, len(unit.Environment))
		for id, values := range unit.Environment {
			environment[id] = maps.Clone(values)
		}
		if i == 1 {
			values := environment["admin"]
			if values == nil {
				values = map[string]string{}
				environment["admin"] = values
			}
			values["MOOX_ADMIN_NODE_ID"], values["MOOX_ADMIN_ENCRYPTION_KEY_FILE"], values["MOOX_ADMIN_PKI_DIR"] = j.HostID, master, pki
			values["MOOX_ADMIN_DB_PATH"] = "./data/admin.db"
		}
		options := unitinstall.PrepareOptions{Archive: unit.Archive, SHA256: unit.SHA256, Profile: profile, DeploymentRoot: request.DeploymentRoot, UnitRoot: unit.UnitRoot, HostUnitRoot: request.Host.UnitRoot, ReleaseID: j.Attempt, MaterialDirectory: metadata.BundleDir, MaterialOptions: materialOptions, Components: components, Environment: environment, Overrides: overrides}
		options.EventBusDirectory, options.EventBusURL = eventBusDirectory, eventBusURL
		prepared[i], err = unitinstall.Prepare(ctx, options, lock)
		if err != nil {
			return err
		}
		if i == 0 {
			j.Host.Candidate = prepared[i].Directory
		} else {
			j.Control.Candidate = prepared[i].Directory
		}
		if err := writeJournal(root, j, "preparing"); err != nil {
			return err
		}
		if i == 0 {
			// Business preparation verifies its host/current directory view.
			// Publish that view before preparing control; no service starts yet.
			if err := writeJournal(root, j, "host-activating"); err != nil {
				return err
			}
			if _, err := unitinstall.Activate(ctx, unitinstall.ActivateOptions{Directory: prepared[0].Directory, NoStart: true}, lock); err != nil {
				return err
			}
		}
	}
	proxySource, err := proxyCheckpoint(ctx, input, j, prepared[1], attempt, lock)
	if err != nil {
		return err
	}
	controlRoot, err := fsutil.OpenPhysicalRoot(request.Control.UnitRoot, false)
	if err != nil {
		return err
	}
	defer controlRoot.Close()
	if err := privateDirectory(controlRoot, "state-imports"); err != nil {
		return err
	}
	imports, err := fsutil.OpenPhysicalRoot(filepath.Join(controlRoot.Name(), "state-imports"), true)
	if err != nil {
		return err
	}
	defer imports.Close()
	if err := imports.Mkdir(j.Attempt, 0o700); err != nil {
		return err
	}
	seedDirectory := filepath.Join(imports.Name(), j.Attempt)
	if err := unitinstall.CopyOfflineState(ctx, checkpoint, seedDirectory, []string{"admin/data"}); err != nil {
		return err
	}
	seedPaths := []string{"admin/data"}
	if proxySource != "" {
		if err := unitinstall.CopyOfflineState(ctx, proxySource, seedDirectory, proxyPaths); err != nil {
			return err
		}
		seedPaths = append(seedPaths, proxyPaths...)
	}
	seed, err := unitinstall.SealState(ctx, unitinstall.SealStateOptions{Directory: seedDirectory, ReleaseDirectory: prepared[1].Directory, PreviousDirectory: j.Control.Previous, Paths: seedPaths}, lock)
	if err != nil {
		return err
	}
	if err := writeJournal(root, j, "control-activating"); err != nil {
		return err
	}
	if _, err := unitinstall.Activate(ctx, unitinstall.ActivateOptions{Directory: prepared[1].Directory, NoStart: true, StateSeed: &seed}, lock); err != nil {
		return err
	}
	if err := writeJournal(root, j, "admin-starting"); err != nil {
		return err
	}
	if err := start(ctx, prepared[1].Directory, []string{"admin"}, true, lock); err != nil {
		return err
	}
	if err := writeJournal(root, j, "gateway-starting"); err != nil {
		return err
	}
	if err := start(ctx, prepared[0].Directory, []string{"host-gateway"}, true, lock); err != nil {
		return err
	}
	if err := writeJournal(root, j, "services-starting"); err != nil {
		return err
	}
	if err := start(ctx, prepared[1].Directory, []string{"eventbus"}, true, lock); err != nil {
		return err
	}
	if err := start(ctx, prepared[0].Directory, []string{"host-agent"}, false, lock); err != nil {
		return err
	}
	if err := start(ctx, prepared[1].Directory, remaining(prepared[1].Components, "admin", "eventbus"), false, lock); err != nil {
		return err
	}
	if err := verifyComplete(ctx, *j, lock); err != nil {
		return err
	}
	return writeJournal(root, j, "complete")
}
