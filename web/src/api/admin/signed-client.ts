import type { AxiosInstance, InternalAxiosRequestConfig } from 'axios';
import { clearSigningSessions, createNonce, loadSigningSession, signRequest } from '@/utils/request-signing';

interface PersistedAuth {
  token?: string;
  sessionId?: string;
  expiresAt?: number;
}

export function readPersistedAuth(): PersistedAuth {
  try {
    return JSON.parse(localStorage.getItem('user-info') || '{}');
  } catch {
    return {};
  }
}

export async function clearBrowserSession(): Promise<void> {
  localStorage.removeItem('user-info');
  await clearSigningSessions();
}

async function signConfig(config: InternalAxiosRequestConfig): Promise<InternalAxiosRequestConfig> {
  const auth = readPersistedAuth();
  const now = Math.floor(Date.now() / 1000);
  const session = auth.sessionId ? await loadSigningSession(auth.sessionId) : null;
  if (!auth.token || !auth.sessionId || !auth.expiresAt || auth.expiresAt <= now || !session) {
    await clearBrowserSession();
    throw new Error('登录态已失效，请重新登录');
  }

  const body = config.data == null ? '' : typeof config.data === 'string' ? config.data : JSON.stringify(config.data);
  config.data = body;
  const url = new URL(config.url || '/', window.location.origin);
  const timestamp = now;
  const nonce = createNonce();
  const signature = await signRequest(session.key, { method: config.method || 'GET', path: url.pathname, body, timestamp, nonce });
  config.headers.Authorization = auth.token;
  config.headers['X-Access-Token'] = auth.token;
  config.headers['X-Moox-Timestamp'] = String(timestamp);
  config.headers['X-Moox-Nonce'] = nonce;
  config.headers['X-Moox-Signature'] = signature;
  return config;
}

export function installSignedClient(client: AxiosInstance): void {
  client.interceptors.request.use(signConfig);
  client.interceptors.response.use(undefined, async (error) => {
    if (error?.response?.status === 401) {
      await clearBrowserSession();
      if (window.location.hash !== '#/login') window.location.hash = '#/login';
    }
    return Promise.reject(error);
  });
}
