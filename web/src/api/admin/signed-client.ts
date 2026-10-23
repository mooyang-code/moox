import { AxiosHeaders, type AxiosInstance, type AxiosRequestConfig, type InternalAxiosRequestConfig } from "axios";
import { clearSigningSessions, createNonce, loadSigningSession, signRequest } from "@/utils/request-signing";
import { onSpaceGenerationChange, readSelectedSpaceId, selectedSpaceGeneration } from "./space-header";
import { AuthSessionExpiredError } from "./auth-errors";
import { StaleSpaceRequestError } from "./stale-space";

interface PersistedAuth {
  token?: string;
  sessionId?: string;
  expiresAt?: number;
}

export function readPersistedAuth(): PersistedAuth {
  try {
    return JSON.parse(localStorage.getItem("user-info") || "{}");
  } catch {
    return {};
  }
}

export async function clearBrowserSession(): Promise<void> {
  localStorage.removeItem("user-info");
  await clearSigningSessions();
}

export async function expireBrowserSession(message?: string): Promise<AuthSessionExpiredError> {
  await clearBrowserSession();
  if (window.location.hash !== "#/login") window.location.hash = "#/login";
  return new AuthSessionExpiredError(message);
}

async function signConfig(config: InternalAxiosRequestConfig): Promise<InternalAxiosRequestConfig> {
  const auth = readPersistedAuth();
  const now = Math.floor(Date.now() / 1000);
  const session = auth.sessionId ? await loadSigningSession(auth.sessionId) : null;
  // 等待签名密钥期间用户可能已经重新登录：旧会话的读取结果不能用来清除或签署新会话，改用最新会话重来
  if (readPersistedAuth().sessionId !== auth.sessionId) return signConfig(config);
  if (!auth.token || !auth.sessionId || !auth.expiresAt || auth.expiresAt <= now || !session) {
    throw await expireBrowserSession();
  }

  const body = config.data == null ? "" : typeof config.data === "string" ? config.data : JSON.stringify(config.data);
  config.data = body;
  const url = new URL(config.url || "/", window.location.origin);
  const timestamp = now;
  const nonce = createNonce();
  const signature = await signRequest(session.key, {
    method: config.method || "GET",
    path: url.pathname,
    body,
    headers: {
      "X-App-Id": config.headers.get("X-App-Id"),
      "X-App-Key": config.headers.get("X-App-Key"),
      "X-Space-Id": config.headers.get("X-Space-Id")
    },
    timestamp,
    nonce
  });
  config.authSessionId = auth.sessionId; // 记录请求所属会话，迟到的旧会话 401 不能清除新登录的会话
  config.headers.Authorization = auth.token;
  config.headers["X-Access-Token"] = auth.token;
  config.headers["X-Moox-Timestamp"] = String(timestamp);
  config.headers["X-Moox-Nonce"] = nonce;
  config.headers["X-Moox-Signature"] = signature;
  return config;
}

/** 请求发出时的会话已经被重新登录替换：它的 401 只说明旧会话失效，不能清除当前会话。 */
function belongsToReplacedSession(config: { authSessionId?: string } | undefined): boolean {
  const requestSession = config?.authSessionId;
  const currentSession = readPersistedAuth().sessionId;
  return Boolean(requestSession && currentSession && requestSession !== currentSession);
}

export function installSignedClient(client: AxiosInstance): void {
  client.interceptors.request.use(signConfig);
  client.interceptors.response.use(undefined, async error => {
    if (error?.response?.status === 401 && !belongsToReplacedSession(error?.config)) {
      await expireBrowserSession();
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

// 登录、空间列表本身不依赖已选空间，也不能等它（空间列表请求就是初始化的一部分，等自己会死锁）。
function independentOfSelectedSpace(url: string | undefined): boolean {
  return /\/api\/admin\/(auth|space)\//.test(url || "");
}

/** 与空间无关的全局服务（仅全局管理员可用，没有空间归属）：不参与“切换空间即取消”，否则全局单例读取会被无故打断。 */
const GLOBAL_SERVICE =
  /\/api\/admin\/(?:(?:auth|space|ssh|secret|sysdeploy|setup|monitor)\/|storage\/(?:ListSpaces|GetDataNode|ListDataNodes)$|cloudfunction\/(?:ListCloudAccounts|GetCloudAccount|ListCloudRegions)$)/;

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
    /** 签名时记录的登录会话 ID。 */
    authSessionId?: string;
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
  return Boolean(config?.injectedSpaceId) && READ_METHOD.test(config?.url || "") && !GLOBAL_SERVICE.test(config?.url || "");
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
 *  不依赖请求内部（IndexedDB 读取、签名等）何时结束，也就不会晚于新空间的任何响应。 */
function runSpaceRead<T>(
  url: string,
  config: AxiosRequestConfig | undefined,
  run: (config: AxiosRequestConfig) => Promise<T>,
  afterInit = false
): Promise<T> {
  const spaceId = readSelectedSpaceId();
  // 还没有选中空间：先等空间初始化，在确定有效空间之后再登记取消，避免初始化期间发起的读取漏登。
  const target = config?.url ?? url;
  if (
    !spaceId &&
    !afterInit &&
    spaceInitializer &&
    READ_METHOD.test(target) &&
    !GLOBAL_SERVICE.test(target) &&
    !hasExplicitSpace(config)
  ) {
    return ensureSpaceSelected().then(() => runSpaceRead(url, config, run, true));
  }
  const tracked: AxiosRequestConfig = { ...config, url: config?.url ?? url } as AxiosRequestConfig;
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

export function installSpaceAwareSignedClient(client: AxiosInstance): void {
  // 测试替身可能只实现拦截器；真实 Axios 实例才有这些方法
  if (typeof client.post === "function") {
    const post = client.post.bind(client);
    client.post = ((url: string, data?: unknown, config?: AxiosRequestConfig) =>
      runSpaceRead(url, config, c => (c === undefined ? post(url, data) : post(url, data, c)))) as typeof client.post;
  }
  if (typeof client.get === "function") {
    const get = client.get.bind(client);
    client.get = ((url: string, config?: AxiosRequestConfig) =>
      runSpaceRead(url, config, c => (c === undefined ? get(url) : get(url, c)))) as typeof client.get;
  }
  if (typeof client.request === "function") {
    const request = client.request.bind(client);
    client.request = ((config: AxiosRequestConfig) =>
      runSpaceRead(config.url || "", config, c => request(c))) as typeof client.request;
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
  // Axios executes request interceptors in reverse registration order.
  installSignedClient(client);
  client.interceptors.request.use(async config => {
    if (!config.headers.get("X-Space-Id") && !independentOfSelectedSpace(config.url)) await ensureSpaceSelected();
    const spaceId = readSelectedSpaceId();
    if (spaceId && !config.headers.get("X-Space-Id")) {
      config.headers.set("X-Space-Id", spaceId);
      // 调用点（下面的包装）已记录发起时的空间与代次；这里只补上没有经过包装的请求
      config.injectedSpaceId ??= spaceId;
      config.injectedSpaceGeneration ??= selectedSpaceGeneration();
    }
    return config;
  });
}
