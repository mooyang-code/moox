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
  baseURL: `http://${getCurrentHost()}:20103/gateway`,
  timeout: 10000, // 10秒超时
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
    
    // 新协议格式：所有接口返回信息都在ret_info字段中
    if (data?.ret_info) {
      // 检查业务级token失效错误
      if (data.ret_info.code === 3 || data.ret_info.code === 401) {
        // 清除token并跳转登录页
        localStorage.removeItem('user-info');
        console.log('Token失效，清除localStorage并跳转登录页');
        // 使用window.location.replace避免在浏览器历史中留下记录
        window.location.replace('/login');
        return Promise.reject(new Error(data.ret_info.msg || 'Token失效'));
      }
      
      // 返回完整的data，让各个API自己处理ret_info
      return response;
    }
    
    // 兼容新格式：直接返回 code 和 data 的格式
    if (data?.code === 200 || data?.code === 0) {
      // 新格式的响应，直接返回
      return response;
    }
    
    // 如果既没有ret_info也不是新格式，说明响应格式不正确
    console.warn('响应格式不正确:', data);
    return response;
  },
  (error) => {
    // 处理HTTP状态码401
    if (error.response?.status === 401) {
      localStorage.removeItem('user-info');
      console.log('HTTP 401错误，清除localStorage并跳转登录页');
      // 使用window.location.replace避免在浏览器历史中留下记录
      window.location.replace('/login');
    }
    
    return Promise.reject(error.response?.data?.ret_info || error);
  }
); 
