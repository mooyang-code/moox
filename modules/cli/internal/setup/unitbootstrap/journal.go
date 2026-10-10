package unitbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

type unitState struct {
	Root      string   `json:"root"`
	Previous  string   `json:"previous"`
	Running   []string `json:"running"`
	Candidate string   `json:"candidate"`
}

type journal struct {
	Version         int       `json:"version"`
	HostID          string    `json:"host_id"`
	DeploymentRoot  string    `json:"deployment_root"`
	RequestSHA256   string    `json:"request_sha256"`
	Attempt         string    `json:"attempt"`
	Phase           string    `json:"phase"`
	Host            unitState `json:"host"`
	Control         unitState `json:"control"`
	ExpectedHash    string    `json:"expected_hash"`
	CAFingerprint   string    `json:"ca_fingerprint"`
	BundleDirectory string    `json:"bundle_directory"`
	UpdatedAt       time.Time `json:"updated_at"`
}

var phases = []string{"captured", "stopping", "offline", "preparing", "host-activating", "control-activating", "admin-starting", "gateway-starting", "services-starting", "complete", "repairing", "recovering", "rolled-back"}

func readJournal(root *os.Root, input inputs) (journal, bool, error) {
	var j journal
	if _, err := root.Lstat("workflow.json"); os.IsNotExist(err) {
		return j, false, nil
	} else if err != nil {
		return j, false, err
	}
	raw, err := fsutil.ReadPrivate(root, "workflow.json", 128<<10)
	if err != nil {
		return j, false, err
	}
	if err := decode(raw, &j); err != nil {
		return j, false, err
	}
	r := input.request
	if j.Version != 1 || j.HostID != r.Topology.ControlHostID || j.DeploymentRoot != r.DeploymentRoot || j.Host.Root != r.Host.UnitRoot || j.Control.Root != r.Control.UnitRoot || !slices.Contains(phases, j.Phase) || j.UpdatedAt.IsZero() || !validAttempt(j.Attempt) || !validDigest(j.RequestSHA256) {
		return j, false, errors.New("bootstrap journal identity, roots or phase do not match")
	}
	for _, u := range []unitState{j.Host, j.Control} {
		for _, directory := range []string{u.Previous, u.Candidate} {
			if directory != "" && (filepath.Dir(directory) != filepath.Join(u.Root, "releases") || !validAttempt(filepath.Base(directory))) {
				return j, false, errors.New("bootstrap journal release escapes its unit")
			}
		}
		if len(u.Running) > 64 || u.Candidate != "" && u.Candidate == u.Previous {
			return j, false, errors.New("bootstrap journal contains invalid release or running selections")
		}
	}
	return j, true, nil
}

func validDigest(s string) bool { return len(s) == 71 && s[:7] == "sha256:" && validHex(s[7:]) }
func validHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}
func validAttempt(s string) bool {
	if len(s) == 0 || len(s) > 64 || s[0] == '.' {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '_' && r != '.' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

func writeJournal(root *os.Root, j *journal, phase string) error {
	j.Phase, j.UpdatedAt = phase, time.Now().UTC()
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(root, "workflow.json", append(raw, '\n'), true)
}

func current(ctx context.Context, unitRoot, deploymentRoot, hostID, profile string) (unitinstall.Prepared, error) {
	root, err := fsutil.OpenPhysicalRoot(unitRoot, false)
	if err != nil {
		return unitinstall.Prepared{}, err
	}
	defer root.Close()
	target, err := root.Readlink("current")
	if os.IsNotExist(err) {
		return unitinstall.Prepared{}, nil
	}
	if err != nil || filepath.Dir(target) != filepath.Join(unitRoot, "releases") || !validAttempt(filepath.Base(target)) {
		return unitinstall.Prepared{}, errors.New("bootstrap current must point to a release within its unit")
	}
	prepared, err := unitinstall.ReadInstalled(ctx, target)
	if err != nil {
		return prepared, err
	}
	if prepared.UnitRoot != unitRoot || prepared.DeploymentRoot != deploymentRoot || prepared.HostID != hostID || prepared.Profile != profile {
		return prepared, errors.New("bootstrap current release belongs to a different host, root or profile")
	}
	return prepared, nil
}

func capture(ctx context.Context, guard *unitruntime.Maintenance, unitRoot, deploymentRoot, hostID, profile string) (unitState, error) {
	state := unitState{Root: unitRoot, Running: []string{}}
	previous, err := current(ctx, unitRoot, deploymentRoot, hostID, profile)
	if err != nil {
		return state, err
	}
	last, exists, err := unitinstall.ReadActivation(unitRoot)
	if err != nil {
		return state, err
	}
	if exists && last.Phase != "active" && last.Phase != "rolled-back" {
		return state, errors.New("bootstrap refuses a pending installation outside its workflow")
	}
	state.Previous = previous.Directory
	if previous.Directory == "" {
		return state, nil
	}
	if profile == "control" {
		if err := validateAdminState(previous.Directory); err != nil {
			return state, err
		}
	}
	plan := filepath.Join(previous.Directory, "runtime.json")
	if err := guard.CheckUnit(ctx, plan, []string{previous.Directory}); err != nil {
		return state, err
	}
	status, err := guard.Execute(ctx, plan, "status", nil)
	if err != nil {
		return state, err
	}
	for _, component := range status.Components {
		if component.PID != 0 && component.State != "paused" {
			state.Running = append(state.Running, component.ID)
		}
	}
	return state, nil
}

// Never initialize fresh signing keys because an existing deployment stored
// Admin outside the snapshot paths or lost its initialized database.
func validateAdminState(directory string) error {
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "secrets/runtime-admin.json", 1<<20)
	if err != nil {
		return errors.New("existing control release requires its Admin environment")
	}
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil {
		return errors.New("existing Admin environment is invalid")
	}
	configured := values["MOOX_ADMIN_DB_PATH"]
	if configured == "" {
		raw, err := fsutil.ReadPrivate(root, "admin/config/app.yaml", 1<<20)
		if err != nil {
			return err
		}
		var config struct {
			Database struct {
				Path string `yaml:"path"`
			} `yaml:"database"`
		}
		if yaml.Unmarshal(raw, &config) != nil {
			return errors.New("existing Admin database configuration is invalid")
		}
		configured = config.Database.Path
		if configured == "" {
			configured = "./data/admin.db"
		}
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(directory, "admin", configured)
	}
	if filepath.Clean(configured) != filepath.Join(directory, "admin/data/admin.db") {
		return errors.New("existing Admin database must belong to the release state snapshot")
	}
	info, err := root.Lstat("admin/data/admin.db")
	if err != nil || !info.Mode().IsRegular() || !fsutil.Owned(info) || info.Mode().Perm() != 0o600 || info.Size() == 0 {
		return errors.New("existing initialized Admin database is missing or not private")
	}
	return nil
}
