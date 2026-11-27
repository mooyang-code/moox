import { AxiosHeaders, type AxiosInstance, type AxiosRequestConfig, type InternalAxiosRequestConfig } from "axios";
import { onSpaceGenerationChange, readSelectedSpaceId, selectedSpaceGeneration } from "./space-header";
import { AuthSessionExpiredError } from "./auth-errors";
import { StaleSpaceRequestError } from "./stale-space";

// 登录会话在 HttpOnly Cookie 里，页面脚本读不到也不用管：同源请求由浏览器自动带上。
// 这里只做两件事：服务端说会话失效（401）时回到登录页；切换空间时取消旧空间的在途读取。

let sessionExpiredListener: (() => void) | null = null;

/** 用户信息 store 在创建时登记：会话失效时清掉页面里的账号，路由守卫才不会把人从登录页推回首页。 */
export function setSessionExpiredListener(listener: (() => void) | null): void {
  sessionExpiredListener = listener;
}

/** 会话过期：清掉页面里的账号并带着当前地址去登录页，登录后回到原页面。 */
export function expireBrowserSession(message?: string): AuthSessionExpiredError {
  sessionExpiredListener?.();
  const current = window.location.hash.replace(/^#/, "");
  if (!current.startsWith("/login")) {
    window.location.hash = current && current !== "/" ? `#/login?redirect=${encodeURIComponent(current)}` : "#/login";
  }
  return new AuthSessionExpiredError(message);
}

/** 只有 401 算会话失效；403（来源不对）、429 等照常报错，不跳登录页。登录探测（authProbe）的 401 由调用方处理。 */
function installSessionExpiry(client: AxiosInstance): void {
  client.interceptors.response.use(undefined, error => {
    if (error?.response?.status === 401 && !error?.config?.authProbe) {
      expireBrowserSession();
    }
    return Promise.reject(error);
  });
}

type SpaceInitializer = () => Promise<void>;
let spaceInitializer: SpaceInitializer | null = null;

/** 空间 store 在创建时登记：没有已选空间时，空间内的请求先等它完成空间列表加载，再带上 X-Space-Id。 */
export function setSpaceInitializer(initializer: SpaceInitializer | null): void {
  spaceInitializer = initializer;
  lastSpaceInitFinished = 0; // 换了初始化函数就不再沿用上一个的冷却
}

const SPACE_INIT_TIMEOUT_MS = 10_000;

/**
 * 空间契约三档策略（与 Admin Console policy.go 1:1 对齐，决策 D7）：
 * - global：纯全局接口，永不等空间初始化、不注入 X-Space-Id、不参与“切换空间即取消”；
 * - space_optional：空间可选接口，无选中空间时不阻塞；有选中空间且未显式指定空间、请求体未传 global: true 时透传 X-Space-Id，
 *   并在带了 injectedSpaceId 时随切空间取消旧读取；
 * - space_required：空间内接口，无选中空间时等待空间初始化、注入 X-Space-Id，并随切空间取消旧读取。
 */
export type SpacePolicy = "global" | "space_optional" | "space_required";

const ADMIN_API_PATH = /\/api\/admin\/([^/?#]+)\/([^/?#]+)/;

const GLOBAL_SERVICES = new Set(["auth", "ssh", "secret", "sysdeploy", "monitor", "eventbus"]);

const GLOBAL_METHODS: Record<string, ReadonlySet<string>> = {
  storage: new Set(["ListSpaces", "CreateSpace", "UpdateSpace", "DeleteSpace"]),
  collector: new Set([
    "ListCloudAccounts",
    "CreateCloudAccount",
    "UpdateCloudAccount",
    "DeleteCloudAccount",
    "ListCloudRegions",
    "GetDataTypeConfigs",
    "GetDataTypeConfigWithFields",
    "ListSubjectListers",
    "RegisterDelistNotice",
    "GetLifecycle",
    "ControlLifecycle"
  ]),
  trade: new Set([
    "InspectExchangeCredential",
    "ListBlockedInstruments",
    "UnblockInstrument",
    "ListInstrumentHolders",
    "ListDelists",
    "GetLifecycle",
    "ControlLifecycle"
  ]),
  strategy: new Set(["ListStrategies", "CreateStrategy", "UpdateStrategy", "DeleteStrategy", "GetLifecycle", "ControlLifecycle"]),
  "factor-mgr": new Set([
    "CreateFactor",
    "UpdateFactor",
    "DeleteFactor",
    "ImportFactorCatalog",
    "ValidateFactorSpec",
    "ListFactorFunctions",
    "ListFactorCategories",
    "UpsertFactorCategory",
    "DeleteFactorCategory",
    "SetFactorCategory",
    "GetStatus",
    "PublishUdf",
    "ListUdfs",
    "GetUdf",
    "ActivateUdf",
    "GetLifecycle",
    "ControlLifecycle"
  ]),
  archive: new Set(["GetLifecycle", "ControlLifecycle"])
};

const SPACE_OPTIONAL_METHODS: Record<string, ReadonlySet<string>> = {
  strategy: new Set(["GetStrategy", "ValidateStrategy"]),
  activity: new Set(["ListActivityLogs"]),
  // GetFactor / ListFactors 无空间时可正常读取全局定义，有空间时服务端按 X-Space-Id 区分本空间与其他空间引用计数。
  "factor-mgr": new Set(["ListFactorTasks", "GetFactor", "ListFactors"])
};

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

declare module "axios" {
  interface AxiosRequestConfig {
    /** 登录探测（取用户信息、登录、退出）：401 只说明没有登录，不跳登录页，由调用方处理。 */
    authProbe?: boolean;
    /** 请求拦截器自动注入 X-Space-Id 时记录的空间；调用方显式指定空间的请求不带这个标记。 */
    injectedSpaceId?: string;
    /** 与 injectedSpaceId 同时记录的空间代次，用来识别 A→B→A 之后返回的旧请求。 */
    injectedSpaceGeneration?: number;
    /** 切换空间时可被取消的在途读取登记项。 */
    spaceReadHandle?: SpaceReadHandle;
  }
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

function isSpaceScopedRead(config: Pick<AxiosRequestConfig, "url" | "injectedSpaceId"> | undefined): boolean {
  return Boolean(config?.injectedSpaceId) && READ_METHOD.test(config?.url || "") && spacePolicyForUrl(config?.url) !== "global";
}

function isStaleSpaceRead(config: InternalAxiosRequestConfig | undefined): boolean {
  if (!isSpaceScopedRead(config)) return false;
  return readSelectedSpaceId() !== config?.injectedSpaceId || selectedSpaceGeneration() !== config?.injectedSpaceGeneration;
}

function finishSpaceRead(config: InternalAxiosRequestConfig | undefined): SpaceReadHandle | undefined {
  const handle = config?.spaceReadHandle;
  if (handle) inflightSpaceReads.delete(handle);
  return handle;
}

function hasExplicitSpace(config: AxiosRequestConfig | undefined): boolean {
  return Boolean(config?.headers && new AxiosHeaders(config.headers as never).get("X-Space-Id"));
}

/** 发起时就登记并与取消信号竞速：切换空间的那一刻调用方立即收到 StaleSpaceRequestError，
 *  不依赖请求内部何时结束，也就不会晚于新空间的任何响应。 */
function runSpaceRead<T>(
  url: string,
  data: unknown,
  config: AxiosRequestConfig | undefined,
  run: (config: AxiosRequestConfig) => Promise<T>,
  afterInit = false
): Promise<T> {
  const target = config?.url ?? url;
  const policy = spacePolicyForUrl(target);
  if (shouldSkipSpaceContext(policy, data ?? config?.data)) {
    return run(config as AxiosRequestConfig);
  }
  const spaceId = readSelectedSpaceId();
  // 只有 space_required 在没有选中空间时等空间初始化；space_optional 无空间时不阻塞，直接发无空间头请求。
  if (
    policy === "space_required" &&
    !spaceId &&
    !afterInit &&
    spaceInitializer &&
    READ_METHOD.test(target) &&
    !hasExplicitSpace(config)
  ) {
    return ensureSpaceSelected().then(() => runSpaceRead(url, data, config, run, true));
  }
  const tracked: AxiosRequestConfig = { ...config, url: target } as AxiosRequestConfig;
  if (!spaceId || hasExplicitSpace(config) || !isSpaceScopedRead(Object.assign(tracked, { injectedSpaceId: spaceId }))) {
    return run(config as AxiosRequestConfig); // 原样透传，保持调用形态不变
  }
  const controller = new AbortController();
  const handle: SpaceReadHandle = { controller, aborted: false };
  const userSignal = config?.signal as AbortSignal | undefined;
  userSignal?.addEventListener("abort", () => controller.abort(), { once: true });
  tracked.signal = controller.signal;
  tracked.injectedSpaceId = spaceId;
  tracked.injectedSpaceGeneration = selectedSpaceGeneration();
  tracked.spaceReadHandle = handle;
  inflightSpaceReads.add(handle);
  const cancelled = new Promise<never>((_, reject) => {
    controller.signal.addEventListener("abort", () => handle.aborted && reject(new StaleSpaceRequestError()), { once: true });
  });
  const request = run(tracked);
  void request.then(
    () => inflightSpaceReads.delete(handle),
    () => inflightSpaceReads.delete(handle)
  );
  return Promise.race([request, cancelled]);
}

export function installSpaceAwareClient(client: AxiosInstance): void {
  // 测试替身可能只实现拦截器；真实 Axios 实例才有这些方法
  if (typeof client.post === "function") {
    const post = client.post.bind(client);
    client.post = ((url: string, data?: unknown, config?: AxiosRequestConfig) =>
      runSpaceRead(url, data, config, c => (c === undefined ? post(url, data) : post(url, data, c)))) as typeof client.post;
  }
  if (typeof client.get === "function") {
    const get = client.get.bind(client);
    client.get = ((url: string, config?: AxiosRequestConfig) =>
      runSpaceRead(url, undefined, config, c => (c === undefined ? get(url) : get(url, c)))) as typeof client.get;
  }
  if (typeof client.request === "function") {
    const request = client.request.bind(client);
    client.request = ((config: AxiosRequestConfig) =>
      runSpaceRead(config.url || "", config.data, config, c => request(c))) as typeof client.request;
  }
  client.interceptors.response.use(
    response => {
      finishSpaceRead(response.config);
      return isStaleSpaceRead(response.config) ? Promise.reject(new StaleSpaceRequestError()) : response;
    },
    error => {
      const handle = finishSpaceRead(error?.config);
      if (handle?.aborted || isStaleSpaceRead(error?.config)) return Promise.reject(new StaleSpaceRequestError());
      return Promise.reject(error);
    }
  );
  installSessionExpiry(client);
  client.interceptors.request.use(async config => {
    const policy = spacePolicyForUrl(config.url);
    if (shouldSkipSpaceContext(policy, config.data)) return config;
    if (policy === "space_required" && !config.headers.get("X-Space-Id")) await ensureSpaceSelected();
    const spaceId = readSelectedSpaceId();
    if (spaceId && !config.headers.get("X-Space-Id")) {
      config.headers.set("X-Space-Id", spaceId);
      // 调用点（上面的包装）已记录发起时的空间与代次；这里只补上没有经过包装的请求
      config.injectedSpaceId ??= spaceId;
      config.injectedSpaceGeneration ??= selectedSpaceGeneration();
    }
    return config;
  });
}
