import { GLOBAL_METHODS, GLOBAL_SERVICES, SPACE_OPTIONAL_METHODS } from "virtual:space-policy";
import { onSpaceGenerationChange, readSelectedSpaceId, selectedSpaceGeneration } from "./space-header";
import { StaleSpaceRequestError } from "./stale-space";

type SpaceInitializer = () => Promise<void>;
let spaceInitializer: SpaceInitializer | null = null;

/** 空间 store 在创建时登记：没有已选空间时，空间内的请求先等它完成空间列表加载，再带上 X-Space-Id。 */
export function setSpaceInitializer(initializer: SpaceInitializer | null): void {
  spaceInitializer = initializer;
  lastSpaceInitFinished = 0; // 换了初始化函数就不再沿用上一个的冷却
}

const SPACE_INIT_TIMEOUT_MS = 10_000;

/**
 * 空间契约三档策略（构建时由 packages/servicecatalog/catalog.yaml 生成，与 Admin Console 同源）：
 * - global：纯全局接口，永不等空间初始化、不注入 X-Space-Id、不参与“切换空间即取消”；
 * - space_optional：空间可选接口，无选中空间时不阻塞；有选中空间且未显式指定空间、请求体未传 global: true 时透传 X-Space-Id，
 *   并在自动注入空间时随切空间取消旧读取；
 * - space_required：空间内接口，无选中空间时等待空间初始化、注入 X-Space-Id，并随切空间取消旧读取。
 */
export type SpacePolicy = "global" | "space_optional" | "space_required";

const ADMIN_API_PATH = /\/api\/admin\/([^/?#]+)\/([^/?#]+)/;

export function spacePolicyForUrl(url: string | undefined): SpacePolicy {
  const match = ADMIN_API_PATH.exec(url || "");
  if (!match) return "space_required";
  const [, service, method] = match;
  if (GLOBAL_SERVICES.has(service)) return "global";
  if (GLOBAL_METHODS[service]?.has(method)) return "global";
  if (SPACE_OPTIONAL_METHODS[service]?.has(method)) return "space_optional";
  return "space_required";
}

function isGlobalPayload(data: unknown): boolean {
  if (typeof data === "string") {
    try {
      const parsed = JSON.parse(data) as Record<string, unknown> | null;
      return Boolean(parsed && parsed.global === true);
    } catch {
      return false;
    }
  }
  return typeof data === "object" && data !== null && (data as Record<string, unknown>).global === true;
}

function shouldSkipSpaceContext(policy: SpacePolicy, data: unknown): boolean {
  return policy === "global" || (policy === "space_optional" && isGlobalPayload(data));
}

let lastSpaceInitFinished = 0;
let spaceInitInflight: Promise<void> | null = null;

async function ensureSpaceSelected(): Promise<void> {
  if (!spaceInitializer || readSelectedSpaceId()) return;
  // 已有在途的初始化：所有并发请求都等它，不能因为冷却而跳过（否则会不带空间头发出去）。
  if (!spaceInitInflight) {
    // 冷却只限制“完成或失败之后”的再次尝试：没有任何空间（或加载失败）时不要让每个请求都重新拉一次空间列表。
    if (Date.now() - lastSpaceInitFinished < 3000) return;
    spaceInitInflight = Promise.race([
      spaceInitializer(),
      new Promise<void>(resolve => setTimeout(resolve, SPACE_INIT_TIMEOUT_MS))
    ])
      .catch(() => undefined) // 空间加载失败时照常发请求，由服务端按缺少空间拒绝并给出提示。
      .finally(() => {
        lastSpaceInitFinished = Date.now();
        spaceInitInflight = null;
      });
  }
  await spaceInitInflight;
}

/** 只读调用：方法名以查询类动词开头。写入类调用的响应必须交给调用方，不能丢。 */
const READ_METHOD = /\/(Get|List|Query|Search|Check|Describe|Count|Validate|Preview|Export)[A-Za-z0-9]*$/;

interface SpaceReadHandle {
  controller: AbortController;
  aborted: boolean;
}

const inflightSpaceReads = new Set<SpaceReadHandle>();

// 切换空间的同一时刻主动取消所有在途的空间读取：旧请求立刻以 StaleSpaceRequestError 结束，
// 调用方的 finally（loading、防重入标志、在途缓存清理）照常执行，且早于新空间的任何响应。
onSpaceGenerationChange(() => {
  for (const handle of [...inflightSpaceReads]) {
    handle.aborted = true;
    handle.controller.abort();
  }
  inflightSpaceReads.clear();
});

export interface SpaceAwareRequestOptions {
  url: string;
  data?: unknown;
  /** 调用方显式指定的空间；指定后不自动注入当前所选空间，也不随切空间取消。 */
  explicitSpaceId?: string;
  signal?: AbortSignal;
}

export interface SpaceAwareRequestContext {
  /** 要写入 X-Space-Id 请求头的空间；为空表示不带该请求头。 */
  spaceId?: string;
  /** 空间内只读请求自动附加的取消信号；其他请求原样透传调用方 signal。 */
  signal?: AbortSignal;
}

/**
 * 为管理台请求准备空间上下文（等待空间初始化、注入 X-Space-Id、切空间取消在途读取与拦截过时响应）。
 */
export async function runWithSpaceContext<T>(
  options: SpaceAwareRequestOptions,
  run: (ctx: SpaceAwareRequestContext) => Promise<T>
): Promise<T> {
  const { url, data, explicitSpaceId, signal: userSignal } = options;
  const policy = spacePolicyForUrl(url);
  if (shouldSkipSpaceContext(policy, data)) {
    return run({ spaceId: explicitSpaceId || undefined, signal: userSignal });
  }
  if (explicitSpaceId) {
    return run({ spaceId: explicitSpaceId, signal: userSignal });
  }
  if (policy === "space_required" && !readSelectedSpaceId()) {
    await ensureSpaceSelected();
  }
  const spaceId = readSelectedSpaceId();
  if (!spaceId || !READ_METHOD.test(url)) {
    return run({ spaceId: spaceId || undefined, signal: userSignal });
  }

  const generation = selectedSpaceGeneration();
  const controller = new AbortController();
  const handle: SpaceReadHandle = { controller, aborted: false };
  const onUserAbort = () => controller.abort();
  userSignal?.addEventListener("abort", onUserAbort, { once: true });
  inflightSpaceReads.add(handle);

  const isStale = () => handle.aborted || readSelectedSpaceId() !== spaceId || selectedSpaceGeneration() !== generation;
  const cancelled = new Promise<never>((_, reject) => {
    controller.signal.addEventListener("abort", () => handle.aborted && reject(new StaleSpaceRequestError()), { once: true });
  });

  const request = run({ spaceId, signal: controller.signal });
  // 切空间竞速先失败时吞掉迟到请求的未处理拒绝，避免产生 unhandledRejection
  void request.catch(() => undefined);

  try {
    const result = await Promise.race([request, cancelled]);
    if (isStale()) throw new StaleSpaceRequestError();
    return result;
  } catch (error) {
    if (error instanceof StaleSpaceRequestError || isStale()) throw new StaleSpaceRequestError();
    throw error;
  } finally {
    inflightSpaceReads.delete(handle);
    userSignal?.removeEventListener("abort", onUserAbort);
  }
}
