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
  total: number | string;
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
