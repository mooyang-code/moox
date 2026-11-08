/** 移出标签成员前的说明：引用这个标签的采集任务会跟着缩小采集范围。 */
export function removeMembersConfirmText(count: number): string {
  return `将移出 ${count} 个对象。引用这个标签的采集任务下一周期起不再采集它们。`;
}
