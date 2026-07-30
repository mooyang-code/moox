import { describe, expect, it } from "vitest";

import { remoteBrowserLaunchArgs } from "../playwright.config";

describe("remoteBrowserLaunchArgs", () => {
  it("maps the configured public host to the local SSH forward", () => {
    expect(remoteBrowserLaunchArgs("106.53.107.122")).toEqual(["--host-resolver-rules=MAP 106.53.107.122 127.0.0.1"]);
  });

  it("rejects values that could inject another Chromium argument", () => {
    expect(() => remoteBrowserLaunchArgs("host.test,EXCLUDE example.com")).toThrow("remote_playwright_forward_host_invalid");
  });
});
