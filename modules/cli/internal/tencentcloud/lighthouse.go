package tencentcloud

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
	lighthouseEndpoint = "https://lighthouse.tencentcloudapi.com"
	lighthouseService  = "lighthouse"
	lighthouseVersion  = "2020-03-24"
)

type ClientOptions struct {
	SecretID   string
	SecretKey  string
	Region     string
	Endpoint   string
	HTTPClient *http.Client
}

type Client struct {
	secretID   string
	secretKey  string
	region     string
	endpoint   *url.URL
	httpClient *http.Client
	now        func() time.Time
}

type CreateFirewallRulesOptions struct {
	InstanceID      string
	Protocol        string
	Ports           string
	CidrBlock       string
	IPv6CidrBlock   string
	Action          string
	Description     string
	FirewallVersion int64
}

type CreateFirewallRulesRequest struct {
	InstanceID      string         `json:"InstanceId"`
	FirewallRules   []FirewallRule `json:"FirewallRules"`
	FirewallVersion *int64         `json:"FirewallVersion,omitempty"`
}

type FirewallRule struct {
	Protocol                string `json:"Protocol"`
	Port                    string `json:"Port,omitempty"`
	CidrBlock               string `json:"CidrBlock,omitempty"`
	IPv6CidrBlock           string `json:"Ipv6CidrBlock,omitempty"`
	Action                  string `json:"Action,omitempty"`
	FirewallRuleDescription string `json:"FirewallRuleDescription,omitempty"`
}

type filter struct {
	Name   string   `json:"Name"`
	Values []string `json:"Values"`
}

type describeInstancesRequest struct {
	Filters []filter `json:"Filters,omitempty"`
	Limit   int      `json:"Limit,omitempty"`
}

type apiResponse struct {
	Response responseBody `json:"Response"`
}

type responseBody struct {
	RequestID   string          `json:"RequestId"`
	Error       *apiError       `json:"Error,omitempty"`
	TotalCount  int             `json:"TotalCount,omitempty"`
	InstanceSet []InstanceBrief `json:"InstanceSet,omitempty"`
}

type apiError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type InstanceBrief struct {
	InstanceID      string   `json:"InstanceId"`
	PublicAddresses []string `json:"PublicAddresses"`
}

func NewClient(opts ClientOptions) (*Client, error) {
	if strings.TrimSpace(opts.SecretID) == "" {
		return nil, fmt.Errorf("secret id is required")
	}
	if strings.TrimSpace(opts.SecretKey) == "" {
		return nil, fmt.Errorf("secret key is required")
	}
	region := strings.TrimSpace(opts.Region)
	if region == "" {
		return nil, fmt.Errorf("region is required")
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = lighthouseEndpoint
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("endpoint host is required")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		secretID:   strings.TrimSpace(opts.SecretID),
		secretKey:  strings.TrimSpace(opts.SecretKey),
		region:     region,
		endpoint:   parsed,
		httpClient: httpClient,
		now:        time.Now,
	}, nil
}

func NewCreateFirewallRulesRequest(opts CreateFirewallRulesOptions) (CreateFirewallRulesRequest, error) {
	instanceID := strings.TrimSpace(opts.InstanceID)
	if instanceID == "" {
		return CreateFirewallRulesRequest{}, fmt.Errorf("instance id is required")
	}
	protocol, err := normalizeProtocol(opts.Protocol)
	if err != nil {
		return CreateFirewallRulesRequest{}, err
	}
	action, err := normalizeFirewallAction(opts.Action)
	if err != nil {
		return CreateFirewallRulesRequest{}, err
	}
	cidr := strings.TrimSpace(opts.CidrBlock)
	ipv6Cidr := strings.TrimSpace(opts.IPv6CidrBlock)
	if cidr != "" && ipv6Cidr != "" {
		return CreateFirewallRulesRequest{}, fmt.Errorf("cidr and ipv6 cidr cannot both be set")
	}
	if cidr != "" {
		if _, _, err := net.ParseCIDR(cidr); err != nil && net.ParseIP(cidr) == nil {
			return CreateFirewallRulesRequest{}, fmt.Errorf("invalid cidr: %s", cidr)
		}
	}
	if ipv6Cidr != "" {
		if _, _, err := net.ParseCIDR(ipv6Cidr); err != nil && net.ParseIP(ipv6Cidr) == nil {
			return CreateFirewallRulesRequest{}, fmt.Errorf("invalid ipv6 cidr: %s", ipv6Cidr)
		}
	}
	port := strings.TrimSpace(opts.Ports)
	if err := validateFirewallPort(protocol, port); err != nil {
		return CreateFirewallRulesRequest{}, err
	}
	description := strings.TrimSpace(opts.Description)
	if len([]rune(description)) > 64 {
		return CreateFirewallRulesRequest{}, fmt.Errorf("description length exceeds 64")
	}
	rule := FirewallRule{
		Protocol:                protocol,
		Port:                    port,
		CidrBlock:               cidr,
		IPv6CidrBlock:           ipv6Cidr,
		Action:                  action,
		FirewallRuleDescription: description,
	}
	req := CreateFirewallRulesRequest{
		InstanceID:    instanceID,
		FirewallRules: []FirewallRule{rule},
	}
	if opts.FirewallVersion > 0 {
		version := opts.FirewallVersion
		req.FirewallVersion = &version
	}
	return req, nil
}

