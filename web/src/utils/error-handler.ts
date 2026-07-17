/**
 * 错误处理工具函数
 * 用于统一处理TypeScript中的错误类型检查
 */

/**
 * 检查是否为Error实例
 */
function isError(error: unknown): error is Error {
  return error instanceof Error;
}

/**
 * 安全获取错误消息
 * @param error 未知类型的错误
 * @param defaultMessage 默认错误消息
 * @returns 错误消息字符串
 */
export function getErrorMessage(error: unknown, defaultMessage = "未知错误"): string {
  if (isError(error)) {
    return error.message || defaultMessage;
  }

  if (typeof error === "string") {
    return error;
  }

  if (error && typeof error === "object") {
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
