package tencent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	cvmEndpoint = "https://cvm.tencentcloudapi.com"
	vpcEndpoint = "https://vpc.tencentcloudapi.com"
	cvmVersion  = "2017-03-12"
	vpcVersion  = "2017-03-12"
)

// CVMClient handles the Tencent CVM/VPC APIs needed for hosts that are not
// Lighthouse instances. A Tencent CVM uses VPC security groups rather than
// Lighthouse firewall rules.
type CVMClient struct {
	secretID   string
	secretKey  string
	region     string
	endpoint   string
	httpClient *http.Client
	now        func() time.Time
}

func NewCVMClient(opts ClientOptions) (*CVMClient, error) {
	if strings.TrimSpace(opts.SecretID) == "" {
		return nil, fmt.Errorf("secret id is required")
	}
	if strings.TrimSpace(opts.SecretKey) == "" {
		return nil, fmt.Errorf("secret key is required")
	}
	if strings.TrimSpace(opts.Region) == "" {
		return nil, fmt.Errorf("region is required")
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = cvmEndpoint
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CVMClient{
		secretID: strings.TrimSpace(opts.SecretID), secretKey: strings.TrimSpace(opts.SecretKey),
		region: strings.TrimSpace(opts.Region), endpoint: endpoint, httpClient: httpClient, now: time.Now,
	}, nil
}

type cvmDescribeInstancesResponse struct {
	Response struct {
		Error       *apiError `json:"Error,omitempty"`
		InstanceSet []struct {
			SecurityGroupIDs []string `json:"SecurityGroupIds"`
		} `json:"InstanceSet"`
	} `json:"Response"`
}

type vpcDescribePoliciesResponse struct {
	Response struct {
		Error                  *apiError `json:"Error,omitempty"`
		SecurityGroupPolicySet struct {
			Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
		} `json:"SecurityGroupPolicySet"`
	} `json:"Response"`
}

type vpcSecurityGroupPolicy struct {
	Protocol          string `json:"Protocol"`
	Port              string `json:"Port"`
	CidrBlock         string `json:"CidrBlock"`
	Action            string `json:"Action"`
	PolicyDescription string `json:"PolicyDescription"`
}

type vpcCreatePoliciesRequest struct {
	SecurityGroupID        string `json:"SecurityGroupId"`
	SecurityGroupPolicySet struct {
		Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
	} `json:"SecurityGroupPolicySet"`
}

// EnsureSecurityGroupRule makes the requested TCP/UDP ingress available on a
// CVM's VPC security group. Existing broad ACCEPT rules are also accepted as
// satisfying the request.
func (c *CVMClient) EnsureSecurityGroupRule(ctx context.Context, publicIP string, opts CreateFirewallRulesOptions) error {
	publicIP = strings.TrimSpace(publicIP)
	if net.ParseIP(publicIP) == nil {
		return fmt.Errorf("invalid public ip: %s", publicIP)
	}
	rule, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
		InstanceID:    "cvm",
		Protocol:      opts.Protocol,
		Ports:         opts.Ports,
		CidrBlock:     opts.CidrBlock,
		IPv6CidrBlock: opts.IPv6CidrBlock,
		Action:        opts.Action,
		Description:   opts.Description,
	})
	if err != nil {
		return err
	}
	wanted := vpcSecurityGroupPolicy{
		Protocol: rule.FirewallRules[0].Protocol, Port: rule.FirewallRules[0].Port,
		CidrBlock: rule.FirewallRules[0].CidrBlock, Action: rule.FirewallRules[0].Action,
		PolicyDescription: rule.FirewallRules[0].FirewallRuleDescription,
	}
	var instances cvmDescribeInstancesResponse
	if err := c.do(ctx, "cvm", cvmVersion, "DescribeInstances", c.endpointFor("cvm"), map[string]any{
		"Filters": []map[string]any{{"Name": "public-ip-address", "Values": []string{publicIP}}}, "Limit": 1,
	}, &instances); err != nil {
		return err
	}
	if instances.Response.Error != nil {
		return fmt.Errorf("%s: %s", instances.Response.Error.Code, instances.Response.Error.Message)
	}
	if len(instances.Response.InstanceSet) == 0 {
		return fmt.Errorf("cvm instance not found for public ip %s", publicIP)
	}
	groups := instances.Response.InstanceSet[0].SecurityGroupIDs
	if len(groups) == 0 {
		return fmt.Errorf("cvm instance has no security group for public ip %s", publicIP)
	}
	for _, groupID := range groups {
		var policies vpcDescribePoliciesResponse
		if err := c.do(ctx, "vpc", vpcVersion, "DescribeSecurityGroupPolicies", c.endpointFor("vpc"), map[string]any{
			"SecurityGroupId": groupID,
		}, &policies); err != nil {
			return err
		}
		if policies.Response.Error != nil {
			return fmt.Errorf("%s: %s", policies.Response.Error.Code, policies.Response.Error.Message)
		}
		for _, existing := range policies.Response.SecurityGroupPolicySet.Ingress {
			if coversCVMRule(existing, wanted) {
				return nil
			}
		}
	}
	// All groups are evaluated above. Adding to the first group is sufficient
	// for the common single-group topology and avoids silently duplicating a
	// rule across every attached group.
	groupID := groups[0]
	var created struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "CreateSecurityGroupPolicies", c.endpointFor("vpc"), vpcCreatePoliciesRequest{
		SecurityGroupID: groupID,
		SecurityGroupPolicySet: struct {
			Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
		}{Ingress: []vpcSecurityGroupPolicy{wanted}},
	}, &created); err != nil {
		return err
	}
	if created.Response.Error != nil {
		return fmt.Errorf("%s: %s", created.Response.Error.Code, created.Response.Error.Message)
	}
	return nil
}

