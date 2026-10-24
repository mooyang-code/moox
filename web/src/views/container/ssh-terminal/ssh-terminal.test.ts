import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  createSSHSession: vi.fn(),
  disconnectSSHSession: vi.fn(),
  resizeSSHTerminal: vi.fn(),
  getSSHWebSocketUrl: vi.fn(),
  query: {} as Record<string, unknown>
}));

vi.mock("@/api/modules/ssh", () => mocks);
vi.mock("vue-router", () => ({ useRoute: () => ({ query: mocks.query }) }));
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: unknown;
    constructor(options: unknown) {
      this.options = options;
    }
    loadAddon() {
      /* 测试桩 */
    }
    open() {
      /* 测试桩 */
    }
    writeln() {
      /* 测试桩 */
    }
    clear() {
      /* 测试桩 */
    }
    dispose() {
      /* 测试桩 */
    }
    onData() {
      return {
        dispose() {
          /* 测试桩 */
        }
      };
    }
  }
}));
vi.mock("@xterm/addon-attach", () => ({ AttachAddon: class {} }));
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {
      /* 测试桩 */
    }
  }
}));
vi.mock("@xterm/xterm/css/xterm.css", () => ({}));
vi.mock("@/views/container/ssh-file-manager/ssh-file-manager.vue", () => ({ default: { template: "<div />" } }));

class FakeWebSocket {
  static OPEN = 1;
  readyState = 1;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {}
  close() {
    /* 测试桩 */
  }
}

import SshTerminal from "./ssh-terminal.vue";

function mountTerminal(props: Record<string, unknown> = {}) {
  return mount(SshTerminal, {
    props,
    global: {
      plugins: [ArcoVue],
      stubs: { "icon-sync": true, "icon-eraser": true, "icon-poweroff": true, "icon-folder": true, "icon-desktop": true }
    }
  });
}

describe("ssh terminal by registered host id", () => {
  let sessions = 0;
  beforeEach(() => {
    sessions = 0;
    mocks.query = {};
    mocks.createSSHSession.mockReset().mockImplementation(async () => ({ session_id: `session-${++sessions}` }));
    mocks.disconnectSSHSession.mockReset().mockResolvedValue({});
    mocks.getSSHWebSocketUrl.mockReset().mockResolvedValue("ws://gateway/ws");
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {
          /* 测试桩 */
        }
        disconnect() {
          /* 测试桩 */
        }
      }
    );
  });

  it("creates the session with the string host id and never fetches a host detail", async () => {
    const wrapper = mountTerminal({ initialHostId: "control" });
    await flushPromises();
    expect(mocks.createSSHSession).toHaveBeenCalledTimes(1);
    expect(mocks.createSSHSession).toHaveBeenCalledWith({ host_id: "control" });
    wrapper.unmount();
  });

  it("resolves the host id from the host_id query parameter", async () => {
    mocks.query = { host_id: "storage" };
    const wrapper = mountTerminal();
    await flushPromises();
    expect(mocks.createSSHSession).toHaveBeenCalledWith({ host_id: "storage" });
    wrapper.unmount();
  });

  it("does not connect without a host id", async () => {
    const wrapper = mountTerminal();
    await flushPromises();
    expect(mocks.createSSHSession).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("reconnects to the same host id and drops the old session", async () => {
    const wrapper = mountTerminal({ initialHostId: "overseas" });
    await flushPromises();
    await vi.waitFor(() => expect(mocks.getSSHWebSocketUrl).toHaveBeenCalled());
    const reconnect = wrapper.findAll("button").find(button => button.text().includes("重连"))!;
    await reconnect.trigger("click");
    await flushPromises();
    expect(mocks.disconnectSSHSession).toHaveBeenCalledWith("session-1");
    expect(mocks.createSSHSession).toHaveBeenCalledTimes(2);
    expect(mocks.createSSHSession).toHaveBeenLastCalledWith({ host_id: "overseas" });
    wrapper.unmount();
  });
});
