package unitruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type Options struct {
	MaintenanceLockHeld bool
	MaintenanceLockFD   int
	// BootstrapID is used only by the bootstrap coordinator to reacquire its
	// own interrupted host workflow. It never authorizes a runtime start.
	BootstrapID string
	scope       *scopedLock
}

type Result struct {
	HostID     string   `json:"host_id"`
	Operation  string   `json:"operation"`
	Skipped    bool     `json:"skipped,omitempty"`
	Components []Status `json:"components"`
}

type Status struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	PID            int    `json:"pid,omitempty"`
	Ready          bool   `json:"ready"`
	Forced         bool   `json:"forced,omitempty"`
	Binary         string `json:"binary_sha256,omitempty"`
	ReadinessError string `json:"readiness_error,omitempty"`
}

type pidRecord struct {
	processIdentity
	HostID         string           `json:"host_id"`
	ComponentID    string           `json:"component_id"`
	ReleaseRoot    string           `json:"release_root"`
	StopTimeout    time.Duration    `json:"stop_timeout_ns"`
	BinarySHA256   string           `json:"binary_sha256"`
	BootID         string           `json:"boot_id"`
	StartedAt      time.Time        `json:"started_at"`
	StartupTimeout time.Duration    `json:"startup_timeout_ns"`
	Launcher       *processIdentity `json:"launcher,omitempty"`
}

type drainRecord struct {
	PID        int           `json:"pid"`
	StartTicks string        `json:"start_ticks"`
	StartedAt  time.Time     `json:"started_at"`
	Budget     time.Duration `json:"budget_ns"`
	TermSent   bool          `json:"term_sent"`
}

type runtimeState struct {
	plan                              Plan
	catalog                           servicecatalog.Catalog
	run, pids, paused, draining, logs *os.Root
}

func Execute(ctx context.Context, planPath, operation string, ids []string, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !slices.Contains([]string{"start", "stop", "restart", "pause", "resume", "healthcheck", "status"}, operation) {
		return Result{}, errors.New("unknown runtime operation")
	}
	plan, err := LoadPlan(planPath)
	if err != nil {
		return Result{}, err
	}
	leave, err := opts.enterScope(plan.HostID, plan.DeploymentRoot)
	if err != nil {
		return Result{}, err
	}
	defer leave()
	components, err := plan.selectComponents(ids)
	if err != nil {
		return Result{}, err
	}
	state, err := openRuntime(plan)
	if err != nil {
		return Result{}, err
	}
	defer state.close()
	result := Result{HostID: plan.HostID, Operation: operation, Components: []Status{}}
	lock, skipped, err := acquireLock(ctx, state.run, opts.MaintenanceLockHeld, opts.MaintenanceLockFD, operation == "healthcheck")
	if err != nil {
		return result, err
	}
	if skipped {
		result.Skipped = true
		return result, nil
	}
	defer lock.Close()
	if err := state.ensureHostIdentity(); err != nil {
		return result, err
	}
	if pending, err := state.pendingInstallation(); err != nil {
		return result, err
	} else if pending != nil && !opts.MaintenanceLockHeld && slices.Contains([]string{"start", "restart", "resume", "healthcheck"}, operation) {
		if operation == "healthcheck" {
			result.Skipped = true
			return result, nil
		}
		return result, errors.New("an interrupted installation must be recovered before starting components")
	}
	if pending, err := state.pendingBootstrap(); err != nil {
		return result, err
	} else if pending != nil && !opts.MaintenanceLockHeld && slices.Contains([]string{"start", "restart", "resume", "healthcheck"}, operation) {
		if operation == "healthcheck" {
			result.Skipped = true
			return result, nil
		}
		return result, errors.New("an interrupted bootstrap must be recovered before starting components")
	}
	if operation == "restart" {
		stopping := slices.Clone(components)
		slices.Reverse(stopping)
		for _, component := range stopping {
			if status, err := state.stop(ctx, component); err != nil {
				result.Components = append(result.Components, status)
				return result, err
			}
		}
	}
	if operation == "stop" || operation == "pause" {
		slices.Reverse(components)
	}
	if operation == "pause" {
		// Persist every requested pause before stopping the first process. A
		// cancellation must not permit watchdog to restart the remaining targets.
		for _, component := range components {
			if err := writeState(state.paused, component.ID, map[string]string{"host_id": plan.HostID}); err != nil {
				return result, err
			}
		}
	}
	for _, component := range components {
		action := operation
		if action == "restart" {
			action = "start"
		}
		status, err := state.executeComponent(ctx, component, action)
		result.Components = append(result.Components, status)
		if err != nil {
			return result, fmt.Errorf("%s %s: %w", operation, component.ID, err)
		}
	}
	return result, nil
}

