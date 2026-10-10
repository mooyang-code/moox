package tencent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 完全一致的已有规则满足请求，不再创建；"全部协议、全部端口"的放行规则不算（见下一个测试）。
func TestCVMClient_ExistingIdenticalSecurityGroupRule(t *testing.T) {
	actions := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("X-TC-Action")
		actions = append(actions, action)
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{"SecurityGroupIds":["sg-test"]}]}}`))
		case "DescribeSecurityGroupPolicies":
			_, _ = w.Write([]byte(`{"Response":{"SecurityGroupPolicySet":{"Ingress":[{"Protocol":"TCP","Port":"11003","CidrBlock":"0.0.0.0/0","Action":"ACCEPT"}]}}}`))
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

// 安全组默认的"全部协议、全部端口、任意来源"放行规则不能算满足具体规则：否则具体规则永远建不出来，
// 这条放行一切的规则也就关不掉。
func TestCVMClient_AllowAllRuleDoesNotSatisfySpecificRule(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("X-TC-Action") {
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{"SecurityGroupIds":["sg-test"]}]}}`))
		case "DescribeSecurityGroupPolicies":
			_, _ = w.Write([]byte(`{"Response":{"SecurityGroupPolicySet":{"Ingress":[{"Protocol":"ALL","Port":"ALL","CidrBlock":"0.0.0.0/0","Action":"ACCEPT"}]}}}`))
		case "CreateSecurityGroupPolicies":
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
		Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT",
	}); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("放行一切的规则不应当让具体规则被跳过")
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
		Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "主机网关",
	}); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("CreateSecurityGroupPolicies was not called")
	}
}

// 删除安全组规则时带上读取时的版本号：期间规则被别人改过则删除被拒绝，不会按失效的序号删掉别的规则。
func TestCVMClient_DeleteSecurityGroupIngressCarriesPolicyVersion(t *testing.T) {
	var deleted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("X-TC-Action") {
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{"SecurityGroupIds":["sg-test"]}]}}`))
		case "DescribeSecurityGroupPolicies":
			_, _ = w.Write([]byte(`{"Response":{"SecurityGroupPolicySet":{"Version":"7","Ingress":[{"PolicyIndex":3,"Protocol":"TCP","Port":"11003","CidrBlock":"1.2.3.4/32","Action":"ACCEPT"}]}}}`))
		case "DeleteSecurityGroupPolicies":
			if err := json.NewDecoder(r.Body).Decode(&deleted); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"Response":{"RequestId":"req-delete"}}`))
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
	policies, err := client.ListSecurityGroupIngress(context.Background(), "43.132.204.177")
	if err != nil || len(policies) != 1 || policies[0].PolicyVersion != "7" {
		t.Fatalf("policies = %+v, err = %v", policies, err)
	}
	if err := client.DeleteSecurityGroupIngress(context.Background(), policies[0].SecurityGroupID, policies[0].PolicyVersion, []int64{policies[0].PolicyIndex}); err != nil {
		t.Fatal(err)
	}
	set, _ := deleted["SecurityGroupPolicySet"].(map[string]any)
	if set["Version"] != "7" {
		t.Fatalf("删除请求没有带版本号: %+v", deleted)
	}
}
