// Package unitruntime owns the shared rootless Linux lifecycle of deployment
// units. It never reads the operator manifest or opens deployment SSH sessions.
package unitruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

const (
	DefaultStopTimeout = 2 * time.Minute
	maxStopTimeout     = 30 * time.Minute
	maxPrivateBytes    = 1 << 20
)

type Plan struct {
	Version        int         `json:"version"`
	HostID         string      `json:"host_id"`
	DeploymentRoot string      `json:"deployment_root"`
	ReleaseRoot    string      `json:"release_root"`
	CatalogSHA256  string      `json:"catalog_sha256"`
	Components     []Component `json:"components"`
}

// EnvironmentFile is a private JSON map of MOOX_* variables. Secrets remain in
// files and the child environment, never process arguments or runtime output.
type Component struct {
	ID              string `json:"id"`
	EnvironmentFile string `json:"environment_file"`
}

func CatalogSHA256() string {
	digest := sha256.Sum256(servicecatalog.EmbeddedYAML())
	return "sha256:" + hex.EncodeToString(digest[:])
}

func privateJSON(path string, out any) error {
	raw, err := readPrivate(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("runtime metadata requires one valid JSON document with known fields")
	}
	return nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownerMatches(info) || info.Size() <= 0 || info.Size() > maxPrivateBytes {
		return nil, errors.New("runtime private input must be a bounded regular 0600 file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("runtime private input changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPrivateBytes+1))
	if err != nil || len(raw) > maxPrivateBytes {
		return nil, errors.New("runtime private input cannot be read or exceeds its bound")
	}
	return raw, nil
}

func physicalDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") || path == string(filepath.Separator) {
		return errors.New("runtime roots must be absolute clean directory paths")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return errors.New("runtime roots must name existing physical directories")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !ownerMatches(info) || info.Mode().Perm()&0o022 != 0 {
		return errors.New("runtime root is not a directory")
	}
	return nil
}

func LoadPlan(path string) (Plan, error) {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return Plan{}, errors.New("runtime plan must be a regular file, with directory views permitted")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := privateJSON(physical, &plan); err != nil {
		return Plan{}, err
	}
	if plan.Version != 1 || !servicecatalog.ValidHostID(plan.HostID) || plan.CatalogSHA256 != CatalogSHA256() || len(plan.Components) == 0 || len(plan.Components) > 64 {
		return Plan{}, errors.New("runtime plan requires version 1, a canonical host, the current catalog and deployed components")
	}
	if physical != filepath.Join(plan.ReleaseRoot, "runtime.json") {
		return Plan{}, errors.New("runtime.json must belong to its physical release root")
	}
	for _, root := range []string{plan.DeploymentRoot, plan.ReleaseRoot} {
		if err := physicalDirectory(root); err != nil {
			return Plan{}, err
		}
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return Plan{}, err
	}
	seen := map[string]bool{}
	for _, component := range plan.Components {
		if _, ok := catalog.Component(component.ID); !ok || seen[component.ID] {
			return Plan{}, errors.New("runtime components must be unique catalog IDs")
		}
		seen[component.ID] = true
		if component.EnvironmentFile != filepath.Join(plan.ReleaseRoot, "secrets", "runtime-"+component.ID+".json") {
			return Plan{}, errors.New("runtime environment must use its private release file")
		}
	}
	return plan, nil
}

func (p Plan) selectComponents(ids []string) ([]Component, error) {
	if len(ids) == 0 {
		return slices.Clone(p.Components), nil
	}
	var components []Component
	seen := map[string]bool{}
	for _, id := range ids {
		index := slices.IndexFunc(p.Components, func(c Component) bool { return c.ID == id })
		if index < 0 || seen[id] {
			return nil, errors.New("runtime selection must contain distinct deployed components")
		}
		seen[id] = true
		components = append(components, p.Components[index])
	}
	return components, nil
}

func loadEnvironment(component Component) (map[string]string, error) {
	var values map[string]string
	if err := privateJSON(component.EnvironmentFile, &values); err != nil {
		return nil, err
	}
	if len(values) == 0 || len(values) > 256 {
		return nil, errors.New("runtime environment must be a bounded MOOX variable map")
	}
	for key, value := range values {
		if !strings.HasPrefix(key, "MOOX_") || len(key) > 128 || strings.ContainsAny(value, "\x00") {
			return nil, errors.New("runtime environment accepts only bounded MOOX variable names and NUL-free values")
		}
		for _, c := range key {
			if c != '_' && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return nil, errors.New("runtime environment variable name is invalid")
			}
		}
	}
	return values, nil
}

// lifecycleBudgets reads the actual proxy release's explicit lifecycle. No
// shorter hardcoded fallback may override the engine's configured grace time.
func lifecycleBudgets(releaseRoot, componentID string) (time.Duration, time.Duration, error) {
	if componentID != "console-proxy" {
		return DefaultStopTimeout, 3 * time.Minute, nil
	}
	raw, err := readPrivate(filepath.Join(releaseRoot, "console-proxy", "config", "app.yaml"))
	if err != nil {
		return 0, 0, err
	}
	var config struct {
		Lifecycle struct {
			Drain   string `yaml:"drain_timeout"`
			Engine  string `yaml:"engine_stop_timeout"`
			Cleanup string `yaml:"cleanup_margin"`
			Startup string `yaml:"startup_timeout"`
		} `yaml:"lifecycle"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return 0, 0, errors.New("proxy lifecycle requires one valid configuration document")
	}
	var durations []time.Duration
	for _, value := range []string{config.Lifecycle.Drain, config.Lifecycle.Engine, config.Lifecycle.Cleanup, config.Lifecycle.Startup} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 || duration > 10*time.Minute {
			return 0, 0, errors.New("proxy lifecycle durations must be explicit, positive and at most 10m each")
		}
		durations = append(durations, duration)
	}
	return durations[0] + durations[1] + durations[2], durations[3], nil
}
