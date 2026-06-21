export interface StorageAuthInfo {
  app_id: string;
  app_key: string;
  operator: string;
  request_id: string;
}

export function getStorageAuthInfo(): StorageAuthInfo {
  return {
    app_id: 'moox_frontend',
    app_key: '2521e0d21b6be0347b72bca93904a0dd',
    operator: 'moox_web',
    request_id: `web-${Date.now()}-${Math.random().toString(16).slice(2)}`,
  };
}
