// Package unitinstall prepares software units using the shared
// host identities and maintenance lifecycle. It never reads operator manifests.
package unitinstall

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

type PrepareOptions struct {
	Archive           string             `json:"archive"`
	SHA256            string             `json:"sha256"`
	Profile           string             `json:"profile"`
	DeploymentRoot    string             `json:"deployment_root"`
	UnitRoot          string             `json:"unit_root"`
	HostUnitRoot      string             `json:"host_unit_root"`
	ReleaseID         string             `json:"release_id"`
	MaterialDirectory string             `json:"material_directory"`
	MaterialOptions   unitbundle.Options `json:"material_options"`
	Components        []string           `json:"components"`
	// Environment and Overrides are explicit rendered private input, not an
	// operator manifest. Overrides map release config paths to owned 0600 files.
	Environment map[string]map[string]string `json:"environment"`
	Overrides   map[string]string            `json:"overrides"`
	// EventBusDirectory contains a closed offline credential export. Only the
	// selected components' roles and required TLS material enter this release.
	EventBusDirectory string `json:"eventbus_directory,omitempty"`
	EventBusURL       string `json:"eventbus_url,omitempty"`
}

func (PrepareOptions) String() string     { return "MooXPrepareOptions{private inputs omitted}" }
func (o PrepareOptions) GoString() string { return o.String() }

type Prepared struct {
	Version        int                   `json:"version"`
	HostID         string                `json:"host_id"`
	Profile        string                `json:"profile"`
	Directory      string                `json:"directory"`
	DeploymentRoot string                `json:"deployment_root"`
	UnitRoot       string                `json:"unit_root"`
	HostUnitRoot   string                `json:"host_unit_root"`
	PackageSHA256  string                `json:"package_sha256"`
	Identity       unitbundle.Projection `json:"identity"`
	Components     []string              `json:"components"`
	Files          []unitpackage.File    `json:"files"`
}

func validReleaseID(id string) bool {
	if id == "" || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if r != '-' && r != '_' && r != '.' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return id[0] != '.'
}

