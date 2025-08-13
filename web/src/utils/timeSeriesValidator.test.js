// 简单的测试文件，用于验证时序周期验证逻辑
import { validateTimeSeriesFreqs, validateSingleFreq } from './timeSeriesValidator.ts';

// 测试用例
const testCases = [
  // 测试0值（无固定周期）
  { input: '0', expected: true, description: '0值表示无固定周期' },
  { input: '0m', expected: false, description: '0m格式不允许' },
  { input: '0s', expected: false, description: '0s格式不允许' },
  
  // 测试正常格式
  { input: '1m', expected: true, description: '1分钟' },
  { input: '5m', expected: true, description: '5分钟' },
  { input: '1H', expected: true, description: '1小时' },
  { input: '1D', expected: true, description: '1天' },
  { input: 'm', expected: true, description: '默认1分钟' },
  
  // 测试组合格式
  { input: '1m+5m+1H+1D', expected: true, description: '多个周期组合' },
  { input: '0+1m', expected: false, description: '0不能与其他周期组合' },
  
  // 测试错误格式
  { input: '', expected: false, description: '空字符串' },
  { input: 'abc', expected: false, description: '无效格式' },
  { input: '-1m', expected: false, description: '负数不允许' },
  { input: '1x', expected: false, description: '无效单位' },
];

// 运行测试
console.log('开始测试时序周期验证逻辑...\n');

testCases.forEach((testCase, index) => {
  const result = validateTimeSeriesFreqs(testCase.input);
  const passed = result.isValid === testCase.expected;
  
  console.log(`测试 ${index + 1}: ${testCase.description}`);
  console.log(`  输入: "${testCase.input}"`);
  console.log(`  期望: ${testCase.expected ? '通过' : '失败'}`);
  console.log(`  实际: ${result.isValid ? '通过' : '失败'}`);
  console.log(`  消息: ${result.message}`);
  console.log(`  结果: ${passed ? '✅ 通过' : '❌ 失败'}\n`);
});

console.log('测试完成！');
