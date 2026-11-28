import { AccountEnvironment, ExecutionMode, type LogicalAccount, type TradingAccount } from "@/api/trade/types";
import { flatRoutes } from "@/router/route";

/** 账户所处环境：模拟盘不连交易所，测试网和生产环境连交易所对应环境。 */
export type Environment = "paper" | "testnet" | "prod";

export const environmentTabs: readonly { key: Environment; label: string; desc: string }[] = [
  { key: "paper", label: "模拟盘", desc: "用真实行情在本地撮合，不连交易所。" },
  { key: "testnet", label: "测试网", desc: "连交易所测试环境，用测试币验证下单流程。" },
  { key: "prod", label: "生产环境", desc: "连交易所生产环境，用真实资金交易。" }
];

/** 路由表里这个环境的账户列表菜单（meta.env 写明环境、不隐藏的那条）。 */
function environmentMenu(env: Environment): { name: string; path: string; title: string } {
  const route = flatRoutes().find(item => item.meta.env === env && !item.meta.hide);
  if (!route) throw new Error(`路由表里没有 ${env} 环境的账户菜单`);
  return { name: route.name, path: route.path, title: String(route.meta.title) };
}

/**
 * 每个环境在「交易管理」下各有一个菜单，取自路由表：列表页是 path，这个环境的组合详情是 path/组合编号。
 * 详情路由的 name 是菜单 name 加 "-detail"，侧栏按 activeMenu 高亮所属菜单。
 */
export const environmentMenus: Readonly<Record<Environment, { name: string; path: string; title: string }>> = {
  paper: environmentMenu("paper"),
  testnet: environmentMenu("testnet"),
  prod: environmentMenu("prod")
};

/** 路由 meta.env 写明页面属于哪个环境；没写（按组合编号直接打开的通用详情地址）返回 undefined。 */
export function environmentOfRoute(route: { meta?: Record<string, unknown> }): Environment | undefined {
  const value = route.meta?.env;
  return value === "paper" || value === "testnet" || value === "prod" ? value : undefined;
}

/** 组合详情页地址。 */
export function logicalDetailPath(env: Environment, logicalAccountId: string): string {
  return `${environmentMenus[env].path}/${encodeURIComponent(logicalAccountId)}`;
}

export function environmentLabel(env: Environment): string {
  return environmentTabs.find(item => item.key === env)?.label ?? "";
}

/** 前端三值环境转后端协议枚举（AccountEnvironment）。 */
export function toAccountEnvironment(env: Environment): AccountEnvironment {
  if (env === "paper") return AccountEnvironment.PAPER;
  if (env === "testnet") return AccountEnvironment.TESTNET;
  return AccountEnvironment.PRODUCTION;
}

/** 执行账户的环境。实盘账户没有写明环境时按生产环境处理，和后端的规则一致。 */
export function accountEnvironment(account: Pick<TradingAccount, "execution_mode" | "live">): Environment {
  if (account.execution_mode === ExecutionMode.PAPER) return "paper";
  return account.live?.environment === AccountEnvironment.TESTNET ? "testnet" : "prod";
}

/** 组合账户的环境：直接读后端返回的三值 environment（3 模拟盘、1 测试网、2 生产环境）。 */
export function logicalEnvironment(logical: Pick<LogicalAccount, "environment">): Environment {
  if (logical.environment === AccountEnvironment.PAPER) return "paper";
  if (logical.environment === AccountEnvironment.TESTNET) return "testnet";
  return "prod";
}

/** 组合是否列在这个环境的菜单里：按后端返回的三值 environment 精确匹配。 */
export function logicalInEnvironment(logical: Pick<LogicalAccount, "environment">, env: Environment): boolean {
  return logicalEnvironment(logical) === env;
}

export interface ConfirmTier {
  env: Environment;
  /** 对话框顶部的提示色条文字，模拟盘没有 */
  banner: string;
  /** 是否要输入组合名称才能确认 */
  requiresName: boolean;
}

/** 危险操作按环境分三档确认：模拟盘直接确认，测试网标明环境，生产环境要输入组合名称。 */
export function confirmTier(env: Environment): ConfirmTier {
  switch (env) {
    case "testnet":
      return { env, banner: "这是测试网，使用交易所测试环境的测试币。", requiresName: false };
    case "prod":
      return { env, banner: "这是生产环境，会使用真实资金。", requiresName: true };
    default:
      return { env, banner: "", requiresName: false };
  }
}

export function nameConfirmed(tier: ConfirmTier, typed: string, name: string): boolean {
  return !tier.requiresName || typed === name;
}