func prepareComponents(options PrepareOptions) ([]string, error) {
	available, err := unitpackage.Components(options.Profile)
	if err != nil {
		return nil, err
	}
	ids := slices.Clone(options.Components)
	if len(ids) == 0 {
		for _, component := range available {
			ids = append(ids, component.ID)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || !slices.ContainsFunc(available, func(c unitpackage.Component) bool { return c.ID == id }) {
			return nil, errors.New("prepared components must be distinct members of their software unit")
		}
		if options.Profile != "host" && !slices.Contains(options.MaterialOptions.Components, id) {
			return nil, errors.New("prepared component is absent from the registered host placement")
		}
		seen[id] = true
	}
	if options.Profile == "host" && (len(ids) != 2 || !seen["host-gateway"] || !seen["host-agent"]) {
		return nil, errors.New("the host unit must contain both host components")
	}
	if options.Profile == "host" {
		ids = []string{"host-gateway", "host-agent"}
	}
	ids = lifecycleOrder(ids)
	if len(options.Environment) != len(ids) {
		return nil, errors.New("runtime environment must be explicit for every selected component")
	}
	for id := range options.Environment {
		if !seen[id] {
			return nil, errors.New("runtime environment contains an unrelated component")
		}
	}
	return ids, nil
}

// Prepare validates software before taking the maintenance lock, then injects
// selected private identities/config and generates the lifecycle scripts under
// that lock. Only a complete release is published. It never changes current,
// copies existing state or starts/stops a component.
func Prepare(ctx context.Context, options PrepareOptions, lockOptions unitruntime.Options) (result Prepared, returnErr error) {
	if !validReleaseID(options.ReleaseID) {
		return result, errors.New("preparation requires a bounded new release ID")
	}
	components, err := prepareComponents(options)
	if err != nil {
		return result, err
	}
	material, err := unitbundle.Load(ctx, options.MaterialDirectory, options.MaterialOptions)
	if err != nil {
		return result, err
	}
	unit, err := fsutil.OpenPhysicalRoot(options.UnitRoot, false)
	if err != nil {
		return result, err
	}
	defer unit.Close()
	deployment, err := fsutil.OpenPhysicalRoot(options.DeploymentRoot, false)
	if err != nil {
		return result, err
	}
	deployment.Close()
	if options.Profile == "host" {
		if options.HostUnitRoot != "" && options.HostUnitRoot != options.UnitRoot {
			return result, errors.New("host preparation must use its own unit root")
		}
		options.HostUnitRoot = options.UnitRoot
	} else if err := validateHostView(options, material.Metadata().HostID); err != nil {
		return result, err
	}
	if err := unit.Mkdir("releases", 0o700); err != nil && !os.IsExist(err) {
		return result, err
	}
	releases, err := fsutil.OpenPhysicalRoot(filepath.Join(options.UnitRoot, "releases"), true)
	if err != nil {
		return result, err
	}
	defer releases.Close()
	if _, err := releases.Lstat(options.ReleaseID); !os.IsNotExist(err) {
		return result, errors.New("prepared release never replaces an existing destination")
	}
	stageName := ".prepare-" + rand.Text()
	stagePath := filepath.Join(options.UnitRoot, "releases", stageName)
	defer func() {
		if err := releases.RemoveAll(stageName); err != nil {
			returnErr = errors.Join(returnErr, errors.New("could not remove unpublished prepared release"))
		}
	}()
	extracted, err := unitpackage.Extract(ctx, unitpackage.ExtractOptions{Archive: options.Archive, ExpectedSHA256: options.SHA256, Profile: options.Profile, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Destination: stagePath})
	if err != nil {
		return result, err
	}
	destination := filepath.Join(options.UnitRoot, "releases", options.ReleaseID)
	err = unitruntime.WithMaintenance(ctx, options.DeploymentRoot, material.Metadata().HostID, lockOptions, func(maintenance *unitruntime.Maintenance) error {
		if options.Profile != "host" {
			if err := validateHostView(options, material.Metadata().HostID); err != nil {
				return err
			}
		}
		root, err := fsutil.OpenPhysicalRoot(stagePath, true)
		if err != nil {
			return err
		}
		defer root.Close()
		for _, id := range components {
			if err := root.Mkdir(id, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
		}
		if err := applyOverrides(root, options, components); err != nil {
			return err
		}
		environment := make(map[string]map[string]string, len(options.Environment))
		for id, values := range options.Environment {
			environment[id] = maps.Clone(values)
		}
		options.Environment = environment
		if err := projectEventBus(root, &options, components, destination); err != nil {
			return err
		}
		projection, err := material.Inject(ctx, stagePath, components)
		if err != nil {
			return err
		}
		if options.Profile != "host" {
			if err := root.Symlink(filepath.Join(options.HostUnitRoot, "current", "host-gateway"), "host-gateway"); err != nil {
				return err
			}
		}
		if err := renderConfigurations(root, options, components, projection, destination); err != nil {
			return err
		}
		for _, id := range components {
			raw, err := json.Marshal(options.Environment[id])
			if err != nil {
				return errors.New("runtime environment cannot be encoded")
			}
			if err := fsutil.WritePrivate(root, "secrets/runtime-"+id+".json", append(raw, '\n'), false); err != nil {
				return err
			}
		}
		plan := runtimePlan(options, components, stagePath)
		if err := writePlan(root, plan, false); err != nil {
			return err
		}
		if err := unitruntime.ValidateRelease(filepath.Join(stagePath, "runtime.json")); err != nil {
			return err
		}
		plan = runtimePlan(options, components, destination)
		if err := writePlan(root, plan, true); err != nil {
			return err
		}
		if err := writeScripts(root, options); err != nil {
			return err
		}
		prepared := Prepared{Version: 1, HostID: projection.HostID, Profile: options.Profile, Directory: destination, DeploymentRoot: options.DeploymentRoot, UnitRoot: options.UnitRoot, HostUnitRoot: options.HostUnitRoot, PackageSHA256: extracted.Package.SHA256, Identity: projection, Components: components}
		prepared.Files, err = inventory(ctx, root, prepared, false)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		if err := fsutil.WritePrivate(root, "prepared.json", append(raw, '\n'), false); err != nil {
			return err
		}
		if err := syncTree(root); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		parent, err := releases.Open(".")
		if err != nil {
			return err
		}
		defer parent.Close()
		if err := fsutil.RenameExclusive(parent, stageName, options.ReleaseID); err != nil {
			return err
		}
		result = prepared
		return parent.Sync()
	})
	return result, err
}

func runtimePlan(options PrepareOptions, components []string, directory string) unitruntime.Plan {
	plan := unitruntime.Plan{Version: 1, HostID: options.MaterialOptions.HostID, DeploymentRoot: options.DeploymentRoot, ReleaseRoot: directory, CatalogSHA256: unitruntime.CatalogSHA256()}
	for _, id := range components {
		plan.Components = append(plan.Components, unitruntime.Component{ID: id, EnvironmentFile: filepath.Join(directory, "secrets", "runtime-"+id+".json")})
	}
	return plan
}
func writePlan(root *os.Root, plan unitruntime.Plan, replace bool) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(root, "runtime.json", append(raw, '\n'), replace)
}
func validateHostView(options PrepareOptions, hostID string) error {
	root, err := fsutil.OpenPhysicalRoot(options.HostUnitRoot, false)
	if err != nil {
		return err
	}
	defer root.Close()
	if pathsOverlap(options.HostUnitRoot, options.UnitRoot) {
		return errors.New("business and host unit roots must be independent")
	}
	current, err := root.Lstat("current")
	if err != nil || current.Mode()&os.ModeSymlink == 0 {
		return errors.New("host current must be a release directory link")
	}
	release, err := filepath.EvalSymlinks(filepath.Join(options.HostUnitRoot, "current"))
	if err != nil || filepath.Dir(release) != filepath.Join(options.HostUnitRoot, "releases") || !validReleaseID(filepath.Base(release)) {
		return errors.New("host current must select a bounded release inside the host unit")
	}
	plan, err := unitruntime.LoadPlan(filepath.Join(options.HostUnitRoot, "current", "runtime.json"))
	if err != nil {
		return err
	}
	if plan.ReleaseRoot != release || plan.HostID != hostID || plan.DeploymentRoot != options.DeploymentRoot || len(plan.Components) != 2 || plan.Components[0].ID != "host-gateway" || plan.Components[1].ID != "host-agent" {
		return errors.New("business unit requires the installed host unit for the same host/deployment root")
	}
	// Active host state may already contain runtime data; its immutable plan
	// and private host/root identity are checked here, rather than requiring a
	// pristine prepared inventory after the host has started.
	return nil
}
func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}
func syncTree(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		directory, err := root.Open(name)
		if err != nil {
			return err
		}
		return errors.Join(directory.Sync(), directory.Close())
	})
}
