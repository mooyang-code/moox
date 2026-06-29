import axios from 'axios';
import { gatewayURL } from '@/api/gateway';
import { isAuthExpiredCode, isRetInfoSuccess } from './ret-info';
import { APP_AUTH_INFO, appAuthHeaders } from './storage/auth';

// 认证信息配置
export const AUTH_INFO = APP_AUTH_INFO;

// 创建axios实例
export const api = axios.create({
  baseURL: gatewayURL('/api/admin'),
  timeout: 10000, // 10秒超时
  headers: {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
    ...appAuthHeaders()
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
        }
      }
    } catch {
      // 忽略本地认证缓存损坏，后续请求会按未登录处理。
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
    // 框架错误：HTTP 200 但 trpc-ret != 0，body 为空，错误信息在 trpc-ret/trpc-func-ret header。
    const trpcRet = response.headers?.['trpc-ret'] ?? response.headers?.['Trpc-Ret'];
    if (trpcRet !== undefined && trpcRet !== null && String(trpcRet) !== '0') {
      const funcRet = response.headers?.['trpc-func-ret'] ?? '';
      return Promise.reject(new Error(funcRet || `框架错误(${trpcRet})`));
    }

    const { data } = response;

    // 新协议格式：所有接口返回信息都在ret_info字段中
    if (data?.ret_info) {
      // 检查业务级token失效错误
      if (isAuthExpiredCode(data.ret_info.code)) {
        // 清除token并跳转登录页
        localStorage.removeItem('user-info');
        // 使用window.location.replace避免在浏览器历史中留下记录
        window.location.replace('/login');
        return Promise.reject(new Error(data.ret_info.msg || 'Token失效'));
      }

      // 返回完整的data，让各个API自己处理ret_info
      return response;
    }

    // 兼容新格式：直接返回 code 和 data 的格式
    if (isRetInfoSuccess(data?.code)) {
      // 新格式的响应，直接返回
      return response;
    }

    // 如果既没有ret_info也不是新格式，说明响应格式不正确
    return response;
  },
  (error) => {
    // 处理HTTP状态码401
    if (error.response?.status === 401) {
      localStorage.removeItem('user-info');
      // 使用window.location.replace避免在浏览器历史中留下记录
      window.location.replace('/login');
    }

    // 特殊处理：如果错误信息是字符串且包含JSON，尝试解析
    if (typeof error.response?.data === 'string' && error.response.data.includes('{"code":')) {
      try {
        const jsonMatch = error.response.data.match(/\{[^}]+\}/);
        if (jsonMatch) {
          const parsedData = JSON.parse(jsonMatch[0]);
          // 用解析后的数据替换原始数据
          error.response.data = parsedData;
        }
      } catch {
        // 保留原始错误响应，由调用方按普通请求失败处理。
      }
    }
    
    return Promise.reject(error);
  }
); 
