import type { AxiosInstance, InternalAxiosRequestConfig } from "axios";
import { clearSigningSessions, createNonce, loadSigningSession, signRequest } from "@/utils/request-signing";
import { readSelectedSpaceId } from "./space-header";
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
}

const SPACE_INIT_TIMEOUT_MS = 10_000;

// 登录、空间列表本身不依赖已选空间，也不能等它（空间列表请求就是初始化的一部分，等自己会死锁）。
function independentOfSelectedSpace(url: string | undefined): boolean {
  return /\/api\/admin\/(auth|space)\//.test(url || "");
}

let lastSpaceInitAttempt = 0;

async function ensureSpaceSelected(): Promise<void> {
  if (!spaceInitializer || readSelectedSpaceId()) return;
  // 没有任何空间（或加载失败）时不要让每个请求都重新拉一次空间列表。
  if (Date.now() - lastSpaceInitAttempt < 3000) return;
  lastSpaceInitAttempt = Date.now();
  try {
    await Promise.race([spaceInitializer(), new Promise<void>(resolve => setTimeout(resolve, SPACE_INIT_TIMEOUT_MS))]);
  } catch {
    // 空间加载失败时照常发请求，由服务端按缺少空间拒绝并给出提示。
  }
}

export function installSpaceAwareSignedClient(client: AxiosInstance): void {
  // Axios executes request interceptors in reverse registration order.
  installSignedClient(client);
  client.interceptors.request.use(async config => {
    if (!config.headers.get("X-Space-Id") && !independentOfSelectedSpace(config.url)) await ensureSpaceSelected();
    const spaceId = readSelectedSpaceId();
    if (spaceId && !config.headers.get("X-Space-Id")) {
      config.headers.set("X-Space-Id", spaceId);
    }
    return config;
  });
}
