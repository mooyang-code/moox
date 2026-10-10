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

export type DeploymentStatus = "enabled" | "disabled";
export interface DeploymentHost {
  host_id: string;
  address: string;
  private_address?: string;
  region?: string;
  status: DeploymentStatus;
  description?: string;
  created_at?: string;
  updated_at?: string;
}
export interface ComponentPlacement {
  host_id: string;
  component_id: string;
  status: DeploymentStatus;
  created_at?: string;
  updated_at?: string;
}
export interface CatalogGrant {
  methods: string[];
  callers: string[];
}
export interface CatalogService {
  path: string;
  port: number;
  methods: string[];
  acl: CatalogGrant[];
  timeout_ms?: number;
  max_body_bytes?: number;
  console_name?: string;
  read_only_methods?: string[];
}
export interface CatalogComponent {
  id: string;
  name: string;
  binary: string;
  scope: "host" | "control" | "any";
  replicas: "single" | "multi";
  protected: boolean;
  ports?: number[];
  health: { kind: string; port?: number; loopback?: boolean; ready_body?: string };
  services?: CatalogService[];
  doctor?: Record<string, unknown>;
}
export interface ComponentCatalog {
  version: number;
  components: CatalogComponent[];
  principals: { id: string; allow: { service: string; methods: string[] }[] }[];
}
export interface CatalogResponse {
  catalog_yaml: string;
  sha256: string;
  control_host_id: string;
}
export interface HostGatewayRuntimeStatus {
  instance_id?: string;
  version?: string;
  expected_hash?: string;
  applied_hash?: string;
  route_count?: number | string;
  last_seen_at?: string;
  last_error?: string;
  previous_instance_id?: string;
  replaced_at?: string;
  conflict_instance_id?: string;
  conflict_seen_at?: string;
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
  definition_hash: string;
  routes?: HostGatewayRoute[];
  gateway_status?: HostGatewayRuntimeStatus;
  snapshot_schema_version: number;
  compiled_at: string;
}
export interface ServiceDirectory {
  version?: string;
  services?: Record<string, { host_ids?: string[] }>;
  hosts?: Record<string, { address?: string; private_address?: string; region?: string }>;
}
