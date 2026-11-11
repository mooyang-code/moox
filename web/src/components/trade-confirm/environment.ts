import type { LogicalAccount, TradingAccount } from "@/api/trade/types";

/** 账户所处环境：模拟盘不连交易所，测试网和生产环境连交易所对应环境。 */
export type Environment = "paper" | "testnet" | "prod";

export const environmentTabs: readonly { key: Environment; label: string; desc: string }[] = [
  { key: "paper", label: "模拟盘", desc: "用真实行情在本地撮合，不连交易所。" },
  { key: "testnet", label: "测试网", desc: "连交易所测试环境，用测试币验证下单流程。" },
  { key: "prod", label: "生产环境", desc: "连交易所生产环境，用真实资金交易。" }
];

/**
 * 每个环境在「交易管理」下各有一个菜单：列表页是 path，这个环境的组合详情是 path/组合编号。
 * 详情路由的 name 是菜单 name 加 "-detail"，侧栏按 activeMenu 高亮所属菜单。
 */
export const environmentMenus: Readonly<Record<Environment, { name: string; path: string; title: string }>> = {
  paper: { name: "trading-paper", path: "/trading/paper", title: "模拟账户" },
  testnet: { name: "trading-testnet", path: "/trading/testnet", title: "测试账户" },
  prod: { name: "trading-live", path: "/trading/live", title: "实盘账户" }
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

/** 执行账户的环境。实盘账户没有写明环境时按生产环境处理，和后端的规则一致。 */
export function accountEnvironment(account: Pick<TradingAccount, "execution_mode" | "live">): Environment {
  if (account.execution_mode === 1) return "paper";
  return account.live?.environment === 1 ? "testnet" : "prod";
}

/**
 * 组合账户的环境：后端要求成员环境一致，所以取第一个能找到的成员的环境。
 * 实盘组合没有可判断的成员时（测试账户、实盘账户两个菜单都列出它）用 entered：从哪个菜单进来就按哪个环境显示；
 * 不知道从哪进来（例如直接打开链接）时按生产环境处理，确认也按最严的一档。
 */
export function logicalEnvironment(
  logical: Pick<LogicalAccount, "execution_mode" | "members">,
  accounts: ReadonlyMap<string, TradingAccount>,
  entered?: Environment
): Environment {
  if (logical.execution_mode === 1) return "paper";
  for (const member of logical.members || []) {
    const account = accounts.get(member.trading_account_id);
    if (account) return accountEnvironment(account);
  }
  return entered === "testnet" ? "testnet" : "prod";
}

/**
 * 组合是否列在这个环境的菜单里。实盘组合还没有可判断的成员时（例如新建账户加入组合失败后留下的空组合），
 * 测试账户和实盘账户两个菜单都列出它，好让用户在任一处给它加成员或处理掉；其余按 logicalEnvironment 判断。
 */
export function logicalInEnvironment(
  logical: Pick<LogicalAccount, "execution_mode" | "members">,
  accounts: ReadonlyMap<string, TradingAccount>,
  env: Environment
): boolean {
  if (logical.execution_mode === 1) return env === "paper";
  const known = (logical.members || []).some(member => accounts.has(member.trading_account_id));
  if (!known) return env !== "paper";
  return logicalEnvironment(logical, accounts) === env;
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