func openRuntime(plan Plan) (*runtimeState, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	deployment, err := os.OpenRoot(plan.DeploymentRoot)
	if err != nil {
		return nil, err
	}
	defer deployment.Close()
	state := &runtimeState{plan: plan, catalog: catalog}
	state.run, err = privateDirectory(deployment, "run")
	if err != nil {
		return nil, err
	}
	for name, destination := range map[string]**os.Root{"pids": &state.pids, "paused": &state.paused, "draining": &state.draining, "logs": &state.logs} {
		*destination, err = privateDirectory(state.run, name)
		if err != nil {
			state.close()
			return nil, err
		}
	}
	return state, nil
}

func (s *runtimeState) close() {
	for _, root := range []*os.Root{s.logs, s.draining, s.paused, s.pids, s.run} {
		if root != nil {
			root.Close()
		}
	}
}

func (s *runtimeState) executeComponent(ctx context.Context, component Component, operation string) (Status, error) {
	switch operation {
	case "start":
		return s.start(ctx, component)
	case "stop":
		return s.stop(ctx, component)
	case "pause":
		status, err := s.stop(ctx, component)
		if err == nil {
			status.State = "paused"
		}
		return status, err
	case "resume":
		// Validate the new release before clearing a persistent pause.
		if _, _, _, err := s.prepare(component); err != nil {
			return Status{ID: component.ID, State: "paused"}, err
		}
		if draining, err := stateExists(s.draining, component.ID); err != nil {
			return Status{ID: component.ID}, err
		} else if draining {
			if _, err := s.stop(ctx, component); err != nil {
				return Status{ID: component.ID}, err
			}
		}
		if err := removeState(s.paused, component.ID); err != nil {
			return Status{ID: component.ID}, err
		}
		status, err := s.start(ctx, component)
		if err != nil {
			return status, errors.Join(err, writeState(s.paused, component.ID, map[string]string{"host_id": s.plan.HostID}))
		}
		return status, nil
	case "healthcheck":
		return s.healthcheck(ctx, component)
	case "status":
		return s.inspect(ctx, component)
	default:
		return s.status(component)
	}
}

