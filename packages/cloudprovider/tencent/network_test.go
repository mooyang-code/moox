package tencent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestNetworkClient(t *testing.T, handler http.HandlerFunc) *NetworkClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewNetworkClient(ClientOptions{
		SecretID: "sid", SecretKey: "skey", Region: "ap-hongkong",
		Endpoint: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(1700000000, 0) }
	return client
}

func TestNetworkClientLookupCVM(t *testing.T) {
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TC-Action") != "DescribeInstances" {
			t.Fatalf("action = %s", r.Header.Get("X-TC-Action"))
		}
		_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{
			"InstanceId":"ins-storage","InstanceName":"storage",
			"PrivateIpAddresses":["10.1.2.8"],"PublicIpAddresses":["146.56.196.204"],
			"SecurityGroupIds":["sg-storage"],
			"Placement":{"Zone":"ap-hongkong-2"},
			"VirtualPrivateCloud":{"VpcId":"vpc-storage","SubnetId":"subnet-storage","PrivateIpAddresses":["10.1.2.8"]}
		}]}}`))
	})
	got, found, err := client.LookupCVM(context.Background(), "146.56.196.204")
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.InstanceID != "ins-storage" || got.VpcID != "vpc-storage" || got.PrivateIPs[0] != "10.1.2.8" {
		t.Fatalf("instance = %+v", got)
	}
}

func TestNetworkClientEnsureCCNReusesExisting(t *testing.T) {
	actions := make([]string, 0, 2)
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("X-TC-Action")
		actions = append(actions, action)
		switch action {
		case "DescribeCcns":
			_, _ = w.Write([]byte(`{"Response":{"CcnSet":[{"CcnId":"ccn-moox","CcnName":"moox-private-network","State":"AVAILABLE"}]}}`))
		default:
			t.Fatalf("unexpected action %s", action)
		}
	})
	info, created, err := client.EnsureCCN(context.Background(), "moox-private-network")
	if err != nil {
		t.Fatal(err)
	}
	if created || info.CcnID != "ccn-moox" {
		t.Fatalf("created=%v info=%+v", created, info)
	}
	if len(actions) != 1 || actions[0] != "DescribeCcns" {
		t.Fatalf("actions = %v", actions)
	}
}

func TestNetworkClientAttachVPCToCCNTreatsAlreadyAttachedAsSuccess(t *testing.T) {
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"Error":{"Code":"UnsupportedOperation.CcnAttached","Message":"already attached"}}}`))
	})
	if err := client.AttachVPCToCCN(context.Background(), "ccn-moox", "ap-hongkong", "vpc-storage"); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkClientListSCFFunctionsFiltersPrefix(t *testing.T) {
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"Functions":[
			{"FunctionName":"moox-fetcher-stockcn-ap-hongkong-0","Status":"Active","Namespace":"default"},
			{"FunctionName":"other-fn","Status":"Active","Namespace":"default"}
		]}}`))
	})
	got, err := client.ListSCFFunctions(context.Background(), "default", []string{"moox-fetcher-stockcn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FunctionName != "moox-fetcher-stockcn-ap-hongkong-0" {
		t.Fatalf("functions = %+v", got)
	}
}

func TestNetworkClientUpdateSCFNetworkSendsVpcAndPublicNet(t *testing.T) {
	var payload map[string]any
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TC-Action") != "UpdateFunctionConfiguration" {
			t.Fatalf("action = %s", r.Header.Get("X-TC-Action"))
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"Response":{"RequestId":"req-scf"}}`))
	})
	if err := client.UpdateSCFNetwork(context.Background(), "default", "moox-fetcher-1", "vpc-scf", "subnet-scf", "ENABLE", nil); err != nil {
		t.Fatal(err)
	}
	vpc, _ := payload["VpcConfig"].(map[string]any)
	if vpc["VpcId"] != "vpc-scf" || vpc["SubnetId"] != "subnet-scf" {
		t.Fatalf("vpc payload = %#v", payload["VpcConfig"])
	}
	if _, ok := payload["Environment"]; ok {
		t.Fatal("environment must be omitted when not requested")
	}
}

