export interface Submission {
  /** 提交仍然有效：弹窗没有被关闭/重新打开，组件也尚未卸载（空间内页面切空间时会卸载）。 */
  isCurrent(): boolean;
}

/**
 * 弹窗/表单的提交作用域。写请求返回后的收尾（关闭弹窗、emit、导航、loading 回写）必须先确认提交仍然有效，
 * 否则迟到的旧响应会关闭用户重新打开的新弹窗、清空新草稿，或把已卸载页面的结果继续作用到界面。
 *
 * 用法：
 *   const scope = createSubmitScope();
 *   watch(visible, () => scope.invalidate());   // 打开/关闭都使进行中的提交失效
 *   onBeforeUnmount(() => scope.invalidate());  // 卸载（含空间内页面切空间重建）使进行中的提交失效
 *   const submission = scope.begin();
 *   await save();
 *   if (!submission.isCurrent()) return false;  // 已经保存成功，但不再操作当前界面
 */
export function createSubmitScope() {
  let token = 0;
  return {
    invalidate() {
      token += 1;
    },
    begin(): Submission {
      const captured = token;
      return {
        isCurrent: () => captured === token
      };
    }
  };
}
