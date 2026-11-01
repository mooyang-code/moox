package validate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	cloudprovider "github.com/mooyang-code/moox/packages/cloudprovider"
	"github.com/mooyang-code/moox/packages/servicecatalog"
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
	return appendHostChecks(ctx, snapshot, deps.SSH, result, snapshot.Manifest.Hosts())
}

func configResult(snapshot *setupconfig.Snapshot) (Result, error) {
	result := Result{}
	if err := snapshot.VerifyUnchanged(); err != nil {
		result.Checks = append(result.Checks, Check{Name: "config", Status: "invalid", Code: "config_changed"})
		return result, fmt.Errorf("%w: config_changed", ErrValidationFailed)
	}
	result.Checks = append(result.Checks, Check{Name: "config", Status: "valid"})
	if err := CheckDeployments(snapshot.Manifest); err != nil {
		result.Checks = append(result.Checks, Check{Name: "deployments", Status: "invalid", Code: "deployment_incomplete", Detail: err.Error()})
		return result, fmt.Errorf("%w: %w", ErrValidationFailed, err)
	}
	result.Checks = append(result.Checks, Check{Name: "deployments", Status: "valid"})
	return result, nil
}

// CheckDeployments 确认配置里的部署完整：授权服务都有组件提供。这是纯配置检查，不连接任何主机，
// 缺组件时部署到一半才会以“未就绪”的形式暴露，所以在动手之前就要报出来。
func CheckDeployments(manifest setupconfig.Manifest) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	deployed := []string{}
	for _, components := range manifest.Deployments {
		deployed = append(deployed, components...)
	}
	missing := catalog.MissingGrants(deployed)
	if len(missing) == 0 {
		return nil
	}
	control := manifest.ControlHost().Name
	components := make([]string, 0, len(missing))
	for _, item := range missing {
		if !slices.Contains(components, item.Component) {
			components = append(components, item.Component)
		}
	}
	return fmt.Errorf("deployment_incomplete: [deployments] 缺少组件 %s，Access 会一直无法就绪；运行 moox-cli config migrate --add-deployment %s=%s --yes 补上，再重新校验",
		strings.Join(components, "、"), control, strings.Join(components, ","))
}

func appendHostChecks(ctx context.Context, snapshot *setupconfig.Snapshot, checker SSHChecker, result Result, hosts []setupconfig.Host) (Result, error) {
	hostChecks := checkHosts(ctx, checker, uniqueHosts(hosts))
	result.Checks = append(result.Checks, hostChecks...)
	failed := false
	for _, check := range hostChecks {
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

func uniqueHosts(hosts []setupconfig.Host) []setupconfig.Host {
	result := make([]setupconfig.Host, 0, len(hosts))
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

func checkHosts(ctx context.Context, checker SSHChecker, hosts []setupconfig.Host) []Check {
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
