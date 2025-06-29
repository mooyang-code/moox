// 测试网络错误处理的工具函数
// 这个文件用于测试和验证网络错误处理是否正常工作

import service from '@/api/index';

/**
 * 测试网络连接错误处理
 * 这个函数会尝试连接到一个不存在的端口，模拟ECONNREFUSED错误
 */
export const testConnectionRefused = async () => {
  try {
    // 尝试连接到一个不存在的端口，这会触发ECONNREFUSED错误
    const response = await service.get('/test-connection-refused');
    console.log('意外成功:', response);
  } catch (error) {
    console.log('捕获到预期的网络错误:', error);
    // 这里应该会显示"网络异常:请确认moox后端服务部署正常"的弹窗
  }
};

/**
 * 测试超时错误处理
 */
export const testTimeout = async () => {
  try {
    // 设置一个很短的超时时间来模拟超时
    const response = await service.get('/test-timeout', { timeout: 1 });
    console.log('意外成功:', response);
  } catch (error) {
    console.log('捕获到预期的超时错误:', error);
    // 这里应该会显示"请求超时，请检查网络连接"的弹窗
  }
};

/**
 * 在开发环境中可以调用这些函数来测试错误处理
 */
export const runNetworkErrorTests = () => {
  console.log('开始测试网络错误处理...');
  
  // 测试连接被拒绝的情况
  testConnectionRefused();
  
  // 延迟一秒后测试超时情况
  setTimeout(() => {
    testTimeout();
  }, 1000);
};

// 在开发环境中，可以在浏览器控制台中调用 window.testNetworkErrors() 来测试
if (import.meta.env.DEV) {
  (window as any).testNetworkErrors = runNetworkErrorTests;
}
