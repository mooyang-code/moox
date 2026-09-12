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
	EnsureCCN(ctx context.Context, homeRegion, name string) (tencent.CCNInfo, bool, error)
	ListCCNAttachments(ctx context.Context, homeRegion, ccnID string) ([]tencent.CCNAttachment, error)
	AttachVPC(ctx context.Context, homeRegion, ccnID, vpcRegion, vpcID string) error
	AcceptCCN(ctx context.Context, homeRegion, ccnID, instanceRegion, instanceType, instanceID string) error
	AttachLighthouseCCN(ctx context.Context, region, ccnID string) error
	DescribeLighthouseCCN(ctx context.Context, region string) ([]tencent.CCNAttachment, error)
	EnsureVpc(ctx context.Context, region, name, cidr string) (tencent.VpcInfo, bool, error)
	EnsureSubnet(ctx context.Context, region, vpcID, name, cidr, zone string) (tencent.SubnetInfo, bool, error)
	DescribeZones(ctx context.Context, region string) ([]string, error)
	EnsureSecurityGroup(ctx context.Context, region string, groupIDs []string, rule tencent.CreateFirewallRulesOptions) (bool, error)
	EnsureLighthouseFirewall(ctx context.Context, region, publicIP string, rule tencent.CreateFirewallRulesOptions) (bool, error)
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
		if plan.NeedMainlandCCN {
			record("ccn", plan.MainlandCCN, "mainland", "planned", false)
		}
		if plan.NeedOverseasCCN {
			record("ccn", plan.OverseasCCN, "overseas", "planned", false)
		}
		for _, vpc := range plan.VPCs {
			record("vpc", vpc.Name, vpc.Region+" "+vpc.CidrBlock, "planned", false)
		}
		for _, bind := range plan.SCF {
			record("scf-vpc", bind.Region, strings.Join(bind.Prefixes, ","), "planned", false)
		}
		return result, nil
	}

	ccnIDs := map[string]string{}
	ensureAreaCCN := func(area, name string, needed bool) error {
		if !needed {
			return nil
		}
		home := opts.HomeRegion
		if area == "overseas" {
			home = "ap-hongkong"
		}
		progress("ensure %s ccn %s", area, name)
		ccn, created, err := cloud.EnsureCCN(ctx, home, name)
		if err != nil {
			return fmt.Errorf("ensure %s ccn: %w", area, err)
		}
		ccnIDs[area] = ccn.CcnID
		record("ccn", ccn.CcnID, name+" "+area, "ok", created)
		return nil
	}
	if err := ensureAreaCCN("mainland", plan.MainlandCCN, plan.NeedMainlandCCN); err != nil {
		return result, err
	}
	if err := ensureAreaCCN("overseas", plan.OverseasCCN, plan.NeedOverseasCCN); err != nil {
		return result, err
	}

	homeForArea := func(area string) string {
		if area == "overseas" {
			return "ap-hongkong"
		}
		return opts.HomeRegion
	}

	createdVPCs := map[string]tencent.VpcInfo{}
	createdSubnets := map[string]tencent.SubnetInfo{}
	for _, vpc := range plan.VPCs {
		progress("ensure vpc %s in %s", vpc.Name, vpc.Region)
		info, created, err := cloud.EnsureVpc(ctx, vpc.Region, vpc.Name, vpc.CidrBlock)
		if err != nil {
			return result, fmt.Errorf("ensure vpc %s: %w", vpc.Name, err)
		}
		createdVPCs[vpc.Region] = info
		record("vpc", info.VpcID, vpc.Region+" "+info.CidrBlock, "ok", created)
		zones, err := cloud.DescribeZones(ctx, vpc.Region)
		if err != nil || len(zones) == 0 {
			return result, fmt.Errorf("describe zones %s: %w", vpc.Region, err)
		}
		subnet, createdSubnet, err := cloud.EnsureSubnet(ctx, vpc.Region, info.VpcID, subnetNameForRegion(vpc.Region), tencent.SubnetCIDRFromVPC(info.CidrBlock), zones[0])
		if err != nil {
			return result, fmt.Errorf("ensure subnet %s: %w", vpc.Name, err)
		}
		createdSubnets[vpc.Region] = subnet
		record("subnet", subnet.SubnetID, vpc.Region+" "+subnet.CidrBlock, "ok", createdSubnet)
		if ccnID := ccnIDs[vpc.Area]; ccnID != "" {
			if err := cloud.AttachVPC(ctx, homeForArea(vpc.Area), ccnID, vpc.Region, info.VpcID); err != nil {
				return result, fmt.Errorf("attach vpc %s to ccn: %w", info.VpcID, err)
			}
			record("ccn-attach", info.VpcID, vpc.Region+" "+vpc.Area, "ok", false)
		}
	}

	attached := map[string]struct{}{}
	for _, host := range plan.Hosts {
		if host.Instance.Kind != tencent.KindCVM || host.Instance.VpcID == "" {
			continue
		}
		ccnID := ccnIDs[host.Area]
		if ccnID == "" {
			continue
		}
		key := host.Area + "/" + host.Instance.Region + "/" + host.Instance.VpcID
		if _, ok := attached[key]; ok {
			continue
		}
		progress("attach vpc %s (%s) to %s ccn", host.Instance.VpcID, host.Instance.Region, host.Area)
		if err := cloud.AttachVPC(ctx, homeForArea(host.Area), ccnID, host.Instance.Region, host.Instance.VpcID); err != nil {
			return result, fmt.Errorf("attach host vpc %s: %w", host.Name, err)
		}
		attached[key] = struct{}{}
		record("ccn-attach", host.Instance.VpcID, host.Name, "ok", false)
	}
	lighthouseRegions := map[string]string{}
	for _, host := range plan.Hosts {
		if host.Instance.Kind == tencent.KindLighthouse && host.Instance.Region != "" {
			lighthouseRegions[host.Instance.Region] = host.Area
		}
	}
	for region, area := range lighthouseRegions {
		ccnID := ccnIDs[area]
		if ccnID == "" {
			continue
		}
		progress("attach lighthouse region %s to %s ccn", region, area)
		if err := cloud.AttachLighthouseCCN(ctx, region, ccnID); err != nil {
			return result, fmt.Errorf("attach lighthouse ccn %s: %w", region, err)
		}
		record("lighthouse-ccn", region, ccnID, "ok", false)
		items, err := cloud.DescribeLighthouseCCN(ctx, region)
		if err != nil {
			return result, fmt.Errorf("describe lighthouse ccn %s: %w", region, err)
		}
		for _, item := range items {
			if item.CcnID != "" && !strings.EqualFold(item.CcnID, ccnID) {
				continue
			}
			if isPendingCCN(item.State) {
				instanceType, instanceID, skip := ccnAcceptIdentity(item)
				if skip {
					continue
				}
				if err := acceptPendingCCN(ctx, cloud, homeForArea(area), ccnID, firstNonEmpty(item.InstanceRegion, region), instanceType, instanceID); err != nil {
					return result, fmt.Errorf("accept lighthouse ccn %s: %w", region, err)
				}
				record("ccn-accept", instanceID, region, "ok", false)
			}
			rememberAttachmentCIDRs(&plan, area, item)
		}
	}
	for area, ccnID := range ccnIDs {
		attachments, err := cloud.ListCCNAttachments(ctx, homeForArea(area), ccnID)
		if err != nil {
			return result, fmt.Errorf("list ccn attachments: %w", err)
		}
		for _, item := range attachments {
			if isPendingCCN(item.State) {
				instanceType, instanceID, skip := ccnAcceptIdentity(item)
				if skip {
					continue
				}
				if err := acceptPendingCCN(ctx, cloud, homeForArea(area), ccnID, item.InstanceRegion, instanceType, instanceID); err != nil {
					return result, fmt.Errorf("accept ccn %s: %w", instanceID, err)
				}
			}
			rememberAttachmentCIDRs(&plan, area, item)
		}
	}

	if !opts.SkipHosts {
		for _, host := range plan.Hosts {
			for _, cidr := range hostAreaCIDRs(plan, host.Area) {
				if cidr == "" || cidr == host.CidrBlock {
					continue
				}
				for _, port := range plan.Ports {
					rule := tencent.CreateFirewallRulesOptions{
						Protocol: "TCP", Ports: port, CidrBlock: cidr, Action: "ACCEPT",
						Description: "MooX private network " + port,
					}
					if host.Instance.Kind == tencent.KindCVM {
						created, err := cloud.EnsureSecurityGroup(ctx, host.Instance.Region, host.Instance.SecurityGroupIDs, rule)
						if err != nil {
							return result, fmt.Errorf("security group %s port %s: %w", host.Name, port, err)
						}
						if created {
							record("security-group", host.Name, port+" "+cidr, "created", true)
						}
						continue
					}
					created, err := cloud.EnsureLighthouseFirewall(ctx, host.Instance.Region, host.Address, rule)
					if err != nil {
						return result, fmt.Errorf("lighthouse firewall %s port %s: %w", host.Name, port, err)
					}
					if created {
						record("lighthouse-firewall", host.Name, port+" "+cidr, "created", true)
					}
				}
			}
		}
	}

	if !opts.SkipSCF {
		for i, bind := range plan.SCF {
			vpcID, subnetID := bind.VpcID, bind.SubnetID
			if vpcID == "" {
				if info, ok := createdVPCs[bind.Region]; ok {
					vpcID = info.VpcID
				}
			}
			if subnetID == "" {
				if info, ok := createdSubnets[bind.Region]; ok {
					subnetID = info.SubnetID
				}
			}
			if vpcID == "" || subnetID == "" {
				return result, fmt.Errorf("scf region %s missing vpc/subnet", bind.Region)
			}
			plan.SCF[i].VpcID = vpcID
			plan.SCF[i].SubnetID = subnetID
			functions, err := cloud.ListSCF(ctx, bind.Region, bind.Namespace, bind.Prefixes)
			if err != nil {
				return result, fmt.Errorf("list scf %s: %w", bind.Region, err)
			}
			progress("bind %d scf functions in %s to %s/%s", len(functions), bind.Region, vpcID, subnetID)
			for index, fn := range functions {
				if index == 0 || (index+1)%10 == 0 || index+1 == len(functions) {
					progress("updating scf %s (%d/%d) in %s", fn.FunctionName, index+1, len(functions), bind.Region)
				}
				current, err := cloud.GetSCF(ctx, bind.Region, fn.Namespace, fn.FunctionName)
				if err != nil {
					return result, fmt.Errorf("get scf %s: %w", fn.FunctionName, err)
				}
				env := map[string]string(nil)
				envChanged := false
				sameAreaAsStorage := plan.Recommended.StorageArea != "" && bind.Area == plan.Recommended.StorageArea
				if opts.UpdateSCFGateway && sameAreaAsStorage && plan.Recommended.StoragePublicIP != "" && plan.Recommended.StoragePrivateIP != "" {
					next := make(map[string]string, len(current.Environment))
					for key, value := range current.Environment {
						replaced := replacePublicIP(value, plan.Recommended.StoragePublicIP, plan.Recommended.StoragePrivateIP)
						next[key] = replaced
						if replaced != value {
							envChanged = true
						}
					}
					if envChanged {
						env = next
					}
				}
				already := strings.EqualFold(current.VpcID, vpcID) && strings.EqualFold(current.SubnetID, subnetID)
				if already && !envChanged {
					result.SCFAlreadyBound++
					continue
				}
				publicNet := bind.PublicNetStatus
				if publicNet == "" {
					publicNet = current.PublicNetStatus
				}
				if err := updateSCFWithRetry(ctx, cloud, bind.Region, fn.Namespace, fn.FunctionName, vpcID, subnetID, publicNet, env, progress); err != nil {
					return result, fmt.Errorf("update scf %s: %w", fn.FunctionName, err)
				}
				result.SCFUpdated++
				record("scf-vpc", fn.FunctionName, bind.Region, "updated", false)
			}
		}
	}

	result.Plan = plan
	result.RecommendedConfig = plan.Recommended
	return result, nil
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