func (s *runtimeState) record(component Component) (pidRecord, bool, error) {
	var record pidRecord
	err := privateJSON(filepath.Join(s.pids.Name(), component.ID+".json"), &record)
	if os.IsNotExist(err) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	catalog, ok := s.catalog.Component(component.ID)
	if !ok || record.HostID != s.plan.HostID || record.ComponentID != component.ID || record.PID <= 1 ||
		record.UID != uint32(os.Getuid()) || record.StartTicks == "" || record.ReleaseRoot == "" || !filepath.IsAbs(record.ReleaseRoot) || filepath.Clean(record.ReleaseRoot) != record.ReleaseRoot ||
		record.Executable != filepath.Join(record.ReleaseRoot, "bin", catalog.Binary) || record.StopTimeout <= 0 || record.StopTimeout > maxStopTimeout || !validDigest(record.BinarySHA256) || !validDigest(record.BootID) || record.StartedAt.IsZero() || record.StartedAt.After(time.Now().Add(time.Second)) || record.StartupTimeout <= 0 || record.StartupTimeout > 10*time.Minute {
		return record, false, errIdentity
	}
	if launch := record.Launcher; launch != nil && (launch.PID != record.PID || launch.StartTicks != record.StartTicks || launch.UID != record.UID || !filepath.IsAbs(launch.Executable) || filepath.Clean(launch.Executable) != launch.Executable || launch.Inode == 0) {
		return record, false, errIdentity
	}
	file, alive, err := openRecordedProcess(record)
	if file != nil {
		file.Close()
	}
	return record, alive, err
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func openRecordedProcess(record pidRecord) (*os.File, bool, error) {
	if record.Launcher != nil {
		return openProcess(record.processIdentity, *record.Launcher)
	}
	return openProcess(record.processIdentity)
}

func (s *runtimeState) status(component Component) (Status, error) {
	status := Status{ID: component.ID, State: "stopped"}
	for _, marker := range []struct {
		root *os.Root
		name string
	}{{s.paused, "paused"}, {s.draining, "draining"}} {
		exists, err := stateExists(marker.root, component.ID)
		if err != nil {
			return status, err
		}
		if exists {
			status.State = marker.name
			return status, nil
		}
	}
	record, alive, err := s.record(component)
	if err != nil {
		return status, err
	}
	if alive {
		status.State, status.PID, status.Binary = "running", record.PID, record.BinarySHA256
	}
	return status, nil
}

// inspect is read-only. A health failure must not hide a verified running PID
// or restart it; callers can distinguish not-ready from a failed probe.
func (s *runtimeState) inspect(ctx context.Context, component Component) (Status, error) {
	status, err := s.status(component)
	if err != nil || status.State != "running" {
		return status, err
	}
	record, _, err := s.record(component)
	if err != nil {
		return status, err
	}
	if record.Launcher != nil {
		return status, nil
	}
	values, err := loadEnvironment(Component{ID: component.ID, EnvironmentFile: filepath.Join(record.ReleaseRoot, "secrets", "runtime-"+component.ID+".json")})
	if err != nil {
		status.ReadinessError = "runtime health environment cannot be loaded"
		return status, nil
	}
	status.Ready, err = s.probe(ctx, component, record, values, true)
	if ctx.Err() != nil {
		return status, ctx.Err()
	}
	if err != nil {
		status.ReadinessError = err.Error()
	}
	return status, nil
}

func componentArguments(id string) []string {
	switch id {
	case "console-proxy":
		return []string{"serve", "-config=config/app.yaml"}
	case "host-gateway", "access", "egress-proxy", "factor-mgr":
		return []string{"-config=config/app.yaml", "-conf=config/trpc_go.yaml"}
	case "web-host":
		return nil
	default:
		return []string{"-conf=config/trpc_go.yaml"}
	}
}

func (s *runtimeState) prepare(component Component) (map[string]string, time.Duration, time.Duration, error) {
	values, err := loadEnvironment(component)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := validateHealthEnvironment(values); err != nil {
		return nil, 0, 0, err
	}
	stop, startup, err := lifecycleBudgets(s.plan.ReleaseRoot, component.ID)
	if err != nil {
		return nil, 0, 0, err
	}
	catalog, _ := s.catalog.Component(component.ID)
	binary := filepath.Join(s.plan.ReleaseRoot, "bin", catalog.Binary)
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 || !ownerMatches(info) {
		return nil, 0, 0, errors.New("runtime executable must be an owned regular 0755 release binary")
	}
	if err := physicalDirectory(filepath.Join(s.plan.ReleaseRoot, component.ID)); err != nil {
		return nil, 0, 0, err
	}
	file, err := os.Open(binary)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, 0, 0, err
	}
	values["MOOX_BINARY_SHA256"] = hex.EncodeToString(digest.Sum(nil))
	values["MOOX_NODE_ID"], values["MOOX_SERVICE_NAME"] = s.plan.HostID, component.ID
	values["MOOX_INSTANCE_ID"] = component.ID + "@" + s.plan.HostID
	return values, stop, startup, nil
}

