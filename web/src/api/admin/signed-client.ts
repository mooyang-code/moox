import type { AxiosInstance, InternalAxiosRequestConfig } from "axios";
import { clearSigningSessions, createNonce, loadSigningSession, signRequest } from "@/utils/request-signing";
import { readSelectedSpaceId, selectedSpaceGeneration } from "./space-header";
import { AuthSessionExpiredError } from "./auth-errors";

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
  config.headers.Authorization = auth.token;
  config.headers["X-Access-Token"] = auth.token;
  config.headers["X-Moox-Timestamp"] = String(timestamp);
  config.headers["X-Moox-Nonce"] = nonce;
  config.headers["X-Moox-Signature"] = signature;
  return config;
}

export function installSignedClient(client: AxiosInstance): void {
  client.interceptors.request.use(signConfig);
  client.interceptors.response.use(undefined, async error => {
    if (error?.response?.status === 401) {
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
    /** 请求拦截器自动注入 X-Space-Id 时记录的空间；调用方显式指定空间的请求不带这个标记。 */
    injectedSpaceId?: string;
    /** 与 injectedSpaceId 同时记录的空间代次，用来识别 A→B→A 之后返回的旧请求。 */
    injectedSpaceGeneration?: number;
  }
}

/** 只读调用：方法名以查询类动词开头。写入类调用的响应必须交给调用方，不能丢。 */
const READ_METHOD = /\/(Get|List|Query|Search|Check|Describe|Count|Validate|Preview|Export)[A-Za-z0-9]*$/;

function isStaleSpaceRead(config: InternalAxiosRequestConfig | undefined): boolean {
  const injected = config?.injectedSpaceId;
  if (!injected || !READ_METHOD.test(config?.url || "")) return false;
  return readSelectedSpaceId() !== injected || selectedSpaceGeneration() !== config?.injectedSpaceGeneration;
}

/** 永不落定的 Promise：旧空间的只读响应（含错误）整体丢弃，页面既不会收到旧数据，也不会弹出多余的错误提示；
 *  新空间的请求由页面自己重新发起并管理 loading 状态。 */
const neverSettles = <T>() => new Promise<T>(() => {});

export function installSpaceAwareSignedClient(client: AxiosInstance): void {
  client.interceptors.response.use(
    response => (isStaleSpaceRead(response.config) ? neverSettles() : response),
    error => (isStaleSpaceRead(error?.config) ? neverSettles() : Promise.reject(error))
  );
  // Axios executes request interceptors in reverse registration order.
  installSignedClient(client);
  client.interceptors.request.use(async config => {
    if (!config.headers.get("X-Space-Id") && !independentOfSelectedSpace(config.url)) await ensureSpaceSelected();
    const spaceId = readSelectedSpaceId();
    if (spaceId && !config.headers.get("X-Space-Id")) {
      config.headers.set("X-Space-Id", spaceId);
      config.injectedSpaceId = spaceId;
      config.injectedSpaceGeneration = selectedSpaceGeneration();
    }
    return config;
  });
}
