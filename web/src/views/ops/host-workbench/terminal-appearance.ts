// 终端外观：控制台只有一个使用者，外观不再按主机保存，统一存在浏览器本地。
export interface TerminalAppearance {
  fontSize: number;
  fontFamily: string;
  cursorStyle: "block" | "underline" | "bar";
  background: string;
  foreground: string;
  cursorColor: string;
}

export const APPEARANCE_KEY = "moox.ssh-terminal.appearance";

export const DEFAULT_APPEARANCE: TerminalAppearance = {
  fontSize: 13,
  fontFamily: "Menlo, Consolas, Monaco, 'Courier New', monospace",
  cursorStyle: "block",
  background: "#1e1e1e",
  foreground: "#d4d4d4",
  cursorColor: "#ffffff"
};

const CURSOR_STYLES = ["block", "underline", "bar"];

// 本地存储不可用或内容损坏时回退默认值；只接受类型正确的字段。
export function loadTerminalAppearance(storage: Pick<Storage, "getItem"> | undefined = safeStorage()): TerminalAppearance {
  const appearance = { ...DEFAULT_APPEARANCE };
  try {
    const saved = JSON.parse(storage?.getItem(APPEARANCE_KEY) || "{}") as Partial<Record<keyof TerminalAppearance, unknown>>;
    if (typeof saved.fontSize === "number" && saved.fontSize >= 8 && saved.fontSize <= 36) appearance.fontSize = saved.fontSize;
    if (typeof saved.fontFamily === "string" && saved.fontFamily.trim()) appearance.fontFamily = saved.fontFamily;
    if (typeof saved.cursorStyle === "string" && CURSOR_STYLES.includes(saved.cursorStyle)) {
      appearance.cursorStyle = saved.cursorStyle as TerminalAppearance["cursorStyle"];
    }
    for (const key of ["background", "foreground", "cursorColor"] as const) {
      const value = saved[key];
      if (typeof value === "string" && /^#[0-9a-fA-F]{6}$/.test(value)) appearance[key] = value;
    }
  } catch {
    // 忽略损坏的本地设置
  }
  return appearance;
}

function safeStorage() {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}
