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

export interface SpaceMember {
  space_id: string;
  user_id: string;
  role: string;
  status: string;
}

export interface ServiceDeployment {
  id?: number;
  service_name: string;
  service_kind: string;
  protocol: string;
  host: string;
  port: number;
  base_url?: string;
  rpc_address?: string;
  gateway_path?: string;
  scope: string;
  status: string;
  description?: string;
  extra_config?: string;
  created_at?: string;
  updated_at?: string;
  node_id: string;
  gateway_service_id: string;
  gateway_enabled: boolean;
}

export type ServiceDeploymentInput = Pick<
  ServiceDeployment,
  | "service_name"
  | "service_kind"
  | "protocol"
  | "host"
  | "port"
  | "gateway_path"
  | "scope"
  | "status"
  | "description"
  | "extra_config"
  | "node_id"
  | "gateway_service_id"
  | "gateway_enabled"
>;

export interface GatewayNode {
  node_id: string;
  host_id: number;
  name: string;
  public_address: string;
  status: string;
  route_hash?: string;
  applied_route_hash?: string;
  route_count?: number;
  last_seen_at?: string;
  last_error?: string;
  created_at?: string;
  updated_at?: string;
}

export type GatewayNodeInput = Pick<GatewayNode, "node_id" | "host_id" | "name" | "public_address" | "status">;

export interface GatewayRoute {
  service_id: string;
  address: string;
  service_path: string;
  timeout_ms: number;
  max_body_bytes: number;
  allowed_methods?: string[];
}

export interface ServiceDeploymentWarning {
  code: string;
  message: string;
  service_name?: string;
  related_endpoint?: string;
}
