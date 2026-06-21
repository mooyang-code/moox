export const APP_AUTH_INFO = {
  app_id: 'moox_frontend',
  app_key: '2521e0d21b6be0347b72bca93904a0dd',
} as const;

export interface StorageAuthInfo {
  app_id: string;
  app_key: string;
  operator: string;
  request_id: string;
}

export function getAppInfo() {
  return { ...APP_AUTH_INFO };
}

export function appAuthHeaders() {
  return {
    app_id: APP_AUTH_INFO.app_id,
    app_key: APP_AUTH_INFO.app_key,
  };
}

export function getStorageAuthInfo(): StorageAuthInfo {
  return {
    ...APP_AUTH_INFO,
    operator: 'moox_web',
    request_id: `web-${Date.now()}-${Math.random().toString(16).slice(2)}`,
  };
}
