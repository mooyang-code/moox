export interface ControlResponse<T> {
  code?: number | string;
  message?: string;
  msg?: string;
  ret_info?: {
    code?: number | string;
    msg?: string;
  };
  data?: T;
}

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
}

export interface ServiceDeploymentWarning {
  code: string;
  message: string;
  service_name?: string;
  related_endpoint?: string;
}
