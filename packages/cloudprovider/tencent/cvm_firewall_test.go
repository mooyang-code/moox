package tencent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCVMClient_ExistingBroadSecurityGroupRule(t *testing.T) {
	actions := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("X-TC-Action")
		actions = append(actions, action)
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{"SecurityGroupIds":["sg-test"]}]}}`))
		case "DescribeSecurityGroupPolicies":
			_, _ = w.Write([]byte(`{"Response":{"SecurityGroupPolicySet":{"Ingress":[{"Protocol":"ALL","Port":"ALL","CidrBlock":"0.0.0.0/0","Action":"ACCEPT"}]}}}`))
		default:
			t.Fatalf("unexpected action %s", action)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewCVMClient(ClientOptions{SecretID: "sid", SecretKey: "skey", Region: "ap-hongkong", Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(1700000000, 0) }
	if err := client.EnsureSecurityGroupRule(context.Background(), "43.132.204.177", CreateFirewallRulesOptions{
		Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT",
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := actions, []string{"DescribeInstances", "DescribeSecurityGroupPolicies"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("actions = %v, want %v", got, want)
	}
}

func TestCVMClient_CreatesMissingSecurityGroupRule(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("X-TC-Action") {
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{"SecurityGroupIds":["sg-test"]}]}}`))
		case "DescribeSecurityGroupPolicies":
			_, _ = w.Write([]byte(`{"Response":{"SecurityGroupPolicySet":{"Ingress":[]}}}`))
		case "CreateSecurityGroupPolicies":
			var payload vpcCreatePoliciesRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.SecurityGroupID != "sg-test" || len(payload.SecurityGroupPolicySet.Ingress) != 1 || payload.SecurityGroupPolicySet.Ingress[0].Port != "11003" {
				t.Fatalf("unexpected create payload: %+v", payload)
			}
			created = true
			_, _ = w.Write([]byte(`{"Response":{"RequestId":"req-create"}}`))
		default:
			t.Fatalf("unexpected action %s", r.Header.Get("X-TC-Action"))
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewCVMClient(ClientOptions{SecretID: "sid", SecretKey: "skey", Region: "ap-hongkong", Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(1700000000, 0) }
	if err := client.EnsureSecurityGroupRule(context.Background(), "43.132.204.177", CreateFirewallRulesOptions{
		Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "Storage gateway",
	}); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("CreateSecurityGroupPolicies was not called")
	}
}
