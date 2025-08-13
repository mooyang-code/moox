/**
 * 错误处理工具函数
 * 用于统一处理TypeScript中的错误类型检查
 */

/**
 * 检查是否为Error实例
 */
export function isError(error: unknown): error is Error {
  return error instanceof Error;
}

/**
 * 安全获取错误消息
 * @param error 未知类型的错误
 * @param defaultMessage 默认错误消息
 * @returns 错误消息字符串
 */
export function getErrorMessage(error: unknown, defaultMessage = '未知错误'): string {
  if (isError(error)) {
    return error.message || defaultMessage;
  }
  
  if (typeof error === 'string') {
    return error;
  }
  
  if (error && typeof error === 'object') {
    // 处理API响应错误格式
    const errorObj = error as any;
    
    // 检查是否有response.data.ret_info.msg格式
    if (errorObj.response?.data?.ret_info?.msg) {
      return errorObj.response.data.ret_info.msg;
    }
    
    // 检查是否有response.data.message格式
    if (errorObj.response?.data?.message) {
      return errorObj.response.data.message;
    }
    
    // 检查是否有message属性
    if (errorObj.message) {
      return errorObj.message;
    }
  }
  
  return defaultMessage;
}

/**
 * 安全获取错误堆栈信息
 * @param error 未知类型的错误
 * @returns 错误堆栈字符串或undefined
 */
export function getErrorStack(error: unknown): string | undefined {
  if (isError(error)) {
    return error.stack;
  }
  return undefined;
}

/**
 * 检查是否为网络错误
 * @param error 未知类型的错误
 * @returns 是否为网络错误
 */
export function isNetworkError(error: unknown): boolean {
  const message = getErrorMessage(error);
  const networkErrorPatterns = [
    'ECONNREFUSED',
    'ETIMEDOUT',
    'ENOTFOUND',
    'timeout',
    'Network Error',
    'network error'
  ];
  
  return networkErrorPatterns.some(pattern => 
    message.toLowerCase().includes(pattern.toLowerCase())
  );
}

/**
 * 检查是否为认证错误
 * @param error 未知类型的错误
 * @returns 是否为认证错误
 */
export function isAuthError(error: unknown): boolean {
  if (error && typeof error === 'object') {
    const errorObj = error as any;
    
    // 检查HTTP状态码
    if (errorObj.response?.status === 401) {
      return true;
    }
    
    // 检查API响应码
    if (errorObj.response?.data?.ret_info?.code === 3) {
      return true;
    }
  }
  
  return false;
}

/**
 * 格式化错误信息用于显示
 * @param error 未知类型的错误
 * @param context 错误上下文信息
 * @returns 格式化后的错误消息
 */
export function formatErrorForDisplay(error: unknown, context?: string): string {
  const message = getErrorMessage(error);
  
  if (context) {
    return `${context}: ${message}`;
  }
  
  return message;
}

/**
 * 创建标准化的错误对象
 * @param message 错误消息
 * @param originalError 原始错误对象
 * @returns 标准化的Error对象
 */
export function createStandardError(message: string, originalError?: unknown): Error {
  const error = new Error(message);
  
  if (originalError && isError(originalError)) {
    error.stack = originalError.stack;
    error.cause = originalError;
  }
  
  return error;
}
