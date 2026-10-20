import { Message } from "@arco-design/web-vue";

type MessageMethod = (config: unknown, ...rest: unknown[]) => unknown;

/** 同一条文案共用一个 id：Arco 对相同 id 的提示是“更新”而不是再叠一条，所以重复的错误只会显示一条。 */
export function dedupeMessageConfig(config: unknown): unknown {
  if (typeof config === "string") return { content: config, id: `dedupe:${config}` };
  if (config && typeof config === "object") {
    const options = config as { content?: unknown; id?: unknown };
    if (options.id === undefined && typeof options.content === "string") return { ...options, id: `dedupe:${options.content}` };
  }
  return config;
}

/**
 * 应用启动时调用一次：相同文案的错误/警告提示只显示一条。
 * 页面里一批请求同时失败（例如浏览器连不上管理台）时，每个请求各弹一条会把页面刷满并不停抖动。
 */
export function installMessageDedupe(): void {
  const target = Message as unknown as Record<string, MessageMethod>;
  for (const level of ["error", "warning"]) {
    const raw = target[level].bind(Message) as MessageMethod;
    target[level] = (config, ...rest) => raw(dedupeMessageConfig(config), ...rest);
  }
}
