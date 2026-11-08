package servicecatalog

import (
	"slices"
	"testing"
)

const secretMgr = "trpc.moox.ops.SecretMgr"

// Collector 读币安现货下架计划只需要只读 Key 的公开标识：它只被放行专用方法，拿不到 GetSecretValue。
// 网关转发会丢掉调用方元数据，GetSecretValue 没法按调用方限定，所以必须靠“按方法授权”。
func TestCollectorOnlyReadsReadonlyExchangeKeyIdentifier(t *testing.T) {
	c := mustCatalog(t)
	if !c.Allowed("collector", secretMgr, "GetReadonlyExchangeKey") {
		t.Fatal("collector must read the readonly exchange key identifier")
	}
	for _, method := range []string{"GetSecretValue", "GetSecret", "ListSecrets", "CreateSecret", "UpdateSecret", "DeleteSecret", "ToggleSecretStatus", "ExportSecrets", "ImportSecrets"} {
		if c.Allowed("collector", secretMgr, method) {
			t.Fatalf("collector must not call SecretMgr.%s", method)
		}
	}

	service, ok := c.Service(secretMgr)
	if !ok {
		t.Fatal("SecretMgr is not declared")
	}
	if !slices.Contains(service.ReadOnlyMethods, "GetReadonlyExchangeKey") {
		t.Fatal("GetReadonlyExchangeKey is read only")
	}
	var callers []string
	for _, grant := range service.ACL {
		if slices.Contains(grant.Methods, "GetReadonlyExchangeKey") {
			callers = append(callers, grant.Callers...)
		}
	}
	if !slices.Equal(callers, []string{"collector"}) {
		t.Fatalf("GetReadonlyExchangeKey callers = %v, want only collector", callers)
	}
	// 其他调用方（含 Access 对外身份与外部程序）都不能调用。
	for _, caller := range []string{"console", "moox-cli", "trade", "cloudfunction", "access", "scf-collector", "collector-stream", "monitor"} {
		if c.Allowed(caller, secretMgr, "GetReadonlyExchangeKey") {
			t.Fatalf("unexpected GetReadonlyExchangeKey grant: %s", caller)
		}
	}
}
