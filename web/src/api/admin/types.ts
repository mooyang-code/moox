export type ControlResponse<T> = T & {
  ret_info: {
    code?: number;
    msg?: string;
  };
};

export interface PageReq {
  page?: number;
  size?: number;
}

export interface PageResult {
  page: number;
  size: number;
  total: number;
  has_more?: boolean;
}

export interface Space {
  space_id: string;
  name: string;
  description?: string;
  owner?: string;
  market?: string;
  timezone?: string;
  status: string;
  attributes?: Record<string, string> | string;
  created_at?: string;
  updated_at?: string;
}

/** int64 字段在 JSON 中可能是字符串。 */
export type WireInt64 = number | string;

/** 组件目录（packages/servicecatalog）中的端口：不经主机网关路由，只用于展示和端口冲突校验。 */
export interface CatalogPort {
  name?: string;
  port?: number;
}

export interface CatalogMethod {
  name?: string;
  read_only?: boolean;
  /** 放行的调用方；host-gateway 表示每台主机的主机网关。 */
  callers?: string[];
}

export interface CatalogService {
  path?: string;
  port?: number;
  console_name?: string;
  timeout_ms?: WireInt64;
  max_body_bytes?: WireInt64;
  methods?: CatalogMethod[];
}

export interface CatalogComponent {
  id?: string;
  name?: string;
  binary?: string;
  /** host（每台主机自动部署）、control、any。 */
  scope?: string;
  /** per_host、single、multi。 */
  replicas?: string;
  protected?: boolean;
  /** readyz、https、none。 */
  health_kind?: string;
  health_port?: number;
  ports?: CatalogPort[];
  services?: CatalogService[];
}

export interface CatalogPrincipal {
  id?: string;
  description?: string;
  allow?: Array<{ service?: string; methods?: string[] }>;
}

export interface Catalog {
  checksum?: string;
  components?: CatalogComponent[];
  principals?: CatalogPrincipal[];
}

/** 主机网关的心跳状态：state 为 online、offline、never_reported、conflict。 */
export interface HostGatewayStatus {
  state?: string;
  synced?: boolean;
  instance_id?: string;
  version?: string;
  expected_hash?: string;
  applied_hash?: string;
  route_count?: number;
  last_seen_at?: string;
  last_error?: string;
  previous_instance_id?: string;
  replaced_at?: string;
  conflict_instance_id?: string;
  conflict_seen_at?: string;
  out_of_sync_since?: string;
}

export interface DeployHost {
  host_id?: string;
  address?: string;
  private_address?: string;
  region?: string;
  /** enabled 或 disabled。 */
  status?: string;
  description?: string;
  /** control 主机受保护，不能停用。 */
  protected?: boolean;
  created_at?: string;
  updated_at?: string;
  gateway?: HostGatewayStatus;
}

export interface DeployPlacement {
  host_id?: string;
  component_id?: string;
  /** enabled 或 disabled。 */
  status?: string;
  protected?: boolean;
  /** 「主机」范围的组件由系统自动维护。 */
  host_component?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface HostRoute {
  component_id?: string;
  service_path?: string;
  address?: string;
  timeout_ms?: WireInt64;
  max_body_bytes?: WireInt64;
  methods?: string[];
  callers?: string[];
}

export interface HostRoutes {
  host_id?: string;
  disabled?: boolean;
  expected_hash?: string;
  generated_at?: string;
  routes?: HostRoute[];
  callers?: string[];
  gateway?: HostGatewayStatus;
}
