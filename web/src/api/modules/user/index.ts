import { call } from "@/api/http";

export interface LoginResult {
  user_info?: Record<string, any>;
  expires_at?: number;
}

/** 登录：密码明文交给 HTTPS 保护；成功后会话在 HttpOnly Cookie 里，响应体里没有会话信息。错误由登录页自己提示。 */
export const loginAPI = (data: { username: string; password: string }) =>
  call<{ username: string; password: string }, LoginResult>("auth", "Login", data, { authProbe: true });

/** 取当前登录用户；401 说明没有登录，由调用方（路由守卫、登录页）处理，不弹提示、不自动跳转。 */
export const getUserInfoAPI = () =>
  call<Record<string, never>, { user_info?: Record<string, any> }>("auth", "GetUserInfo", {}, { authProbe: true });

export const logoutAPI = () => call<Record<string, never>, Record<string, never>>("auth", "Logout", {}, { authProbe: true });