func (c *Client) CreateFirewallRules(ctx context.Context, req CreateFirewallRulesRequest) (string, error) {
	var resp apiResponse
	if err := c.do(ctx, "CreateFirewallRules", req, &resp); err != nil {
		return "", err
	}
	return resp.Response.RequestID, nil
}

func (c *Client) ResolveInstanceIDByPublicIP(ctx context.Context, publicIP string) (string, error) {
	publicIP = strings.TrimSpace(publicIP)
	if net.ParseIP(publicIP) == nil {
		return "", fmt.Errorf("invalid public ip: %s", publicIP)
	}
	req := describeInstancesRequest{
		Filters: []filter{{
			Name:   "public-ip-address",
			Values: []string{publicIP},
		}},
		Limit: 1,
	}
	var resp apiResponse
	if err := c.do(ctx, "DescribeInstances", req, &resp); err != nil {
		return "", err
	}
	if len(resp.Response.InstanceSet) == 0 || resp.Response.InstanceSet[0].InstanceID == "" {
		return "", fmt.Errorf("lighthouse instance not found for public ip %s", publicIP)
	}
	return resp.Response.InstanceSet[0].InstanceID, nil
}

func (c *Client) do(ctx context.Context, action string, payload any, out *apiResponse) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	timestamp := c.now().Unix()
	c.sign(req, action, timestamp, body)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s failed: %w", action, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request %s returned HTTP %s: %s", action, resp.Status, string(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s response: %w", action, err)
	}
	if out.Response.Error != nil {
		return fmt.Errorf("%s: %s", out.Response.Error.Code, out.Response.Error.Message)
	}
	return nil
}

func (c *Client) sign(req *http.Request, action string, timestamp int64, payload []byte) {
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	hashedPayload := sha256Hex(payload)
	canonicalHeaders := fmt.Sprintf("content-type:application/json\nhost:%s\n", c.endpoint.Host)
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		canonicalHeaders,
		"content-type;host",
		hashedPayload,
	}, "\n")
	hashedCanonicalRequest := sha256Hex([]byte(canonicalRequest))
	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, lighthouseService)
	stringToSign := strings.Join([]string{
		"TC3-HMAC-SHA256",
		strconv.FormatInt(timestamp, 10),
		credentialScope,
		hashedCanonicalRequest,
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(deriveSigningKey(c.secretKey, date), stringToSign))
	authorization := fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host, Signature=%s",
		c.secretID, credentialScope, signature)

	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", c.endpoint.Host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-TC-Version", lighthouseVersion)
	req.Header.Set("X-TC-Region", c.region)
}

func normalizeProtocol(protocol string) (string, error) {
	protocol = strings.ToUpper(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "TCP"
	}
	switch protocol {
	case "TCP", "UDP", "ICMP", "ICMPV6", "ALL":
		if protocol == "ICMPV6" {
			return "ICMPv6", nil
		}
		return protocol, nil
	default:
		return "", fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func normalizeFirewallAction(action string) (string, error) {
	action = strings.ToUpper(strings.TrimSpace(action))
	if action == "" {
		action = "ACCEPT"
	}
	switch action {
	case "ACCEPT", "DROP":
		return action, nil
	default:
		return "", fmt.Errorf("unsupported action: %s", action)
	}
}

func validateFirewallPort(protocol, port string) error {
	if protocol != "TCP" && protocol != "UDP" {
		if port == "" || strings.EqualFold(port, "ALL") {
			return nil
		}
		return fmt.Errorf("port can only be empty or ALL when protocol is %s", protocol)
	}
	if port == "" {
		return fmt.Errorf("ports are required for %s", protocol)
	}
	if len(port) > 64 {
		return fmt.Errorf("ports length exceeds 64")
	}
	if strings.EqualFold(port, "ALL") {
		return nil
	}
	if strings.Contains(port, ",") {
		for _, item := range strings.Split(port, ",") {
			if err := validateSinglePort(item); err != nil {
				return err
			}
		}
		return nil
	}
	if strings.Contains(port, "-") {
		parts := strings.Split(port, "-")
		if len(parts) != 2 {
			return fmt.Errorf("invalid port range: %s", port)
		}
		start, err := parsePort(parts[0])
		if err != nil {
			return err
		}
		end, err := parsePort(parts[1])
		if err != nil {
			return err
		}
		if start >= end {
			return fmt.Errorf("invalid port range: %s", port)
		}
		return nil
	}
	return validateSinglePort(port)
}

func validateSinglePort(raw string) error {
	_, err := parsePort(raw)
	return err
}

func parsePort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port: %s", raw)
	}
	return port, nil
}

func deriveSigningKey(secretKey, date string) []byte {
	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, lighthouseService)
	return hmacSHA256(secretService, "tc3_request")
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(msg))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
