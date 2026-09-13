package privatenet

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

type Cloud interface {
	LookupHost(ctx context.Context, publicIP string, regions []string) (tencent.CloudInstance, error)
	DescribeVpc(ctx context.Context, region, vpcID string) (tencent.VpcInfo, error)
	FindCCN(ctx context.Context, homeRegion, name string) (tencent.CCNInfo, bool, error)
	DetachVPC(ctx context.Context, homeRegion, ccnID, vpcRegion, vpcID string) error
	ListSCF(ctx context.Context, region, namespace string, prefixes []string) ([]tencent.SCFFunction, error)
	GetSCF(ctx context.Context, region, namespace, name string) (tencent.SCFFunction, error)
	UpdateSCF(ctx context.Context, region, namespace, name, vpcID, subnetID, publicNet string, env map[string]string) error
}

type Action struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Detail  string `json:"detail,omitempty"`
	Status  string `json:"status"`
	Created bool   `json:"created,omitempty"`
}

type Result struct {
	DryRun            bool              `json:"dry_run"`
	Status            string            `json:"status"`
	Plan              Plan              `json:"plan"`
	Actions           []Action          `json:"actions"`
	SCFUpdated        int               `json:"scf_updated"`
	SCFAlreadyBound   int               `json:"scf_already_bound"`
	Probes            []Probe           `json:"probes,omitempty"`
	RuntimeConfigHits []Probe           `json:"runtime_config_hits,omitempty"`
	RecommendedConfig RecommendedConfig `json:"recommended_config"`
}

func ResolveHosts(ctx context.Context, cloud Cloud, hosts []HostTarget, regions []string) ([]ResolvedHost, error) {
	resolved := make([]ResolvedHost, 0, len(hosts))
	for _, host := range hosts {
		instance, err := cloud.LookupHost(ctx, host.Address, regions)
		if err != nil {
			return nil, fmt.Errorf("lookup %s (%s): %w", host.Name, host.Address, err)
		}
		item := ResolvedHost{HostTarget: host, Instance: instance}
		if instance.Kind == tencent.KindCVM && instance.VpcID != "" {
			vpc, err := cloud.DescribeVpc(ctx, instance.Region, instance.VpcID)
			if err != nil {
				return nil, fmt.Errorf("describe vpc for %s: %w", host.Name, err)
			}
			item.CidrBlock = vpc.CidrBlock
		}
		if item.CidrBlock == "" && len(instance.PrivateIPs) > 0 {
			item.CidrBlock = tencent.InferPrivateCIDR(instance.PrivateIPs[0])
		}
		resolved = append(resolved, item)
	}
	return resolved, nil
}

func Apply(ctx context.Context, cloud Cloud, opts Options, plan Plan, stderr io.Writer) (Result, error) {
	result := Result{DryRun: opts.DryRun, Status: "ready", Plan: plan, RecommendedConfig: plan.Recommended, Actions: []Action{}}
	progress := func(format string, args ...any) {
		if stderr != nil {
			fmt.Fprintf(stderr, format+"\n", args...)
		}
	}
	record := func(kind, target, detail, status string, created bool) {
		result.Actions = append(result.Actions, Action{Kind: kind, Target: target, Detail: detail, Status: status, Created: created})
	}

	if opts.DryRun {
		result.Status = "dry_run"
		for _, host := range plan.Hosts {
			record("discover-host", host.Name, host.Instance.Kind+" "+host.Instance.Region+" "+host.Instance.InstanceID, "planned", false)
		}
		if opts.RestoreSCFPublic {
			for _, bind := range plan.SCF {
				functions, err := cloud.ListSCF(ctx, bind.Region, bind.Namespace, bind.Prefixes)
				if err != nil {
					return result, fmt.Errorf("list scf %s: %w", bind.Region, err)
				}
				record("scf-restore-public", bind.Region, fmt.Sprintf("%s functions=%d", strings.Join(bind.Prefixes, ","), len(functions)), "planned", false)
			}
			return result, nil
		}
		record("public-only", "storage", plan.Recommended.SCFGatewayTarget, "planned", false)
		return result, nil
	}

	if opts.RestoreSCFPublic {
		if err := restoreSCFPublic(ctx, cloud, opts, plan, progress, record, &result); err != nil {
			return result, err
		}
		return result, nil
	}

	result.Status = "public_only"
	result.Plan = plan
	result.RecommendedConfig = plan.Recommended
	return result, nil
}

