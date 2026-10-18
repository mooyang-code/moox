import { Message } from "@arco-design/web-vue";

export const STALE_SPACE_MESSAGE = "空间已切换，已取消过期请求";

/** 切换空间后被取消（或返回过晚）的空间读取。调用方应当把它当作静默取消处理，而不是失败。 */
export class StaleSpaceRequestError extends Error {
  constructor() {
    super(STALE_SPACE_MESSAGE);
    this.name = "StaleSpaceRequestError";
  }
}

type MessageError = (config: unknown, ...rest: unknown[]) => unknown;

/** 应用启动时调用一次：页面 catch 里直接展示 error.message 的错误提示，对“切换空间造成的取消”统一静默。 */
export function installStaleSpaceToastFilter(): void {
  const target = Message as unknown as { error: MessageError };
  const raw = target.error.bind(Message) as MessageError;
  target.error = (config, ...rest) => {
    const text = typeof config === "string" ? config : (config as { content?: unknown } | null)?.content;
    if (typeof text === "string" && text.includes(STALE_SPACE_MESSAGE)) return { close: () => undefined };
    return raw(config, ...rest);
  };
}
