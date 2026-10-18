import { selectedSpaceGeneration } from "@/api/admin/space-header";

export interface Submission {
  /** 提交仍然有效：弹窗没有被关闭/重新打开，且空间没有切换过（含 A→B→A）。 */
  isCurrent(): boolean;
}

export interface SubmitScopeOptions {
  /** 全局资源（没有空间归属）：切换空间不使提交失效，只受弹窗打开/关闭序号约束。 */
  global?: boolean;
}

/**
 * 弹窗/表单的提交作用域。写请求返回后的收尾（关闭弹窗、emit、导航、loading 回写）必须先确认提交仍然有效，
 * 否则迟到的旧响应会关闭用户重新打开的新弹窗、清空新草稿，或把旧空间的结果带进新空间。
 *
 * 用法：
 *   const scope = createSubmitScope();
 *   watch(visible, () => scope.invalidate());   // 打开/关闭都使进行中的提交失效
 *   const submission = scope.begin();
 *   await save();
 *   if (!submission.isCurrent()) return false;  // 已经保存成功，但不再操作当前界面
 */
export function createSubmitScope(options: SubmitScopeOptions = {}) {
  let token = 0;
  return {
    invalidate() {
      token += 1;
    },
    begin(): Submission {
      const captured = token;
      const generation = selectedSpaceGeneration();
      return {
        isCurrent: () => captured === token && (options.global || generation === selectedSpaceGeneration())
      };
    }
  };
}