func (s *runtimeState) start(ctx context.Context, component Component) (Status, error) {
	status, err := s.status(component)
	if err != nil || status.State == "paused" || status.State == "draining" {
		return status, err
	}
	if status.State == "running" {
		record, _, err := s.record(component)
		if err != nil {
			return status, err
		}
		values, err := loadEnvironment(Component{ID: component.ID, EnvironmentFile: filepath.Join(record.ReleaseRoot, "secrets", "runtime-"+component.ID+".json")})
		if err != nil {
			return status, err
		}
		if record.Launcher != nil {
			return s.awaitStartup(ctx, component, record, values, status)
		}
		status.Ready, err = s.probe(ctx, component, record, values, true)
		return status, err
	}
	values, stop, startup, err := s.prepare(component)
	if err != nil {
		return status, err
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	catalog, _ := s.catalog.Component(component.ID)
	self, err := os.Executable()
	if err != nil {
		return status, err
	}
	gate, release, err := os.Pipe()
	if err != nil {
		return status, err
	}
	defer gate.Close()
	defer release.Close()
	command := exec.Command(self, "exec-component", filepath.Join(s.plan.ReleaseRoot, "runtime.json"), component.ID)
	command.ExtraFiles = []*os.File{gate}
	command.Dir = filepath.Join(s.plan.ReleaseRoot, component.ID)
	// Inherit ordinary OS settings, replacing every MOOX variable with the
	// explicit environment of this release. Operator-side MOOX secrets cannot
	// accidentally become an unrelated service's environment.
	bootID, err := requestauth.NewNonce()
	if err != nil {
		return status, err
	}
	values["MOOX_BOOT_ID"] = bootID
	command.Env = componentEnvironment(values)
	detachCommand(command)
	logName := component.ID + ".log"
	if info, err := s.logs.Lstat(logName); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownerMatches(info)) {
		return status, errors.New("runtime output must use an owned regular 0600 log")
	} else if err != nil && !os.IsNotExist(err) {
		return status, err
	}
	logFile, err := s.logs.OpenFile(logName, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return status, err
	}
	command.Stdout, command.Stderr = logFile, logFile
	err = command.Start()
	logFile.Close()
	if err != nil {
		return status, errors.New("could not start the release process")
	}
	go func() { _ = command.Wait() }()
	identity, alive, err := readProcess(command.Process.Pid)
	if err != nil || !alive || identity.Executable != command.Path {
		_ = command.Process.Kill()
		return status, errors.New("started process identity could not be established")
	}
	expected, err := releaseIdentity(identity, filepath.Join(s.plan.ReleaseRoot, "bin", catalog.Binary))
	if err != nil {
		_ = command.Process.Kill()
		return status, err
	}
	record := pidRecord{processIdentity: expected, HostID: s.plan.HostID, ComponentID: component.ID, ReleaseRoot: s.plan.ReleaseRoot, StopTimeout: stop, BinarySHA256: values["MOOX_BINARY_SHA256"], BootID: bootID, StartedAt: time.Now().UTC(), StartupTimeout: startup, Launcher: &identity}
	if err := writeState(s.pids, component.ID+".json", record); err != nil {
		_ = command.Process.Kill()
		return status, err
	}
	status.State, status.PID, status.Binary = "running", record.PID, record.BinarySHA256
	// The child cannot exec a service until its identity record is durable.
	// If this helper dies earlier, the pipe closes and the waiting child exits.
	if _, err := release.Write([]byte{1}); err != nil {
		_ = command.Process.Kill()
		return status, errors.New("could not release the recorded child process")
	}
	_ = release.Close()
	return s.awaitStartup(ctx, component, record, values, status)
}

func componentEnvironment(values map[string]string) []string {
	var environment []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "MOOX_") {
			environment = append(environment, value)
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func (s *runtimeState) awaitStartup(ctx context.Context, component Component, record pidRecord, values map[string]string, status Status) (Status, error) {
	readyCtx, cancel := context.WithDeadline(ctx, record.StartedAt.Add(record.StartupTimeout))
	defer cancel()
	for {
		_, alive, identityErr := s.record(component)
		if identityErr != nil || !alive {
			break
		}
		actual, alive, identityErr := readProcess(record.PID)
		if identityErr != nil || !alive {
			break
		}
		if actual == record.processIdentity {
			ready, probeErr := s.probe(readyCtx, component, record, values, true)
			if probeErr == nil && ready {
				record.Launcher = nil
				if err := writeState(s.pids, component.ID+".json", record); err != nil {
					break
				}
				status.Ready = true
				return status, nil
			}
		}
		select {
		case <-readyCtx.Done():
			goto cleanup
		case <-time.After(100 * time.Millisecond):
		}
	}
cleanup:
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), record.StopTimeout+10*time.Second)
	defer cleanupCancel()
	_, stopErr := s.stop(cleanupCtx, component)
	return status, errors.Join(errors.New("release process did not become ready"), stopErr)
}

