import { Message } from "@arco-design/web-vue";

function copyWithExecCommand(value: string): boolean {
  if (typeof document === "undefined" || typeof document.execCommand !== "function" || !document.body) return false;
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, value.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(textarea);
  }
}

/** 复制文本到剪贴板并给出轻量反馈；非安全上下文或权限受限时自动降级到 execCommand。 */
export async function copyText(text: string, label?: string): Promise<boolean> {
  const value = text.trim();
  if (!value) return false;
  const successMessage = label ? `已复制 ${label}` : `已复制 ${value}`;
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      Message.success(successMessage);
      return true;
    } catch {
      // 权限受限或非焦点上下文时尝试 execCommand 降级。
    }
  }
  if (copyWithExecCommand(value)) {
    Message.success(successMessage);
    return true;
  }
  Message.warning("当前环境不支持一键复制，请手动复制");
  return false;
}
