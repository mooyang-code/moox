package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	cloudprovider "github.com/mooyang-code/moox/packages/cloudprovider"
)

const maxHostWorkers = 4

var ErrValidationFailed = errors.New("setup_validation_failed")

type Check struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Code        string `json:"code,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type Result struct {
	Checks []Check `json:"checks"`
}

type SSHChecker interface {
	Check(context.Context, setupconfig.Host) error
}

type Dependencies struct {
	Identity cloudprovider.IdentityValidator
	SSH      SSHChecker
}

func Run(ctx context.Context, snapshot *setupconfig.Snapshot, deps Dependencies) (Result, error) {
	result := Result{}
	if snapshot == nil || deps.Identity == nil || deps.SSH == nil {
		return result, fmt.Errorf("%w: dependencies_invalid", ErrValidationFailed)
	}
	result, err := configResult(snapshot)
	if err != nil {
		return result, err
	}

	if _, err := deps.Identity.GetCallerIdentity(ctx); err != nil {
		result.Checks = append(result.Checks, Check{Name: "tencent_cloud", Status: "invalid", Code: "tencent_auth_failed"})
		return result, fmt.Errorf("%w: tencent_auth_failed", ErrValidationFailed)
	}
	result.Checks = append(result.Checks, Check{Name: "tencent_cloud", Status: "valid"})
	return appendHostChecks(ctx, snapshot, deps.SSH, result)
}

// RunSSH validates the immutable setup snapshot and all configured SSH hosts
// without contacting Tencent Cloud. Deployment commands use this narrower
// gate; cloud credentials are checked only by Run and cloud operations.
func RunSSH(ctx context.Context, snapshot *setupconfig.Snapshot, deps Dependencies) (Result, error) {
	if snapshot == nil || deps.SSH == nil {
		return Result{}, fmt.Errorf("%w: dependencies_invalid", ErrValidationFailed)
	}
	result, err := configResult(snapshot)
	if err != nil {
		return result, err
	}
	return appendHostChecks(ctx, snapshot, deps.SSH, result)
}

func configResult(snapshot *setupconfig.Snapshot) (Result, error) {
	result := Result{}
	if err := snapshot.VerifyUnchanged(); err != nil {
		result.Checks = append(result.Checks, Check{Name: "config", Status: "invalid", Code: "config_changed"})
		return result, fmt.Errorf("%w: config_changed", ErrValidationFailed)
	}
	result.Checks = append(result.Checks, Check{Name: "config", Status: "valid"})
	return result, nil
}

func appendHostChecks(ctx context.Context, snapshot *setupconfig.Snapshot, checker SSHChecker, result Result) (Result, error) {

	failed := false
	control := hostCheck(ctx, checker, snapshot.Manifest.ControlHost)
	result.Checks = append(result.Checks, control)
	failed = failed || control.Status != "valid"

	otherChecks := checkOtherHosts(ctx, checker, snapshot.Manifest.OtherHosts)
	result.Checks = append(result.Checks, otherChecks...)
	for _, check := range otherChecks {
		failed = failed || check.Status != "valid"
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return result, fmt.Errorf("%w: config_changed", ErrValidationFailed)
	}
	if failed {
		return result, ErrValidationFailed
	}
	return result, nil
}

func checkOtherHosts(ctx context.Context, checker SSHChecker, hosts []setupconfig.Host) []Check {
	checks := make([]Check, len(hosts))
	if len(hosts) == 0 {
		return checks
	}
	workers := maxHostWorkers
	if len(hosts) < workers {
		workers = len(hosts)
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for index := range jobs {
				checks[index] = hostCheck(ctx, checker, hosts[index])
			}
		}()
	}
	for index := range hosts {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	return checks
}

func hostCheck(ctx context.Context, checker SSHChecker, host setupconfig.Host) Check {
	check := Check{Name: "host:" + host.Name, Status: "valid"}
	if err := checker.Check(ctx, host); err != nil {
		check.Status = "invalid"
		check.Code = sshErrorCode(err)
		if check.Code == "host_key_unknown" {
			check.Fingerprint = sshFingerprint(err)
		}
	}
	return check
}

func sshFingerprint(err error) string {
	message := err.Error()
	index := strings.LastIndex(message, "SHA256:")
	if index < 0 {
		return ""
	}
	return strings.TrimSpace(message[index:])
}

func sshErrorCode(err error) string {
	switch {
	case errors.Is(err, setupssh.ErrHostKeyUnknown):
		return "host_key_unknown"
	case errors.Is(err, setupssh.ErrAuthFailed):
		return "ssh_auth_failed"
	case errors.Is(err, setupssh.ErrUnreachable):
		return "ssh_unreachable"
	default:
		return "ssh_validation_failed"
	}
}
