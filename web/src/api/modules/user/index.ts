import axios from "@/api";
import { getAppInfo } from "@/api/storage/auth";
import { secureLoginManager } from "@/utils/crypto";

// 安全登录（新版本）
export const loginAPI = async (data: { username: string; password: string; verifyCode: string }) => {
  // 使用安全登录管理器进行登录
  return await secureLoginManager.login(data.username, data.password);
};

// 获取登录盐值
export const getLoginSaltAPI = async (data: { username: string }) => {
  try {
    const response = await axios({
      url: "/api/control/auth/GetLoginSalt",
      method: "post",
      data: {
        app_info: getAppInfo(),
        username: data.username
      }
    });
    
    // 注意：由于响应拦截器的处理，axios直接返回数据而不是response对象
    const result = response as any;
    
    // 添加安全检查
    if (!result) {
      throw new Error('获取登录盐值失败：响应数据为空');
    }
    
    // 使用新的ret_info协议格式
    if (!result.ret_info) {
      throw new Error('获取登录盐值失败：响应格式错误，缺少ret_info字段');
    }
    
    if (result.ret_info.code !== 0) {
      throw new Error(result.ret_info.msg || '获取登录盐值失败');
    }
    
    return result;
  } catch (error: unknown) {
    console.error('获取登录盐值失败:', error);
    throw error;
  }
};

// 获取用户信息 - 调用真实后台接口
export const getUserInfoAPI = async (accessToken: string) => {
  try {
    const response = await axios({
      url: "/api/control/auth/GetUserInfo",
      method: "post",
      data: {
        app_info: getAppInfo(),
        access_token: accessToken,
        user_id: "" // 空字符串表示获取当前用户信息
      }
    });
    
    // 注意：由于响应拦截器的处理，axios直接返回数据而不是response对象
    const result = response as any;
    
    // 添加安全检查
    if (!result) {
      throw new Error('获取用户信息失败：响应数据为空');
    }
    
    // 使用新的ret_info协议格式
    if (!result.ret_info) {
      throw new Error('获取用户信息失败：响应格式错误，缺少ret_info字段');
    }
    
    if (result.ret_info.code !== 0) {
      throw new Error(result.ret_info.msg || '获取用户信息失败');
    }
    
    return result;
  } catch (error: unknown) {
    console.error('获取用户信息失败:', error);
    throw error;
  }
};
