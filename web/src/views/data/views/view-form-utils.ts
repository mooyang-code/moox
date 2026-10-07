
export function freqFromViewFilterJSON(filterJSON?: string) {
  try {
    const parsed = JSON.parse(jsonText(filterJSON)) as { freq?: unknown };
    return typeof parsed.freq === "string" ? parsed.freq.trim() : "";
  } catch {
    return "";
  }
}

function jsonText(value?: string) {
  return value?.trim() || "{}";
}
