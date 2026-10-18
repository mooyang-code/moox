/** 原生部署初始化允许的密码长度上限（字节，bcrypt 的输入上限），登录页必须与之一致，否则已部署的控制台无法通过页面登录。 */
export const PASSWORD_MAX_BYTES = 72;

/** 密码只校验非空与字节长度上限；字符集不限制（初始化允许符号与非 ASCII 字符）。返回错误文案，合法时返回 null。 */
export function validatePasswordInput(value: string): string | null {
  if (!value || value.trim().length === 0) return "请输入密码";
  if (value.includes("\u0000")) return "密码不能包含空字符";
  if (new TextEncoder().encode(value).length > PASSWORD_MAX_BYTES) return `密码长度不能超过 ${PASSWORD_MAX_BYTES} 个字节`;
  return null;
}