func restoreSCFPublic(
	ctx context.Context,
	cloud Cloud,
	opts Options,
	plan Plan,
	progress func(string, ...any),
	record func(kind, target, detail, status string, created bool),
	result *Result,
) error {
	if result == nil {
		return fmt.Errorf("restore scf public: result is nil")
	}
	publicIP := strings.TrimSpace(plan.Recommended.StoragePublicIP)
	privateIP := strings.TrimSpace(plan.Recommended.StoragePrivateIP)
	if publicIP == "" {
		return fmt.Errorf("restore scf public: storage public ip missing")
	}
	type vpcKey struct {
		region string
		vpcID  string
	}
	seen := map[vpcKey]struct{}{}
	for _, bind := range plan.SCF {
		functions, err := cloud.ListSCF(ctx, bind.Region, bind.Namespace, bind.Prefixes)
		if err != nil {
			return fmt.Errorf("list scf %s: %w", bind.Region, err)
		}
		progress("restore %d scf functions in %s to public storage gateway", len(functions), bind.Region)
		for index, fn := range functions {
			if index == 0 || (index+1)%10 == 0 || index+1 == len(functions) {
				progress("restoring scf %s (%d/%d) in %s", fn.FunctionName, index+1, len(functions), bind.Region)
			}
			current, err := cloud.GetSCF(ctx, bind.Region, fn.Namespace, fn.FunctionName)
			if err != nil {
				return fmt.Errorf("get scf %s: %w", fn.FunctionName, err)
			}
			next := make(map[string]string, len(current.Environment))
			envChanged := false
			for key, value := range current.Environment {
				replaced := replaceIP(value, privateIP, publicIP)
				next[key] = replaced
				if replaced != value {
					envChanged = true
				}
			}
			vpcID, subnetID := current.VpcID, current.SubnetID
			vpcChanged := false
			if opts.UnbindSCFVPC && (vpcID != "" || subnetID != "") {
				if vpcID != "" {
					seen[vpcKey{region: bind.Region, vpcID: vpcID}] = struct{}{}
				}
				vpcID, subnetID = "", ""
				vpcChanged = true
			}
			if !envChanged && !vpcChanged {
				result.SCFAlreadyBound++
				continue
			}
			env := map[string]string(nil)
			if envChanged {
				env = next
			}
			publicNet := firstNonEmpty(bind.PublicNetStatus, current.PublicNetStatus, "ENABLE")
			if err := updateSCFWithRetry(ctx, cloud, bind.Region, fn.Namespace, fn.FunctionName, vpcID, subnetID, publicNet, env, progress); err != nil {
				return fmt.Errorf("update scf %s: %w", fn.FunctionName, err)
			}
			result.SCFUpdated++
			record("scf-restore-public", fn.FunctionName, bind.Region, "updated", false)
		}
	}
	if opts.UnbindSCFVPC {
		hostVPCs := map[vpcKey]struct{}{}
		for _, host := range plan.Hosts {
			if vpcID := strings.TrimSpace(host.Instance.VpcID); vpcID != "" {
				hostVPCs[vpcKey{region: host.Instance.Region, vpcID: vpcID}] = struct{}{}
			}
		}
		for key := range seen {
			if _, isHost := hostVPCs[key]; isHost {
				record("ccn-detach", key.vpcID, key.region+" skipped-host-vpc", "skipped", false)
				continue
			}
			area := tencent.NetworkArea(key.region)
			home := opts.HomeRegion
			name := plan.MainlandCCN
			if area == "overseas" {
				home = "ap-hongkong"
				name = plan.OverseasCCN
			}
			if name == "" {
				continue
			}
			ccn, found, err := cloud.FindCCN(ctx, home, name)
			if err != nil {
				return fmt.Errorf("find ccn %s: %w", name, err)
			}
			if !found || ccn.CcnID == "" {
				continue
			}
			if err := cloud.DetachVPC(ctx, home, ccn.CcnID, key.region, key.vpcID); err != nil {
				return fmt.Errorf("detach vpc %s from ccn: %w", key.vpcID, err)
			}
			record("ccn-detach", key.vpcID, key.region+" "+name, "ok", false)
		}
	}
	result.Plan = plan
	result.RecommendedConfig = plan.Recommended
	result.Status = "scf_public_restored"
	return nil
}

func updateSCFWithRetry(ctx context.Context, cloud Cloud, region, namespace, name, vpcID, subnetID, publicNet string, env map[string]string, progress func(string, ...any)) error {
	var last error
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = cloud.UpdateSCF(ctx, region, namespace, name, vpcID, subnetID, publicNet, env)
		if last == nil {
			return nil
		}
		if !isRetryableSCFUpdate(last) {
			return last
		}
		if progress != nil {
			progress("retry scf %s in %s after %v: %v", name, region, backoff, last)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
	return last
}

func isRetryableSCFUpdate(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "updating") ||
		strings.Contains(text, "请稍后重试") ||
		strings.Contains(text, "requestlimitexceeded") ||
		strings.Contains(text, "limitexceeded") ||
		strings.Contains(text, "resourceinuse")
}
