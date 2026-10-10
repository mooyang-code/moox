//go:build !linux

package unitruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

var errLinux = errors.New("deployment runtime process operations require Linux")

func ownerMatches(os.FileInfo) bool                  { return true }
func readProcess(int) (processIdentity, bool, error) { return processIdentity{}, false, errLinux }
func openProcess(processIdentity, ...processIdentity) (*os.File, bool, error) {
	return nil, false, errLinux
}
func releaseIdentity(processIdentity, string) (processIdentity, error) {
	return processIdentity{}, errLinux
}
func execComponent(string, []string, []string) error { return errLinux }
func signalProcess(*os.File, bool) error             { return errLinux }
func processExited(*os.File) (bool, error)           { return false, errLinux }
func detachCommand(*exec.Cmd)                        {}
func acquireLock(context.Context, *os.Root, bool, int, bool) (*os.File, bool, error) {
	return nil, false, errLinux
}
