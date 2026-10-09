package servicecatalog

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const minimalCatalog = `
version: 1
callers:
  - id: console
    description: 控制台
components:
  - id: host-gateway
    name: 主机网关
    binary: moox-host-gateway
    scope: host
    replicas: per_host
    protected: true
    health: {kind: readyz, port: 11012}
    observability: {transport: reporter, functional: not_applicable}
  - id: access
    name: 外部接入
    binary: moox-access
    scope: any
    replicas: multi
    health: {kind: readyz, port: 11014}
    observability: {transport: reporter, functional: not_applicable}
  - id: admin
    name: 管理后台
    binary: moox-admin
    scope: control
    replicas: single
    protected: true
    health: {kind: readyz, port: 11010}
    observability: {transport: reporter, functional: not_applicable}
    services:
      - path: trpc.moox.admin.SpaceMgr
        port: 11107
        console_name: space
        rpcs: [ListSpaces, DeleteSpace]
        read_only: [ListSpaces]
        acl:
          - methods: [ListSpaces]
            callers: [console, collector]
  - id: collector
    name: 行情采集服务
    binary: moox-collector
    scope: any
    replicas: single
    health: {kind: readyz, port: 11412}
    observability: {transport: reporter, functional: not_applicable}
    services:
      - path: trpc.moox.collector.CollectMgr
        port: 11402
        rpcs: [GetTaskList]
        acl:
          - all: true
            callers: [console]
principals:
  - id: scf-collector
    description: SCF
    allow:
      - service: trpc.moox.collector.CollectMgr
        methods: [GetTaskList]
`

func mutateCatalog(t *testing.T, mutate func(*Catalog)) string {
	t.Helper()
	var catalog Catalog
	if err := yaml.Unmarshal([]byte(minimalCatalog), &catalog); err != nil {
		t.Fatal(err)
	}
	mutate(&catalog)
	encoded, err := yaml.Marshal(&catalog)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestMinimalCatalogIsValid(t *testing.T) {
	catalog, err := Parse([]byte(minimalCatalog))
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Allowed("access", "trpc.moox.collector.CollectMgr", "GetTaskList") {
		t.Fatal("外部调用方白名单里的方法应当自动放行 access")
	}
}

func TestCatalogValidationRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{"端口冲突", func(c *Catalog) { c.Components[3].Services[0].Port = 11107 }, "端口 11107"},
		{"健康端口与服务端口冲突", func(c *Catalog) { c.Components[3].Health.Port = 11402 }, "端口 11402"},
		{"未知调用方", func(c *Catalog) { c.Components[2].Services[0].ACL[0].Callers = []string{"nobody"} }, "调用方 \"nobody\" 未定义"},
		{"外部调用方不能写进 ACL", func(c *Catalog) {
			c.Components[2].Services[0].ACL[0].Callers = []string{"scf-collector"}
		}, "调用方 \"scf-collector\" 未定义"},
		{"白名单引用未声明的方法", func(c *Catalog) { c.Principals[0].Allow[0].Methods = []string{"DeleteTask"} }, "未声明的方法 \"DeleteTask\""},
		{"白名单引用未声明的服务", func(c *Catalog) { c.Principals[0].Allow[0].Service = "trpc.moox.x.Y" }, "未声明的服务"},
		{"ACL 引用未声明的方法", func(c *Catalog) { c.Components[2].Services[0].ACL[0].Methods = []string{"Nope"} }, "未声明的方法 \"Nope\""},
		{"readyz 没有端口", func(c *Catalog) { c.Components[3].Health.Port = 0 }, "必须给出健康端口"},
		{"none 带端口", func(c *Catalog) { c.Components[3].Health = Health{Kind: HealthNone, Port: 1} }, "不能声明健康端口"},
		{"console_name 服务没有放行 console", func(c *Catalog) {
			c.Components[2].Services[0].ACL[0].Callers = []string{"collector"}
		}, "没有任何方法放行 console"},
		{"主机组件副本数", func(c *Catalog) { c.Components[0].Replicas = ReplicasSingle }, "副本数必须为 per_host"},
		{"非主机组件副本数", func(c *Catalog) { c.Components[3].Replicas = ReplicasPerHost }, "副本数必须为 single 或 multi"},
		{"组件 ID 重复", func(c *Catalog) { c.Components[3].ID = "admin" }, "重复"},
		{"调用方与组件同名", func(c *Catalog) { c.Callers = append(c.Callers, Caller{ID: "collector"}) }, "重复"},
		{"methods 与 all 同时出现", func(c *Catalog) { c.Components[3].Services[0].ACL[0].Methods = []string{"GetTaskList"} }, "二选一"},
		{"except 不能单独使用", func(c *Catalog) {
			c.Components[2].Services[0].ACL[0].Except = []string{"DeleteSpace"}
		}, "except 只能与 all"},
		{"read_only 引用未声明的方法", func(c *Catalog) { c.Components[2].Services[0].ReadOnly = []string{"Nope"} }, "read_only"},
		{"缺少 console 调用方", func(c *Catalog) { c.Callers = nil }, "console"},
		{"缺少观测方式", func(c *Catalog) { c.Components[3].Observability = Observability{} }, "观测方式"},
		{"业务进度无效", func(c *Catalog) { c.Components[3].Observability.Functional = "later" }, "业务进度"},
		{"只探测的组件没有业务进度", func(c *Catalog) {
			c.Components[3].Observability = Observability{Transport: TransportHealthOnly, Functional: FunctionalActive}
		}, "不能声明业务进度"},
		{"服务重复声明", func(c *Catalog) {
			c.Components[3].Services[0].Path = "trpc.moox.admin.SpaceMgr"
		}, "重复声明"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(mutateCatalog(t, tc.mutate)))
			if err == nil {
				t.Fatalf("期望报错包含 %q，实际通过", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("报错 %q 不包含 %q", err, tc.want)
			}
		})
	}
}

func TestCatalogRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(minimalCatalog + "\nunknown: true\n")); err == nil {
		t.Fatal("未知字段应当报错")
	}
}

func TestConsoleNameMethodsMustBeUnique(t *testing.T) {
	raw := mutateCatalog(t, func(c *Catalog) {
		c.Components[3].Services[0].ConsoleName = "space"
		c.Components[3].Services[0].RPCs = []string{"ListSpaces"}
		c.Components[3].Services[0].ACL[0] = ACLRule{All: true, Callers: []string{"console"}}
		c.Principals = nil
	})
	if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "同时属于") {
		t.Fatalf("同一 console_name 下的同名方法应当报错，实际 %v", err)
	}
}