func coversCVMRule(existing, wanted vpcSecurityGroupPolicy) bool {
	if !strings.EqualFold(strings.TrimSpace(existing.Action), strings.TrimSpace(wanted.Action)) {
		return false
	}
	protocol := strings.ToUpper(strings.TrimSpace(existing.Protocol))
	if protocol != strings.ToUpper(strings.TrimSpace(wanted.Protocol)) && protocol != "ALL" {
		return false
	}
	port := strings.TrimSpace(existing.Port)
	if port != strings.TrimSpace(wanted.Port) && !strings.EqualFold(port, "ALL") {
		return false
	}
	return strings.TrimSpace(existing.CidrBlock) == strings.TrimSpace(wanted.CidrBlock)
}

func (c *CVMClient) endpointFor(service string) string {
	if c.endpoint != cvmEndpoint {
		return c.endpoint
	}
	if service == "vpc" {
		return vpcEndpoint
	}
	return cvmEndpoint
}

func (c *CVMClient) do(ctx context.Context, service, version, action, endpoint string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	timestamp := c.now().Unix()
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	hashedBody := sha256HexCVM(body)
	canonical := strings.Join([]string{
		"POST", "/", "", "content-type:application/json\nhost:" + parsed.Host + "\n", "content-type;host", hashedBody,
	}, "\n")
	scope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	stringToSign := strings.Join([]string{"TC3-HMAC-SHA256", strconv.FormatInt(timestamp, 10), scope, sha256HexCVM([]byte(canonical))}, "\n")
	key := hmacCVM([]byte("TC3"+c.secretKey), date)
	key = hmacCVM(key, service)
	key = hmacCVM(key, "tc3_request")
	signature := hex.EncodeToString(hmacCVM(key, stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host, Signature=%s", c.secretID, scope, signature))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", parsed.Host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Region", c.region)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s failed: %w", action, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s response: %w", action, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request %s returned HTTP %s: %s", action, resp.Status, string(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", action, err)
	}
	return nil
}

func sha256HexCVM(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacCVM(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}
