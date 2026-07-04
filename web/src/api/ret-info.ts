export type RetInfoCode = number | string | null | undefined;

const successCodes = new Set<RetInfoCode>([0, '0', 'SUCCESS']);
const authExpiredCodes = new Set<RetInfoCode>([2, '2', 'NO_AUTH']);

export function isRetInfoSuccess(code: RetInfoCode): boolean {
  return successCodes.has(code);
}

export function isAuthExpiredCode(code: RetInfoCode): boolean {
  return authExpiredCodes.has(code);
}
