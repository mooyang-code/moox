package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	cloudprovider "github.com/mooyang-code/moox/packages/cloudprovider"
)

const maxHostWorkers = 4

var ErrValidationFailed = errors.New("setup_validation_failed")

type Check struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Code        string `json:"code,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type Result struct {
	Checks []Check `json:"checks"`
}

type SSHChecker interface {
	Check(context.Context, topology.Host) error
}

type Dependencies struct {
	Identity cloudprovider.IdentityValidator
	SSH      SSHChecker
}

func Run(ctx context.Context, cfg *topology.Config, deps Dependencies) (Result, error) {
	result := Result{}
	if cfg == nil || deps.Identity == nil || deps.SSH == nil {
		return result, fmt.Errorf("%w: dependencies_invalid", ErrValidationFailed)
	}
	result.Checks = append(result.Checks, Check{Name: "config", Status: "valid"})

	if _, err := deps.Identity.GetCallerIdentity(ctx); err != nil {
		result.Checks = append(result.Checks, Check{Name: "tencent_cloud", Status: "invalid", Code: "tencent_auth_failed"})
		return result, fmt.Errorf("%w: tencent_auth_failed", ErrValidationFailed)
	}
	result.Checks = append(result.Checks, Check{Name: "tencent_cloud", Status: "valid"})
	return appendHostChecks(ctx, deps.SSH, result, cfg.Hosts())
}

func appendHostChecks(ctx context.Context, checker SSHChecker, result Result, hosts []topology.Host) (Result, error) {
	hostChecks := checkHosts(ctx, checker, uniqueHosts(hosts))
	result.Checks = append(result.Checks, hostChecks...)
	failed := false
	for _, check := range hostChecks {
		failed = failed || check.Status != "valid"
	}
	if failed {
		return result, ErrValidationFailed
	}
	return result, nil
}

func uniqueHosts(hosts []topology.Host) []topology.Host {
	result := make([]topology.Host, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		key := strings.ToLower(strings.TrimSpace(host.Name))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, host)
	}
	return result
}

func checkHosts(ctx context.Context, checker SSHChecker, hosts []topology.Host) []Check {
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

func hostCheck(ctx context.Context, checker SSHChecker, host topology.Host) Check {
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
