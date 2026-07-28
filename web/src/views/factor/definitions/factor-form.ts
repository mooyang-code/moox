export function validateFactorParamsJSON(raw: string): string {
  const normalized = raw.trim() || "{}";
  const params: unknown = JSON.parse(normalized);
  if (!params || Array.isArray(params) || typeof params !== "object") {
    throw new TypeError("params must be a JSON object");
  }
  return normalized;
}
