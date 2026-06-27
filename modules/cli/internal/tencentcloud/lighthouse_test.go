package tencentcloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildCreateFirewallRulesRequestUsesDocumentedFields(t *testing.T) {
	req, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
		InstanceID:      "lhins-demo",
		Protocol:        "tcp",
		Ports:           "20201,20200,11000",
		CidrBlock:       "0.0.0.0/0",
		Action:          "accept",
		Description:     "moox services",
		FirewallVersion: 5,
	})
	if err != nil {
		t.Fatalf("NewCreateFirewallRulesRequest returned error: %v", err)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"InstanceId":"lhins-demo"`,
		`"FirewallVersion":5`,
		`"Protocol":"TCP"`,
		`"Port":"20201,20200,11000"`,
		`"CidrBlock":"0.0.0.0/0"`,
		`"Action":"ACCEPT"`,
		`"FirewallRuleDescription":"moox services"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("request JSON %s missing %s", got, want)
		}
	}
}

func TestBuildCreateFirewallRulesRequestRejectsInvalidInput(t *testing.T) {
	for _, opts := range []CreateFirewallRulesOptions{
		{InstanceID: "", Protocol: "TCP", Ports: "20201", CidrBlock: "0.0.0.0/0"},
		{InstanceID: "lhins-demo", Protocol: "BAD", Ports: "20201", CidrBlock: "0.0.0.0/0"},
		{InstanceID: "lhins-demo", Protocol: "TCP", Ports: "", CidrBlock: "0.0.0.0/0"},
		{InstanceID: "lhins-demo", Protocol: "TCP", Ports: "abc", CidrBlock: "0.0.0.0/0"},
		{InstanceID: "lhins-demo", Protocol: "TCP", Ports: "20201", CidrBlock: "0.0.0.0/0", IPv6CidrBlock: "::/0"},
	} {
		if _, err := NewCreateFirewallRulesRequest(opts); err == nil {
			t.Fatalf("NewCreateFirewallRulesRequest(%+v) should fail", opts)
		}
	}
}

func TestClientSendsTencentCloudHeadersAndParsesRequestID(t *testing.T) {
	var gotAction, gotVersion, gotRegion, gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("X-TC-Action")
		gotVersion = r.Header.Get("X-TC-Version")
		gotRegion = r.Header.Get("X-TC-Region")
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"Response":{"RequestId":"req-123"}}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		SecretID:  "sid",
		SecretKey: "skey",
		Region:    "ap-guangzhou",
		Endpoint:  server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	requestID, err := client.CreateFirewallRules(context.Background(), CreateFirewallRulesRequest{
		InstanceID: "lhins-demo",
		FirewallRules: []FirewallRule{{
			Protocol:  "TCP",
			Port:      "20201",
			CidrBlock: "0.0.0.0/0",
			Action:    "ACCEPT",
		}},
	})
	if err != nil {
		t.Fatalf("CreateFirewallRules returned error: %v", err)
	}
	if requestID != "req-123" {
		t.Fatalf("requestID = %q, want req-123", requestID)
	}
	if gotAction != "CreateFirewallRules" || gotVersion != "2020-03-24" || gotRegion != "ap-guangzhou" {
		t.Fatalf("headers action=%q version=%q region=%q", gotAction, gotVersion, gotRegion)
	}
	if !strings.Contains(gotAuth, "TC3-HMAC-SHA256") || !strings.Contains(gotAuth, "Credential=sid/") {
		t.Fatalf("Authorization header not signed as TC3: %s", gotAuth)
	}
	if !strings.Contains(gotBody, `"InstanceId":"lhins-demo"`) || !strings.Contains(gotBody, `"Port":"20201"`) {
		t.Fatalf("unexpected body: %s", gotBody)
	}
}

func TestResolveInstanceIDByPublicIPUsesDescribeInstancesFilter(t *testing.T) {
	var gotAction, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("X-TC-Action")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"Response":{"TotalCount":1,"InstanceSet":[{"InstanceId":"lhins-from-ip","PublicAddresses":["106.53.107.122"]}],"RequestId":"req-describe"}}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		SecretID:  "sid",
		SecretKey: "skey",
		Region:    "ap-guangzhou",
		Endpoint:  server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	instanceID, err := client.ResolveInstanceIDByPublicIP(context.Background(), "106.53.107.122")
	if err != nil {
		t.Fatalf("ResolveInstanceIDByPublicIP returned error: %v", err)
	}
	if instanceID != "lhins-from-ip" {
		t.Fatalf("instanceID = %q, want lhins-from-ip", instanceID)
	}
	if gotAction != "DescribeInstances" {
		t.Fatalf("action = %q, want DescribeInstances", gotAction)
	}
	for _, want := range []string{
		`"Name":"public-ip-address"`,
		`"106.53.107.122"`,
		`"Limit":1`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("DescribeInstances body %s missing %s", gotBody, want)
		}
	}
}
