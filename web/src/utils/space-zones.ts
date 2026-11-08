/** 空间常用的时区：空间页、顶部新建空间都从这里选，和安装种子 config/setup/metadata.yaml 一致。 */
export const SPACE_TIMEZONES: readonly { value: string; label: string }[] = [
  { value: "UTC", label: "UTC（加密货币）" },
  { value: "Asia/Shanghai", label: "Asia/Shanghai（A 股，UTC+8）" },
  { value: "Asia/Hong_Kong", label: "Asia/Hong_Kong（港股，UTC+8）" },
  { value: "America/New_York", label: "America/New_York（美股，UTC−5/UTC−4）" }
];

/** 时区不在常用列表里时的提示；空串表示是常用时区或还没选。 */
export function uncommonZoneHint(zone: string): string {
  return zone && !SPACE_TIMEZONES.some(item => item.value === zone) ? "不是常用时区，页面时间会按它显示" : "";
}
