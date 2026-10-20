import { afterEach, describe, expect, it } from "vitest";
import { Message } from "@arco-design/web-vue";
import { installMessageDedupe } from "./message-dedupe";

// 用真实的 Arco Message 验证：同一条文案连续报错只显示一条（而不是每个失败请求各弹一条把页面刷满）。
describe("真实 Arco Message 下的去重", () => {
  afterEach(() => {
    Message.clear();
    document.body.innerHTML = "";
  });

  it("相同文案连续弹 30 次只留下 1 条，不同文案各 1 条", async () => {
    installMessageDedupe();
    for (let i = 0; i < 30; i++) Message.error("Network Error");
    Message.error("另一种错误");
    await new Promise(resolve => setTimeout(resolve, 50));
    const texts = Array.from(document.querySelectorAll(".arco-message")).map(node => node.textContent?.trim());
    expect(texts.filter(text => text === "Network Error")).toHaveLength(1);
    expect(texts.filter(text => text === "另一种错误")).toHaveLength(1);
  });
});
