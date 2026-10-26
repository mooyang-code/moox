import { LOCKED_SYSTEM_KEYS, SYSTEM_KEYS, type FactorScope } from "./factor-scope";

/** 主表最多钉住的因子列数；更多的因子请在“查看全部”弹窗里看。 */
export const MAX_PINNED = 30;

/** 结果页的列设置，按因子任务分别保存在浏览器本地。 */
export interface ColumnSettings {
  /** 隐藏的系统键（只能是频率、序列标签）。 */
  hiddenSystem: string[];
  /** 显示的行情列及顺序（基础行情与其他源字段）。 */
  market: string[];
  /** 钉成外显列的因子输出列及顺序。 */
  pinned: string[];
}

const storageKey = (taskId: string) => `moox.factor-results.columns.${taskId}`;

export function defaultColumnSettings(scope: FactorScope): ColumnSettings {
  return { hiddenSystem: [], market: [...scope.baseMarket], pinned: [] };
}

function stringList(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.some(item => typeof item !== "string")) return undefined;
  return [...new Set(value as string[])];
}

/**
 * 校验并按有效范围收敛：结构不对就回到默认；去重；不可隐藏的系统键强制显示；
 * 不在有效范围里的行情列与钉住列剔除；钉住列最多 MAX_PINNED 个。
 * 只能在视图列元数据成功加载之后调用，否则会把加载中的空范围当成全部失效。
 */
export function normalizeColumnSettings(raw: unknown, scope: FactorScope): ColumnSettings {
  if (!raw || typeof raw !== "object") return defaultColumnSettings(scope);
  const value = raw as Record<string, unknown>;
  const hiddenSystem = stringList(value.hiddenSystem);
  const market = stringList(value.market);
  const pinned = stringList(value.pinned);
  if (!hiddenSystem || !market || !pinned) return defaultColumnSettings(scope);
  const marketColumns = new Set([...scope.baseMarket, ...scope.otherSource]);
  return {
    hiddenSystem: hiddenSystem.filter(
      key => (SYSTEM_KEYS as readonly string[]).includes(key) && !LOCKED_SYSTEM_KEYS.includes(key)
    ),
    market: market.filter(column => marketColumns.has(column)),
    pinned: pinned.filter(column => scope.outputByColumn.has(column)).slice(0, MAX_PINNED)
  };
}

/** 读取失败（存储不可用、JSON 无效）时回到默认。 */
export function loadColumnSettings(taskId: string, scope: FactorScope): ColumnSettings {
  try {
    const text = window.localStorage.getItem(storageKey(taskId));
    return text ? normalizeColumnSettings(JSON.parse(text), scope) : defaultColumnSettings(scope);
  } catch {
    return defaultColumnSettings(scope);
  }
}

export function saveColumnSettings(taskId: string, settings: ColumnSettings) {
  try {
    window.localStorage.setItem(storageKey(taskId), JSON.stringify(settings));
  } catch {
    // 本地存储不可用时只在本次页面内生效。
  }
}

/** 主表的显示列与请求投影：投影只含业务列；全部隐藏时投影一个基础行情列但不显示，避免空投影读回全部列。 */
export function tableColumns(settings: ColumnSettings, scope: FactorScope) {
  const hidden = new Set(settings.hiddenSystem);
  const business = [...settings.market, ...settings.pinned];
  const fallback = scope.baseMarket.includes("close") ? "close" : (scope.baseMarket[0] ?? scope.otherSource[0]);
  return {
    display: { system: SYSTEM_KEYS.filter(key => !hidden.has(key)), business },
    project: business.length ? business : fallback ? [fallback] : [],
    search: settings.market
  };
}
