export type RetInfoCode = number | string | null | undefined;

const successCodes = new Set<RetInfoCode>([0, "0", "SUCCESS"]);

export function isRetInfoSuccess(code: RetInfoCode): boolean {
  return successCodes.has(code);
}
