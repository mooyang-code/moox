package client

import (
	"context"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

const (
	testKeyID  = "AKIDobjectstore0001"
	testSecret = "ObjectStoreSecretValue-DoNotLeak"
)

func TestObjectStoreSecretReadsAnActiveS3Secret(t *testing.T) {
	var asked string
	material := &pb.SecretMaterial{SecretId: "secret_s3", Category: "other", Provider: "S3", Status: "active", KeyId: testKeyID, SecretValue: testSecret}
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		asked = service + "/" + method + "/" + req.(*pb.GetSecretValueReq).GetSecretId()
		out := rsp.(*pb.GetSecretValueRsp)
		out.RetInfo, out.Secret = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, material
		return nil
	})
	cred, err := New(gateway).ObjectStoreSecret(t.Context(), "secret_s3")
	if err != nil || cred.AccessKeyID != testKeyID || cred.SecretAccessKey != testSecret {
		t.Fatalf("cred=%+v err=%v", cred, err)
	}
	if asked != "trpc.moox.ops.SecretMgr/GetSecretValue/secret_s3" {
		t.Fatalf("调用 = %s", asked)
	}
	if string(cred.Env()) != "ACCESS_KEY_ID="+testKeyID+"\nSECRET_ACCESS_KEY="+testSecret+"\n" {
		t.Fatalf("env = %q", cred.Env())
	}
}

func TestObjectStoreSecretRejectsTheWrongKindWithoutLeakingValues(t *testing.T) {
	for name, mutate := range map[string]func(*pb.SecretMaterial){
		"提供方不对": func(m *pb.SecretMaterial) { m.Provider = "tencent" },
		"未启用":   func(m *pb.SecretMaterial) { m.Status = "inactive" },
		"标识为空":  func(m *pb.SecretMaterial) { m.KeyId = "" },
		"值含空白":  func(m *pb.SecretMaterial) { m.SecretValue = testSecret + " x" },
	} {
		m := &pb.SecretMaterial{Provider: "s3", Status: "active", KeyId: testKeyID, SecretValue: testSecret}
		mutate(m)
		_, err := objectStoreCredential("secret_s3", m)
		if err == nil {
			t.Fatalf("%s：应被拒", name)
		}
		if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testKeyID) {
			t.Fatalf("%s：错误里出现了密钥值: %v", name, err)
		}
	}
	if _, err := New(topologyInvoker(func(context.Context, string, string, any, any) error { return nil })).ObjectStoreSecret(t.Context(), ""); err == nil {
		t.Fatal("secret_id 为空应报错")
	}
}
