import { flushPromises, mount } from "@vue/test-utils";
import { reactive } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ControlRequestError } from "@/api/admin/http";
import { AuthSessionExpiredError } from "@/api/admin/auth-errors";

const mocks = vi.hoisted(() => ({
  getTaskDetail: vi.fn(),
  getTaskList: vi.fn(),
  routeQuery: {} as Record<string, unknown>,
  selectedSpaceId: undefined as { value: string } | undefined,
  routerReplace: vi.fn()
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => {
    resolve = done;
  });
  return { promise, resolve };
}

vi.mock("@/api/collector", () => ({
  GetTaskDetail: mocks.getTaskDetail,
  GetTaskList: mocks.getTaskList
}));

vi.mock("@/store/modules/space", async () => {
  const { ref } = await import("vue");
  mocks.selectedSpaceId = ref("crypto");
  return {
    useSpaceStore: () => ({
      get selectedSpaceId() {
        return mocks.selectedSpaceId?.value || "";
      }
    })
  };
});

vi.mock("vue-router", () => ({
  useRoute: () => ({ query: mocks.routeQuery }),
  useRouter: () => ({ replace: mocks.routerReplace })
}));

import TaskResultsPage from "./index.vue";

describe("collector task result page deep links", () => {
  beforeEach(() => {
    mocks.getTaskList.mockReset().mockResolvedValue({
      tasks: [
        {
          task_id: "known",
          task_name: "Known task",
          create_time: "2026-10-01T00:00:00Z",
          result: { view_id: "view-known", status: "active" }
        }
      ],
      page: { page: 1, size: 100, total: 1 }
    });
    mocks.getTaskDetail.mockReset();
    mocks.routeQuery = reactive({ tab: "results", resultTask: "missing" });
    mocks.routerReplace.mockReset();
    if (mocks.selectedSpaceId) mocks.selectedSpaceId.value = "crypto";
  });

  it("keeps an invalid deep link from rendering another task's View", async () => {
    mocks.getTaskDetail.mockRejectedValue(new Error("task not found"));
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div class='alert'><slot /></div>" },
          "a-button": true,
          "a-empty": { template: "<div class='empty'><slot /><slot name='extra' /></div>" },
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: "<div data-testid='view-browse' />" }
        }
      }
    });
    await flushPromises();

    expect(mocks.getTaskList).toHaveBeenCalledWith({ space_id: "crypto", page: { page: 1, size: 100 } });
    expect(mocks.getTaskDetail).toHaveBeenCalledWith({ space_id: "crypto", task_id: "missing" });
    expect(wrapper.text()).toContain("task not found");
    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(false);
    wrapper.unmount();
  });

  it("shows an invalid deep-link error when the task list is empty", async () => {
    mocks.getTaskList.mockResolvedValue({ tasks: [], page: { page: 1, size: 100, total: 0 } });
    mocks.getTaskDetail.mockRejectedValue(new Error("task not found"));
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div class='alert'><slot /></div>" },
          "a-button": true,
          "a-empty": { template: "<div class='empty'><slot /><slot name='extra' /></div>" },
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: "<div data-testid='view-browse' />" }
        }
      }
    });
    await flushPromises();

    expect(wrapper.text()).toContain("task not found");
    expect(wrapper.find(".empty").exists()).toBe(false);
    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(false);
    wrapper.unmount();
  });

  it("renders only the View returned for a valid deep-linked task", async () => {
    mocks.routeQuery.resultTask = "known";
    mocks.getTaskDetail.mockResolvedValue({
      task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
    });
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": true,
          "a-button": true,
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { props: ["viewIds"], template: "<div data-testid='view-browse'>{{ viewIds.join(',') }}</div>" }
        }
      }
    });
    await flushPromises();

    expect(mocks.getTaskDetail).toHaveBeenCalledWith({ space_id: "crypto", task_id: "known" });
    expect(wrapper.get("[data-testid='view-browse']").text()).toBe("view-known");
    wrapper.unmount();
  });

  it("renders the local task list while the selected task detail is pending", async () => {
    mocks.routeQuery.resultTask = "known";
    const detail = deferred<{ task: { task_id: string; task_name: string } }>();
    mocks.getTaskDetail.mockReturnValue(detail.promise);
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": true,
          "a-button": true,
          "a-empty": true,
          "a-pagination": true,
          "a-spin": {
            props: ["loading"],
            template: '<div class="spin" :data-loading="String(loading)"><slot /></div>'
          },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: true
        }
      }
    });
    await flushPromises();

    expect(mocks.getTaskDetail).toHaveBeenCalledWith({ space_id: "crypto", task_id: "known" });
    expect(wrapper.text()).toContain("共 1 个任务");
    expect(wrapper.get(".spin").attributes("data-loading")).toBe("false");

    detail.resolve({ task: { task_id: "known", task_name: "Known task" } });
    await flushPromises();
    wrapper.unmount();
  });

  it("rejects task detail metadata with a different task identity", async () => {
    mocks.routeQuery = reactive({ tab: "results", resultTask: "known" });
    mocks.getTaskDetail.mockResolvedValue({
      task: { task_id: "other", task_name: "Other task", result: { view_id: "view-other", status: "ready" } }
    });
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div><slot /></div>" },
          "a-button": true,
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: "<div data-testid='view-browse' />" }
        }
      }
    });
    await flushPromises();

    expect(wrapper.text()).toContain("任务结果身份与请求不一致");
    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(false);
    wrapper.unmount();
  });

  it("refreshes the selected View rows together with its result metadata", async () => {
    mocks.routeQuery.resultTask = "known";
    mocks.getTaskDetail.mockResolvedValue({
      task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
    });
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": true,
          "a-button": {
            emits: ["click"],
            props: ["disabled", "loading"],
            template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
          },
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: {
            name: "ViewBrowse",
            props: ["viewIds", "refreshKey"],
            template: '<div data-testid="view-browse">{{ viewIds.join(",") }}:{{ refreshKey }}</div>'
          }
        }
      }
    });
    await flushPromises();

    expect(wrapper.get("[data-testid='view-browse']").text()).toBe("view-known:0");
    const refresh = wrapper.findAll("button").find(button => button.text().includes("刷新结果"));
    expect(refresh).toBeDefined();
    await refresh?.trigger("click");
    await flushPromises();

    expect(mocks.getTaskDetail).toHaveBeenCalledTimes(2);
    expect(wrapper.get("[data-testid='view-browse']").text()).toBe("view-known:1");
    wrapper.unmount();
  });

  it.each(["stale", "unknown"])("keeps an already displayed View mounted when refreshed metadata is %s", async status => {
    mocks.routeQuery.resultTask = "known";
    mocks.getTaskDetail
      .mockResolvedValueOnce({
        task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
      })
      .mockResolvedValueOnce({
        task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status } }
      });
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div><slot /></div>" },
          "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: {
            props: ["refreshKey"],
            template: '<div data-testid="view-browse">{{ refreshKey }}</div>'
          }
        }
      }
    });
    await flushPromises();
    const initialView = wrapper.get("[data-testid='view-browse']").element;
    expect(wrapper.get("[data-testid='view-browse']").text()).toBe("0");
    await wrapper
      .findAll("button")
      .find(button => button.text().includes("刷新结果"))
      ?.trigger("click");
    await flushPromises();
    expect(wrapper.get("[data-testid='view-browse']").element).toBe(initialView);
    expect(wrapper.get("[data-testid='view-browse']").text()).toBe("0");
    expect(wrapper.text()).toContain("结果状态待刷新");
    wrapper.unmount();
  });

  it("keeps the last View mounted when refreshing task metadata fails", async () => {
    mocks.routeQuery.resultTask = "known";
    mocks.routeQuery.tab = "results";
    mocks.getTaskList.mockResolvedValue({
      tasks: [{ task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }],
      page: { page: 1, size: 100, total: 1 }
    });
    mocks.getTaskDetail
      .mockResolvedValueOnce({
        task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
      })
      .mockRejectedValueOnce(new Error("Storage is temporarily unavailable"));
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div><slot /></div>" },
          "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: '<div data-testid="view-browse">view-known</div>' }
        }
      }
    });
    await flushPromises();
    const initialView = wrapper.get("[data-testid='view-browse']").element;
    await wrapper
      .findAll("button")
      .find(button => button.text().includes("刷新结果"))
      ?.trigger("click");
    await flushPromises();

    expect(wrapper.get("[data-testid='view-browse']").element).toBe(initialView);
    expect(wrapper.text()).toContain("Storage is temporarily unavailable");
    wrapper.unmount();
  });

  it("clears the last View when task access is permanently rejected", async () => {
    mocks.routeQuery.resultTask = "known";
    mocks.getTaskList.mockResolvedValue({
      tasks: [{ task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }],
      page: { page: 1, size: 100, total: 1 }
    });
    mocks.getTaskDetail
      .mockResolvedValueOnce({
        task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
      })
      .mockRejectedValueOnce(
        new ControlRequestError("task no longer exists", {
          ret_info: { code: 5, msg: "task no longer exists" }
        })
      );
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div><slot /></div>" },
          "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: '<div data-testid="view-browse">view-known</div>' }
        }
      }
    });
    await flushPromises();
    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(true);

    await wrapper
      .findAll("button")
      .find(button => button.text().includes("刷新结果"))
      ?.trigger("click");
    await flushPromises();

    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(false);
    expect(wrapper.text()).toContain("task no longer exists");
    wrapper.unmount();
  });

  it("clears the last View when the browser session expires", async () => {
    mocks.routeQuery.resultTask = "known";
    mocks.getTaskList.mockResolvedValue({
      tasks: [{ task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }],
      page: { page: 1, size: 100, total: 1 }
    });
    mocks.getTaskDetail
      .mockResolvedValueOnce({
        task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
      })
      .mockRejectedValueOnce(new AuthSessionExpiredError());
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div><slot /></div>" },
          "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: '<div data-testid="view-browse">view-known</div>' }
        }
      }
    });
    await flushPromises();
    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(true);

    await wrapper
      .findAll("button")
      .find(button => button.text().includes("刷新结果"))
      ?.trigger("click");
    await flushPromises();

    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(false);
    expect(wrapper.text()).toContain("登录态已失效，请重新登录");
    wrapper.unmount();
  });

  it("clears the last View when task detail identity does not match the request", async () => {
    mocks.routeQuery.resultTask = "known";
    mocks.getTaskList.mockResolvedValue({
      tasks: [{ task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }],
      page: { page: 1, size: 100, total: 1 }
    });
    mocks.getTaskDetail
      .mockResolvedValueOnce({
        task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
      })
      .mockResolvedValueOnce({
        task: { task_id: "other", task_name: "Other task", result: { view_id: "view-other", status: "ready" } }
      });
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": { template: "<div><slot /></div>" },
          "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: '<div data-testid="view-browse">view-known</div>' }
        }
      }
    });
    await flushPromises();

    await wrapper
      .findAll("button")
      .find(button => button.text().includes("刷新结果"))
      ?.trigger("click");
    await flushPromises();

    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(false);
    expect(wrapper.text()).toContain("任务结果身份与请求不一致");
    wrapper.unmount();
  });

  it("restores an explicit task selection when the resultTask query is cleared", async () => {
    mocks.routeQuery = reactive({ tab: "results", resultTask: "known" });
    mocks.getTaskDetail.mockResolvedValue({
      task: { task_id: "known", task_name: "Known task", result: { view_id: "view-known", status: "ready" } }
    });
    const wrapper = mount(TaskResultsPage, {
      global: {
        stubs: {
          "a-alert": true,
          "a-button": true,
          "a-empty": true,
          "a-pagination": true,
          "a-spin": { template: "<div><slot /></div>" },
          "a-tab-pane": true,
          "a-tabs": { template: "<div><slot /></div>" },
          ViewBrowse: { template: "<div data-testid='view-browse' />" }
        }
      }
    });
    await flushPromises();

    delete mocks.routeQuery.resultTask;
    await flushPromises();

    expect(mocks.routerReplace).toHaveBeenCalledWith({
      path: "/collector/tasks",
      query: { tab: "results", resultTask: "known" }
    });
    expect(mocks.getTaskDetail).toHaveBeenCalledTimes(2);
    expect(wrapper.find("[data-testid='view-browse']").exists()).toBe(true);
    wrapper.unmount();
  });
});
