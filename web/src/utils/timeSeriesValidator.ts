/**
 * 时序周期验证工具
 * 基于后端 CheckTimeSeriesOrder 逻辑实现前端验证
 */

export interface TimeSeriesValidationResult {
  isValid: boolean;
  message: string;
  validFormats?: string[];
}

/**
 * 验证单个时序周期格式
 * @param freq 时序周期，如 "1m", "5H", "1D"
 * @returns 验证结果
 */
export function validateSingleFreq(freq: string): TimeSeriesValidationResult {
  if (!freq || typeof freq !== 'string') {
    return {
      isValid: false,
      message: '时序周期不能为空',
      validFormats: getValidFormats()
    };
  }

  const trimmedFreq = freq.trim();
  if (!trimmedFreq) {
    return {
      isValid: false,
      message: '时序周期不能为空',
      validFormats: getValidFormats()
    };
  }

  // 特殊情况：0 表示无固定周期
  if (trimmedFreq === '0') {
    return {
      isValid: true,
      message: '无固定周期的时序数据'
    };
  }

  // 解析频率格式：数字+单位
  const freqRegex = /^(\d+)?([smHDWMY])$/;
  const match = trimmedFreq.match(freqRegex);

  if (!match) {
    return {
      isValid: false,
      message: `无效的时序周期格式: ${trimmedFreq}`,
      validFormats: getValidFormats()
    };
  }

  const [, intervalStr, unit] = match;
  const interval = intervalStr ? parseInt(intervalStr, 10) : 1;

  // 验证数字部分（允许0表示无固定周期）
  if (interval < 0) {
    return {
      isValid: false,
      message: `时序周期的数字部分不能为负数: ${trimmedFreq}`,
      validFormats: getValidFormats()
    };
  }

  // 如果数字部分为0，只允许单独的"0"，不允许"0m"这样的格式
  if (interval === 0) {
    return {
      isValid: false,
      message: `无固定周期请使用"0"，不要添加单位: ${trimmedFreq}`,
      validFormats: getValidFormats()
    };
  }

  // 验证单位部分
  const validUnits = ['s', 'm', 'H', 'D', 'W', 'M', 'Y'];
  if (!validUnits.includes(unit)) {
    return {
      isValid: false,
      message: `不支持的时序周期单位: ${unit}`,
      validFormats: getValidFormats()
    };
  }

  return {
    isValid: true,
    message: '时序周期格式正确'
  };
}

/**
 * 将用户粘贴的周期文本解析为数组。
 * 支持逗号、中文逗号、加号和换行，便于从旧表达式迁移到 string[]。
 */
export function parseFreqInput(input: string | string[]): string[] {
  if (Array.isArray(input)) {
    return input.map((item) => item.trim()).filter(Boolean);
  }
  return input
    .split(/[+,\n，]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

/**
 * 验证时序周期数组。
 * @param freqs 时序周期数组，如 ["1m", "5m", "1H"] 或 ["0"]（表示无固定周期）
 * @returns 验证结果
 */
export function validateTimeSeriesFreqs(freqs: string[]): TimeSeriesValidationResult {
  if (!Array.isArray(freqs)) {
    return {
      isValid: false,
      message: '时序周期不能为空',
      validFormats: getValidFormats()
    };
  }

  const freqList = freqs.map((freq) => freq.trim()).filter(Boolean);
  if (freqList.length === 0) {
    return {
      isValid: false,
      message: '时序周期不能为空',
      validFormats: getValidFormats()
    };
  }

  // 特殊情况：0 表示无固定周期的时序数据
  if (freqList.length === 1 && freqList[0] === '0') {
    return {
      isValid: true,
      message: '无固定周期的时序数据'
    };
  }

  // 检查是否包含0值与其他周期的组合
  if (freqList.includes('0') && freqList.length > 1) {
    return {
      isValid: false,
      message: '无固定周期（0）不能与其他周期组合使用',
      validFormats: getValidFormats()
    };
  }

  // 验证每个周期
  for (const freq of freqList) {
    const result = validateSingleFreq(freq);
    if (!result.isValid) {
      return result;
    }
  }

  // 检查是否有重复的周期
  const uniqueFreqs = new Set(freqList);
  if (uniqueFreqs.size !== freqList.length) {
    return {
      isValid: false,
      message: '时序周期中存在重复项',
      validFormats: getValidFormats()
    };
  }

  return {
    isValid: true,
    message: '时序周期格式正确'
  };
}

/**
 * 获取支持的时序周期格式说明
 * @returns 格式说明数组
 */
export function getValidFormats(): string[] {
  return [
    '0 - 无固定周期的时序数据',
    's - 秒（如：1s, 30s）',
    'm - 分钟（如：1m, 5m, 15m）',
    'H - 小时（如：1H, 4H, 12H）',
    'D - 天（如：1D, 7D）',
    'W - 周（如：1W, 2W）',
    'M - 月（如：1M, 3M）',
    'Y - 年（如：1Y）',
    '多个周期使用数组表达（如：["1m", "5m", "1H", "1D"]）'
  ];
}

/**
 * 获取常用的时序周期示例
 * @returns 示例数组
 */
export function getFreqExamples(): string[] {
  return [
    '0',
    '1m',
    '5m',
    '15m',
    '1H',
    '4H',
    '1D',
    '1m,5m,1H,1D',
    '1s,1m,1H',
    '15m,1H,4H,1D'
  ];
}
