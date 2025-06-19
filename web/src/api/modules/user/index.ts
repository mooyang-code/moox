import axios from "@/api";
import { secureLoginManager } from "@/utils/crypto";

// 安全登录（新版本）
export const loginAPI = async (data: { username: string; password: string; verifyCode: string }) => {
  // 使用安全登录管理器进行登录
  return await secureLoginManager.login(data.username, data.password, data.verifyCode);
};

// 获取登录盐值
export const getLoginSaltAPI = (data: { username: string }) => {
  return axios({
    url: "/gateway/auth/GetLoginSalt",
    method: "post",
    data: {
      app_info: {
        app_id: "moox_frontend",
        app_key: "2521e0d21b6be0347b72bca93904a0dd"
      },
      username: data.username
    }
  });
};

// 获取用户信息 - 调用真实后台接口
export const getUserInfoAPI = (accessToken: string) => {
  return axios({
    url: "/gateway/auth/GetUserInfo",
    method: "post",
    data: {
      app_info: {
        app_id: "moox_frontend",
        app_key: "2521e0d21b6be0347b72bca93904a0dd"
      },
      access_token: accessToken,
      user_id: "" // 空字符串表示获取当前用户信息
    }
  });
};
