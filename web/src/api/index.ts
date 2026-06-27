import axios from "axios";
import router from "@/router";
import { Message } from "@arco-design/web-vue";
import { isRetInfoSuccess, isAuthExpiredCode } from "@/api/ret-info";

// 创建axios实例
const service = axios.create({
  baseURL: ""
});
// 请求拦截器
service.interceptors.request.use(
  function (config: any) {
    // 发送请求之前做什么
    // 获取token鉴权
    let userInfo: any = {};
    if (localStorage.getItem("user-info")) {
      userInfo = JSON.parse(localStorage.getItem("user-info") as string);
    }
    if (userInfo?.token) {
      // 有token，在请求头中携带token
      config.headers.Authorization = userInfo.token;
    }
    return config;
  },
  function (error: any) {
    // 请求错误
    return Promise.reject(error);
  }
);

// 响应拦截器
service.interceptors.response.use(
  function (response: any) {
    if (response.status != 200) {
      Message.error("服务器异常，请联系管理员");
      return Promise.reject(response.data);
    }
    let res = response.data;
    
    // collector类接口格式：直接包含code和data字段
    if (res.code !== undefined && res.data !== undefined) {
      // 处理collector类接口格式
      if (res.code === 200) {
        // 成功
        return Promise.resolve(res);
      } else {
        // 错误
        Message.error(res.message || "请求失败");
        return Promise.reject(res);
      }
    }
    
    // 新协议格式：检查ret_info字段
    if (res.ret_info) {
      // 处理新的ret_info协议格式（code 为 0 / '0' / 200 / '200' / 'SUCCESS' 均为成功）
      const code = res.ret_info.code;
      if (isAuthExpiredCode(code)) {
        Message.error("登录状态已过期");
        // 清除本地存储，避免死循环
        localStorage.removeItem("user-info");
        // 使用replace避免在浏览器历史中留下记录，防止用户返回到错误页面
        router.replace("/login");
        return Promise.reject(res);
      } else if (!isRetInfoSuccess(code)) {
        // 非成功状态码，当作错误处理
        Message.error(res.ret_info.msg || "请求失败");
        return Promise.reject(res);
      } else {
        // 成功：返回完整响应体（业务字段在顶层，如 instances/rules/total 等）
        return Promise.resolve(res);
      }
    }
    
    // 如果没有ret_info字段，也没有code/data字段，说明响应格式不正确
    console.warn('响应格式不正确，缺少ret_info或code/data字段:', res);
    Message.error("响应格式错误");
    return Promise.reject(res);
  },
  function (error: any) {
    console.error("API请求失败:", error);

    // 处理网络连接错误
    if (error.code === 'ECONNREFUSED' || error.message?.includes('ECONNREFUSED')) {
      Message.error("网络异常:请确认moox后端服务部署正常");
      return Promise.reject(error);
    }

    // 处理其他网络错误
    if (error.code === 'ETIMEDOUT' || error.message?.includes('timeout')) {
      Message.error("请求超时，请检查网络连接");
      return Promise.reject(error);
    }

    if (error.code === 'ENOTFOUND' || error.message?.includes('ENOTFOUND')) {
      Message.error("网络异常:无法连接到服务器");
      return Promise.reject(error);
    }

    // 处理没有响应的情况（网络完全断开）
    if (!error.response) {
      Message.error("网络异常:请确认moox后端服务部署正常");
      return Promise.reject(error);
    }

    // 只在认证相关错误时清除用户信息，其他网络错误不应该清除
    if (error.response?.status === 401 || error.response?.data?.ret_info?.code === 3) {
      localStorage.removeItem("user-info");
      // 使用replace避免在浏览器历史中留下记录，防止用户返回到错误页面
      router.replace("/login");
    } else if (error.response?.status >= 500) {
      // 处理服务器内部错误
      Message.error("服务器内部错误，请确认moox后端服务是否正常");
    } else if (error.response?.status === 404) {
      Message.error("请求的资源不存在");
    } else if (error.response?.status >= 400) {
      // 处理其他客户端错误
      Message.error(error.response?.data?.message || "请求失败");
    }

    return Promise.reject(error);
  }
);
export default service;
