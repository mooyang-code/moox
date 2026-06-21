export type RetInfoCode = number | string | null | undefined;

const successCodes = new Set<RetInfoCode>([0, '0', 200, '200', 'SUCCESS']);
const authExpiredCodes = new Set<RetInfoCode>([
  3,
  '3',
  401,
  '401',
  'TOKEN_EXPIRED',
  'UNAUTHORIZED',
  'AUTH_EXPIRED',
]);

export function isRetInfoSuccess(code: RetInfoCode): boolean {
  return successCodes.has(code);
}

export function isAuthExpiredCode(code: RetInfoCode): boolean {
  return authExpiredCodes.has(code);
}