func (s *runtimeState) stop(ctx context.Context, component Component) (Status, error) {
	status := Status{ID: component.ID, State: "stopped"}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	record, alive, err := s.record(component)
	if err != nil {
		return status, err
	}
	if !alive {
		return status, errors.Join(removeState(s.pids, component.ID+".json"), removeState(s.draining, component.ID))
	}
	file, alive, err := openRecordedProcess(record)
	if err != nil {
		return status, err
	}
	if !alive {
		return status, errors.Join(removeState(s.pids, component.ID+".json"), removeState(s.draining, component.ID))
	}
	defer file.Close()
	status.State, status.PID, status.Binary = "draining", record.PID, record.BinarySHA256
	drain := drainRecord{PID: record.PID, StartTicks: record.StartTicks, StartedAt: time.Now().UTC(), Budget: record.StopTimeout}
	var previous drainRecord
	if err := privateJSON(filepath.Join(s.draining.Name(), component.ID), &previous); err == nil {
		if previous.PID == record.PID && previous.StartTicks == record.StartTicks && previous.TermSent {
			if previous.StartedAt.IsZero() || previous.StartedAt.After(time.Now().Add(time.Second)) || previous.Budget < record.StopTimeout || previous.Budget > maxStopTimeout {
				return status, errors.New("invalid persisted drain budget")
			}
			drain = previous
		}
	} else if !os.IsNotExist(err) {
		return status, err
	}
	if !drain.TermSent {
		if err := writeState(s.draining, component.ID, drain); err != nil {
			return status, err
		}
		if err := signalProcess(file, false); err != nil {
			return status, err
		}
		drain.StartedAt, drain.TermSent = time.Now().UTC(), true
		if err := writeState(s.draining, component.ID, drain); err != nil {
			return status, err
		}
	}
	exited, err := waitProcess(ctx, file, drain.StartedAt.Add(drain.Budget))
	if err != nil {
		// Preserve the drain marker on cancellation. Watchdog must not restart
		// the process until an explicit operation completes its stop.
		return status, err
	}
	if !exited {
		check, alive, err := openRecordedProcess(record)
		if check != nil {
			check.Close()
		}
		if err != nil {
			return status, err
		}
		if alive {
			if err := signalProcess(file, true); err != nil {
				return status, err
			}
			status.Forced = true
		}
		// Escalation is asynchronous. Keep the lock and marker until the old
		// pidfd reports exit, before any replacement can acquire listeners/CA.
		exited, err = waitProcess(ctx, file, time.Now().Add(10*time.Second))
		if err != nil {
			return status, err
		}
		if !exited {
			return status, errors.New("forced process exit was not observed")
		}
	}
	status.State, status.PID = "stopped", 0
	return status, errors.Join(removeState(s.pids, component.ID+".json"), removeState(s.draining, component.ID))
}

func (s *runtimeState) healthcheck(ctx context.Context, component Component) (Status, error) {
	status, err := s.status(component)
	if err != nil || status.State == "paused" || status.State == "draining" {
		return status, err
	}
	if status.State == "stopped" {
		return s.start(ctx, component)
	}
	values, err := loadEnvironment(component)
	if err != nil {
		return status, err
	}
	record, _, err := s.record(component)
	if err != nil {
		return status, err
	}
	if record.Launcher != nil {
		return s.start(ctx, component)
	}
	live, err := s.probe(ctx, component, record, values, false)
	if ctx.Err() != nil {
		return status, ctx.Err()
	}
	if errors.Is(err, errHealthIdentity) {
		return status, err
	}
	if err == nil && live {
		status.Ready, _ = s.probe(ctx, component, record, values, true)
		return status, nil
	}
	if _, err := s.stop(ctx, component); err != nil {
		return status, err
	}
	return s.start(ctx, component)
}

func waitProcess(ctx context.Context, file *os.File, deadline time.Time) (bool, error) {
	for {
		exited, err := processExited(file)
		if err != nil || exited {
			return exited, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