func isPendingCCN(state string) bool {
	switch strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(state), "_", "")) {
	case "PENDING", "APPLYING", "PENDINGACCEPTANCE", "PENDINGACCEPT", "PENDINGAPPLY":
		return true
	default:
		return false
	}
}

func ccnAcceptIdentity(item tencent.CCNAttachment) (instanceType, instanceID string, skip bool) {
	instanceID = strings.TrimSpace(item.InstanceID)
	if instanceID == "" || strings.HasPrefix(strings.ToLower(instanceID), "lhins-") {
		return "", "", true
	}
	instanceType = strings.ToUpper(strings.TrimSpace(item.InstanceType))
	if instanceType == "" || instanceType == "LIGHTHOUSE" {
		instanceType = "VPC"
	}
	return instanceType, instanceID, false
}

func acceptPendingCCN(ctx context.Context, cloud Cloud, home, ccnID, region, instanceType, instanceID string) error {
	var last error
	for _, typ := range uniqueNonEmpty([]string{"VPC", instanceType}) {
		if strings.EqualFold(typ, "LIGHTHOUSE") {
			continue
		}
		err := cloud.AcceptCCN(ctx, home, ccnID, region, typ, instanceID)
		if err == nil {
			return nil
		}
		last = err
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "notpending") || strings.Contains(text, "already") || strings.Contains(text, "已关联") {
			return nil
		}
		if strings.Contains(text, "instancetype") || strings.Contains(text, "invalidparameter") || strings.Contains(text, "resourcenotfound") {
			continue
		}
		return err
	}
	return last
}

func rememberAttachmentCIDRs(plan *Plan, area string, item tencent.CCNAttachment) {
	if plan.CIDRsByArea == nil {
		plan.CIDRsByArea = map[string][]string{}
	}
	add := func(cidr string) {
		if cidr == "" {
			return
		}
		plan.CIDRsByArea[area] = appendUnique(plan.CIDRsByArea[area], cidr)
		plan.CIDRs = appendUnique(plan.CIDRs, cidr)
	}
	add(item.CidrBlock)
	for _, cidr := range item.CidrBlocks {
		add(cidr)
	}
}
