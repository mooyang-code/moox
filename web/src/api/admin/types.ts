export type { Page, PageResult } from "@/api/page";

export type ControlResponse<T> = T & {
  ret_info: {
    code?: number;
    msg?: string;
  };
};

export type DeploymentStatus = "enabled" | "disabled";
export interface DeploymentHost {
  host_id: string;
  /** 中文名，如“控制机”；没有时回退显示主机 ID */
  label?: string;
  /** 控制台终端使用的 SSH 端口与用户；端口为 0（或缺省）表示控制台不能 SSH 到这台主机 */
  ssh_port?: number;
  ssh_user?: string;
  address: string;
  private_address?: string;
  region?: string;
  status: DeploymentStatus;
  /** direct：可直达；outbound：只能出站，经控制主机网关反向连接中转 */
  reachability?: "direct" | "outbound";
  /** 这台主机的网关是否开外部入口（11004），云函数、Skill 等外部身份从这里进来 */
  external_entry?: boolean;
  description?: string;
  /** 创建、更新时刻：Unix 毫秒的数字字符串 */
  created_at?: string;
  updated_at?: string;
}
export interface ComponentDeployment {
  host_id: string;
  component_id: string;
  status: DeploymentStatus;
  /** 创建、更新时刻：Unix 毫秒的数字字符串 */
  created_at?: string;
  updated_at?: string;
}
/** 服务目录里的一个方法：callers 是后端展开后的全部调用方（含人操作身份与机器身份）。 */
export interface CatalogMethod {
  name: string;
  read_only?: boolean;
  idempotent?: boolean;
  timeout_ms?: number | string;
  callers?: string[];
}
export interface CatalogService {
  path: string;
  port: number;
  methods?: CatalogMethod[];
  timeout_ms?: number | string;
  max_body_bytes?: number | string;
  console_name?: string;
}
export interface CatalogComponent {
  id: string;
  name: string;
  binary: string;
  scope: "host" | "control" | "any";
  /** 外部程序（因子引擎、归档 worker）：由 systemd 用户服务托管，不经主机网关路由 */
  external_program?: boolean;
  ports?: number[];
  /** 健康端口只听本机（/healthz、/readyz、/metrics），组件健康看上报 */
  health_port?: number;
  services?: CatalogService[];
}
export interface ComponentCatalog {
  version: number;
  /** 不对应部署的调用身份；external 为真的从入口主机网关的外部入口（11004）进入。 */
  identities?: { id: string; external?: boolean }[];
  components?: CatalogComponent[];
}
export interface CatalogResponse {
  catalog_yaml?: string;
  sha256: string;
  control_host_id: string;
  catalog?: ComponentCatalog;
}
/** 网关最近一次拉取快照时 Admin 记下的状态（拉取约每 5 秒一次，兼作心跳）。 */
export interface HostGatewayRuntimeStatus {
  expected_hash?: string;
  applied_hash?: string;
  /** 最近一次拉取的时刻：Unix 毫秒的数字字符串，没拉取过为 "0" */
  last_seen_at?: string;
}
export interface HostGatewayRoute {
  component_id: string;
  service_path: string;
  method: string;
  address: string;
  timeout_ms?: number | string;
  max_body_bytes?: number | string;
  callers?: string[];
  read_only?: boolean;
}
export interface HostRoutesResponse {
  host_id: string;
  routes?: HostGatewayRoute[];
  gateway_status?: HostGatewayRuntimeStatus;
  snapshot_schema_version: number;
  /** 这次响应计算的时刻：Unix 毫秒的数字字符串 */
  compiled_at: string;
}
