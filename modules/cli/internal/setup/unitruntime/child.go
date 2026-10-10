package unitruntime

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// RunComponentChild is the private exec barrier used by moox-runtime itself.
// FD 3 must be a pipe: only its parent can release the child after publishing a
// durable PID record. No signal handler or service starts before this barrier.
func RunComponentChild(planPath, componentID string) error {
	gate := os.NewFile(3, "moox-start-barrier")
	info, err := gate.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		gate.Close()
		return errors.New("component launch requires its inherited start pipe")
	}
	var token [1]byte
	_, err = io.ReadFull(gate, token[:])
	gate.Close()
	if err != nil || token[0] != 1 {
		return errors.New("component launch was not released by its parent")
	}
	plan, err := LoadPlan(planPath)
	if err != nil {
		return err
	}
	components, err := plan.selectComponents([]string{componentID})
	if err != nil {
		return err
	}
	state, err := openRuntime(plan)
	if err != nil {
		return err
	}
	defer state.close()
	record, alive, err := state.record(components[0])
	if err != nil || !alive || record.PID != os.Getpid() || record.Launcher == nil {
		return errIdentity
	}
	actual, alive, err := readProcess(os.Getpid())
	if err != nil || !alive || actual != *record.Launcher {
		return errIdentity
	}
	values, _, _, err := state.prepare(components[0])
	if err != nil || values["MOOX_BINARY_SHA256"] != record.BinarySHA256 {
		return errors.New("recorded release changed before component exec")
	}
	values["MOOX_BOOT_ID"] = record.BootID
	expected, err := releaseIdentity(actual, record.Executable)
	if err != nil || expected != record.processIdentity {
		return errIdentity
	}
	if err := os.Chdir(filepath.Join(plan.ReleaseRoot, componentID)); err != nil {
		return err
	}
	// Exec preserves PID/start ticks. Recovery accepts the recorded launcher or
	// final executable on this same pidfd throughout the transition.
	return execComponent(record.Executable, componentArguments(componentID), componentEnvironment(values))
}
