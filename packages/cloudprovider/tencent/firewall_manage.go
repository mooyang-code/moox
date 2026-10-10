package tencent

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// ListFirewallRules 返回轻量应用服务器（按公网 IP 查找）的全部防火墙规则。
func (c *Client) ListFirewallRules(ctx context.Context, publicIP string) (string, []FirewallRule, error) {
	instanceID, err := c.ResolveInstanceIDByPublicIP(ctx, publicIP)
	if err != nil {
		return "", nil, err
	}
	rules, err := c.describeFirewallRules(ctx, instanceID)
	if err != nil {
		return "", nil, err
	}
	return instanceID, rules, nil
}

type deleteFirewallRulesRequest struct {
	InstanceID    string         `json:"InstanceId"`
	FirewallRules []FirewallRule `json:"FirewallRules"`
}

// DeleteFirewallRules 删除轻量应用服务器的防火墙规则；规则按协议、端口、来源和动作匹配，取自 ListFirewallRules。
func (c *Client) DeleteFirewallRules(ctx context.Context, instanceID string, rules []FirewallRule) error {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return fmt.Errorf("instance id is required")
	}
	if len(rules) == 0 {
		return nil
	}
	var response apiResponse
	return c.do(ctx, "DeleteFirewallRules", deleteFirewallRulesRequest{InstanceID: instanceID, FirewallRules: rules}, &response)
}

// SecurityGroupIngress 是云服务器安全组中的一条入站规则。
type SecurityGroupIngress struct {
	SecurityGroupID string `json:"security_group_id"`
	// PolicyVersion 是读取时安全组规则的版本号，删除时带上它：期间规则被别人改过（序号随之变化）则删除被拒绝，
	// 不会按失效的序号删掉别的规则。
	PolicyVersion string `json:"policy_version"`
	PolicyIndex   int64  `json:"policy_index"`
	Protocol      string `json:"protocol"`
	Port          string `json:"port"`
	CidrBlock     string `json:"cidr_block"`
	Action        string `json:"action"`
	Description   string `json:"description"`
}

type vpcIngressPolicy struct {
	PolicyIndex       int64  `json:"PolicyIndex"`
	Protocol          string `json:"Protocol"`
	Port              string `json:"Port"`
	CidrBlock         string `json:"CidrBlock"`
	Action            string `json:"Action"`
	PolicyDescription string `json:"PolicyDescription"`
}

// ListSecurityGroupIngress 返回云服务器（按公网 IP 查找）所绑定全部安全组的入站规则。
func (c *CVMClient) ListSecurityGroupIngress(ctx context.Context, publicIP string) ([]SecurityGroupIngress, error) {
	groups, err := c.securityGroups(ctx, publicIP)
	if err != nil {
		return nil, err
	}
	var out []SecurityGroupIngress
	for _, groupID := range groups {
		var policies struct {
			Response struct {
				Error                  *apiError `json:"Error,omitempty"`
				SecurityGroupPolicySet struct {
					Version string             `json:"Version"`
					Ingress []vpcIngressPolicy `json:"Ingress"`
				} `json:"SecurityGroupPolicySet"`
			} `json:"Response"`
		}
		if err := c.do(ctx, "vpc", vpcVersion, "DescribeSecurityGroupPolicies", c.endpointFor("vpc"), map[string]any{
			"SecurityGroupId": groupID,
		}, &policies); err != nil {
			return nil, err
		}
		if policies.Response.Error != nil {
			return nil, fmt.Errorf("%s: %s", policies.Response.Error.Code, policies.Response.Error.Message)
		}
		for _, policy := range policies.Response.SecurityGroupPolicySet.Ingress {
			out = append(out, SecurityGroupIngress{
				SecurityGroupID: groupID, PolicyVersion: policies.Response.SecurityGroupPolicySet.Version, PolicyIndex: policy.PolicyIndex, Protocol: policy.Protocol, Port: policy.Port,
				CidrBlock: policy.CidrBlock, Action: policy.Action, Description: policy.PolicyDescription,
			})
		}
	}
	return out, nil
}

// DeleteSecurityGroupIngress 按序号删除一个安全组中的入站规则。序号取自 ListSecurityGroupIngress，同一个安全组的规则
// 一次删完，避免删除后序号变化。
func (c *CVMClient) DeleteSecurityGroupIngress(ctx context.Context, groupID, version string, indexes []int64) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return fmt.Errorf("security group id is required")
	}
	if len(indexes) == 0 {
		return nil
	}
	ingress := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		ingress = append(ingress, map[string]any{"PolicyIndex": index})
	}
	var response struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "DeleteSecurityGroupPolicies", c.endpointFor("vpc"), map[string]any{
		"SecurityGroupId":        groupID,
		"SecurityGroupPolicySet": map[string]any{"Version": version, "Ingress": ingress},
	}, &response); err != nil {
		return err
	}
	if response.Response.Error != nil {
		return fmt.Errorf("%s: %s", response.Response.Error.Code, response.Response.Error.Message)
	}
	return nil
}

// securityGroups 返回云服务器（按公网 IP 查找）绑定的安全组。
func (c *CVMClient) securityGroups(ctx context.Context, publicIP string) ([]string, error) {
	publicIP = strings.TrimSpace(publicIP)
	if net.ParseIP(publicIP) == nil {
		return nil, fmt.Errorf("invalid public ip: %s", publicIP)
	}
	var instances cvmDescribeInstancesResponse
	if err := c.do(ctx, "cvm", cvmVersion, "DescribeInstances", c.endpointFor("cvm"), map[string]any{
		"Filters": []map[string]any{{"Name": "public-ip-address", "Values": []string{publicIP}}}, "Limit": 1,
	}, &instances); err != nil {
		return nil, err
	}
	if instances.Response.Error != nil {
		return nil, fmt.Errorf("%s: %s", instances.Response.Error.Code, instances.Response.Error.Message)
	}
	if len(instances.Response.InstanceSet) == 0 {
		return nil, fmt.Errorf("cvm instance not found for public ip %s", publicIP)
	}
	groups := instances.Response.InstanceSet[0].SecurityGroupIDs
	if len(groups) == 0 {
		return nil, fmt.Errorf("cvm instance has no security group for public ip %s", publicIP)
	}
	return groups, nil
}
