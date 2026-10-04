const dayMilliseconds = 24 * 60 * 60 * 1000;

export function rangeToTime(range: number[]) {
  if (range.length !== 2) return {};
  return {
    start_time: String(range[0]),
    end_time: String(range[1] + dayMilliseconds - 1)
  };
}
