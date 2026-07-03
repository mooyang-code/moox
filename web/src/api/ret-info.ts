export type RetInfoCode = number | null | undefined;

const successCodes = new Set<RetInfoCode>([0]);
const authExpiredCodes = new Set<RetInfoCode>([2]);

export function isRetInfoSuccess(code: RetInfoCode): boolean {
  return successCodes.has(code);
}

export function isAuthExpiredCode(code: RetInfoCode): boolean {
  return authExpiredCodes.has(code);
}
