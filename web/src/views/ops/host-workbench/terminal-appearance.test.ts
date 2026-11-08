import { describe, expect, it } from "vitest";
import { APPEARANCE_KEY, DEFAULT_APPEARANCE, loadTerminalAppearance } from "./terminal-appearance";

const store = (value?: string) => ({ getItem: (key: string) => (key === APPEARANCE_KEY ? (value ?? null) : null) });

describe("loadTerminalAppearance", () => {
  it("uses defaults when nothing is stored or storage is unavailable", () => {
    expect(loadTerminalAppearance(store())).toEqual(DEFAULT_APPEARANCE);
    expect(loadTerminalAppearance(undefined)).toEqual(DEFAULT_APPEARANCE);
    expect(
      loadTerminalAppearance({
        getItem: () => {
          throw new Error("blocked");
        }
      })
    ).toEqual(DEFAULT_APPEARANCE);
  });

  it("applies valid stored fields and ignores invalid ones", () => {
    const appearance = loadTerminalAppearance(
      store(JSON.stringify({ fontSize: 16, cursorStyle: "bar", background: "#000000", foreground: "red", fontFamily: 3 }))
    );
    expect(appearance).toMatchObject({ fontSize: 16, cursorStyle: "bar", background: "#000000" });
    expect(appearance.foreground).toBe(DEFAULT_APPEARANCE.foreground);
    expect(appearance.fontFamily).toBe(DEFAULT_APPEARANCE.fontFamily);
  });

  it("falls back to defaults for corrupt JSON", () => {
    expect(loadTerminalAppearance(store("{oops"))).toEqual(DEFAULT_APPEARANCE);
  });
});
