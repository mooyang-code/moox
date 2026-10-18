// Canonical time-series frequencies, mirroring packages/frequency. Storage
// accepts only these values; aliases are normalized at input boundaries.
export const FREQUENCIES = ["30s", "1m", "5m", "15m", "30m", "1h", "4h", "1d", "1w", "1mo"] as const;

export type Frequency = (typeof FREQUENCIES)[number];

// "1M" is a month (exchange convention) while "1m" is a minute.
const ALIASES: Record<string, Frequency> = {
  "30S": "30s",
  "1H": "1h",
  "60m": "1h",
  "60M": "1h",
  "4H": "4h",
  "240m": "4h",
  "1D": "1d",
  "1W": "1w",
  "1M": "1mo",
  "1MO": "1mo",
  "1Mo": "1mo"
};

export function isCanonicalFrequency(value: string): value is Frequency {
  return (FREQUENCIES as readonly string[]).includes(value);
}

/** Returns the canonical frequency for value, or the trimmed value when it is not a frequency. */
export function normalizeFrequency(value: string): string {
  const trimmed = value.trim();
  if (isCanonicalFrequency(trimmed)) return trimmed;
  return ALIASES[trimmed] ?? trimmed;
}
