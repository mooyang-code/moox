import axios from 'axios';

// 认证信息配置
export const AUTH_INFO = {
  app_id: "moox_frontend",
  app_key: "2521e0d21b6be0347b72bca93904a0dd"
};

// 获取当前页面的IP地址
const getCurrentHost = () => {
  const url = window.location.href;
  const urlObj = new URL(url);
  return urlObj.hostname;
};

// 创建axios实例
export const api = axios.create({
  baseURL: `http://${getCurrentHost()}:18202/gateway/metadata`,
  headers: {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
    'app_id': AUTH_INFO.app_id,
    'app_key': AUTH_INFO.app_key
  }
});

// 请求拦截器 - 动态添加X-Access-Token
api.interceptors.request.use(
  (config) => {
    // 从localStorage获取token（兼容原有存储方式）
    try {
      const userInfo = localStorage.getItem('user-info');
      if (userInfo) {
        const { token } = JSON.parse(userInfo);
        if (token) {
          config.headers['X-Access-Token'] = token;
          console.log('设置X-Access-Token:', token.substring(0, 20) + '...');
        } else {
          console.warn('localStorage中没有找到token');
        }
      } else {
        console.warn('localStorage中没有找到user-info');
      }
    } catch (error) {
      console.error('解析localStorage中的用户信息失败:', error);
    }
    return config;
  },
  (error) => {
    return Promise.reject(error);
  }
);

// 响应拦截器
api.interceptors.response.use(
  (response) => {
    const { data } = response;
    
    // 检查业务级token失效错误
    if (data && (data.code === 3 || data.code === 401)) {
      // 清除token并跳转登录页
      localStorage.removeItem('user-info');
      console.log('Token失效，清除localStorage并跳转登录页');
      window.location.href = '/login';
      return Promise.reject(new Error(data.message || 'Token失效'));
    }
    
    // 检查是否为旧协议格式（有 ret_info 字段）
    if (data?.ret_info) {
      if (data.ret_info.code === 0) {
        return data;
      }
      return Promise.reject(data.ret_info);
    }
    
    // 新协议格式：已经简化，直接返回
    return response;
  },
  (error) => {
    // 处理HTTP状态码401
    if (error.response?.status === 401) {
      localStorage.removeItem('user-info');
      console.log('HTTP 401错误，清除localStorage并跳转登录页');
      window.location.href = '/login';
    }
    
    // 保持原有错误处理逻辑
    return Promise.reject(error.response?.data?.ret_info || error);
  }
); 