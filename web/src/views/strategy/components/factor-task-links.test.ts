import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import FactorTaskLinks from "./factor-task-links.vue";

describe("计算任务链接", () => {
  it("单任务只写 ID，多任务写成 现货 xxx、永续 yyy；每个都链到计算任务详情", () => {
    const single = mount(FactorTaskLinks, { props: { tasks: { swap: "ftask_a" } } });
    expect(single.text()).toBe("ftask_a");
    expect(single.find("a").attributes("href")).toBe("#/factor/tasks?detail=ftask_a");

    const both = mount(FactorTaskLinks, { props: { tasks: { swap: "ftask_b", spot: "ftask_a" } } });
    expect(both.text().replace(/\s+/g, " ")).toBe("现货 ftask_a、永续 ftask_b");
    expect(both.findAll("a").map(item => item.attributes("href"))).toEqual([
      "#/factor/tasks?detail=ftask_a",
      "#/factor/tasks?detail=ftask_b"
    ]);
    expect(mount(FactorTaskLinks, { props: { tasks: {} } }).text()).toBe("-");
  });
});
