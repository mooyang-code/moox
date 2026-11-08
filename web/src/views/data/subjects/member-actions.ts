import { Message, Modal } from "@arco-design/web-vue";

/** 移出标签成员前的说明：引用这个标签的采集任务会跟着缩小采集范围。 */
export function removeMembersConfirmText(count: number): string {
  return `将移出 ${count} 个对象。引用这个标签的采集任务下一周期起不再采集它们。`;
}

/**
 * 移出标签会缩小引用它的采集任务的范围，先确认：点“移出”才发请求，取消什么也不做；
 * 失败时确认框保持打开（原因由请求层提示），成功后调用 done 刷新列表。
 */
export function confirmRemoveMembers(count: number, remove: () => Promise<unknown>, done: () => void) {
  Modal.confirm({
    title: `移出 ${count} 个对象？`,
    content: removeMembersConfirmText(count),
    okText: "移出",
    okButtonProps: { status: "danger" },
    cancelText: "取消",
    onBeforeOk: async () => {
      try {
        await remove();
      } catch {
        return false;
      }
      Message.success("已移出标签");
      done();
      return true;
    }
  });
}