func TestNetworkClientInvokeSCF(t *testing.T) {
	var payload map[string]any
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TC-Action") != "Invoke" {
			t.Fatalf("action = %s", r.Header.Get("X-TC-Action"))
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"Response":{"Result":{"success":true},"Log":"ok","FunctionRequestId":"req-1"}}`))
	})
	got, err := client.InvokeSCF(context.Background(), "default", "moox-fetcher-stockcn-ap-guangzhou-0", map[string]any{"action": "market_fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "req-1" || got.Result != `{"success":true}` {
		t.Fatalf("invoke = %+v", got)
	}
	if payload["FunctionName"] != "moox-fetcher-stockcn-ap-guangzhou-0" || payload["LogType"] != "Tail" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestNetworkClientLookupCVMByEIP(t *testing.T) {
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-TC-Action") {
		case "DescribeAddresses":
			_, _ = w.Write([]byte(`{"Response":{"AddressSet":[{"AddressIp":"146.56.196.204","InstanceId":"ins-storage"}]}}`))
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{
				"InstanceId":"ins-storage","PrivateIpAddresses":["10.1.2.8"],
				"VirtualPrivateCloud":{"VpcId":"vpc-storage","SubnetId":"subnet-storage"}
			}]}}`))
		default:
			t.Fatalf("action = %s", r.Header.Get("X-TC-Action"))
		}
	})
	got, found, err := client.LookupCVMByEIP(context.Background(), "146.56.196.204")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.InstanceID != "ins-storage" || got.VpcID != "vpc-storage" {
		t.Fatalf("instance = %+v", got)
	}
}

func TestFlexibleCIDRsUnmarshalsStringOrArray(t *testing.T) {
	var asString flexibleCIDRs
	if err := json.Unmarshal([]byte(`"10.1.0.0/16"`), &asString); err != nil {
		t.Fatal(err)
	}
	if asString.first != "10.1.0.0/16" || len(asString.all) != 1 {
		t.Fatalf("string cidr = %+v", asString)
	}
	var asArray flexibleCIDRs
	if err := json.Unmarshal([]byte(`["10.1.0.0/16","10.2.0.0/16"]`), &asArray); err != nil {
		t.Fatal(err)
	}
	if asArray.first != "10.1.0.0/16" || len(asArray.all) != 2 {
		t.Fatalf("array cidr = %+v", asArray)
	}
	client := newTestNetworkClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{
			"CcnId":"ccn-1","InstanceId":"vpc-lh","InstanceRegion":"ap-guangzhou",
			"InstanceType":"VPC","CidrBlock":["10.1.0.0/16"],"State":"PENDING"
		}]}}`))
	})
	got, err := client.ListCCNAttachments(context.Background(), "ccn-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].InstanceID != "vpc-lh" || got[0].CidrBlock != "10.1.0.0/16" {
		t.Fatalf("attachments = %+v", got)
	}
}

func TestCIDRHelpers(t *testing.T) {
	if !CIDRsOverlap("10.80.0.0/16", "10.80.1.0/24") {
		t.Fatal("expected overlap")
	}
	got, err := FirstNonOverlappingCIDR("10.80.0.0/16", []string{"10.80.0.0/16"}, 80, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.81.0.0/16" {
		t.Fatalf("cidr = %s", got)
	}
	if SubnetCIDRFromVPC("10.85.0.0/16") != "10.85.0.0/20" {
		t.Fatalf("subnet = %s", SubnetCIDRFromVPC("10.85.0.0/16"))
	}
	if InferPrivateCIDR("10.1.2.8") != "10.1.0.0/16" {
		t.Fatalf("infer = %s", InferPrivateCIDR("10.1.2.8"))
	}
}

func TestLighthouseLookupInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(apiResponse{Response: responseBody{
			InstanceSet: []InstanceBrief{{
				InstanceID: "lhins-control", InstanceName: "control", Zone: "ap-guangzhou-3",
				PublicAddresses: []string{"106.53.107.122"}, PrivateAddresses: []string{"10.0.1.12"},
			}},
		}})
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(ClientOptions{SecretID: "sid", SecretKey: "skey", Region: "ap-guangzhou", Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Unix(1700000000, 0) }
	got, found, err := client.LookupInstance(context.Background(), "106.53.107.122")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.Kind != KindLighthouse || got.PrivateIPs[0] != "10.0.1.12" {
		t.Fatalf("instance = %+v", got)
	}
}
