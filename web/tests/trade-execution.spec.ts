import { expect, test } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

// Browser contract tests: every API is intercepted. These are not production
// Paper acceptance tests and must never send an order to a running Trade node.
for (const controlMode of [1, 2]) {
  for (const width of [1440, 390]) {
    test(`order entry mode ${controlMode} at ${width}px`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 950 });
      const posts: { method: string; body: Record<string, unknown> }[] = [];
      const logical = {
        logical_account_id: "paper-ui",
        space_id: "space-1",
        name: "Paper UI",
        control_mode: controlMode,
        execution_mode: 1,
        market_type: 1,
        settlement_asset: "USDT",
        automation_state: "PAUSED",
        pause_reason: "",
        ready: true,
        readiness_reasons: [],
        members: [{ trading_account_id: "paper-member", enabled: true, priority: 0 }]
      };
      let action = {
        action_id: "",
        action_type: controlMode === 2 ? "SUBMIT_ORDER" : "MANUAL_ORDER",
        logical_account_id: logical.logical_account_id,
        status: "RUNNING",
        result_json: '{"order_id":"paper-order"}',
        reason: "ui-test"
      };
      await page.route(
        url => url.pathname.startsWith("/api/admin/"),
        async route => {
          const method = route.request().url().split("/").pop() || "";
          const ok = (data: object = {}) => route.fulfill({ json: { ret_info: { code: 0, msg: "" }, ...data } });
          switch (method) {
            case "GetUserInfo":
              return ok({ user_info: { user_id: "ui", username: "ui", nickname: "UI", role: 3, status: 1 } });
            case "ListSpaces":
              return ok({ spaces: [{ space_id: "space-1", name: "UI", status: "active" }], page_result: { total: 1 } });
            case "ListLogicalAccounts":
              return ok({ logical_accounts: [logical], page_result: { total: 1 } });
            case "GetLogicalAccount":
              return ok({ logical_account: logical });
            case "GetLogicalAccountTarget":
              return ok({});
            case "ListEquitySnapshots":
              return ok({ snapshots: [] });
            case "GetExecutionCapabilities":
              return ok({ capabilities: { can_place_order: true, order_types: [1, 2], fill_policies: [1, 2, 3] } });
            case "PlaceManualOrder":
            case "SubmitOrder": {
              const body = route.request().postDataJSON();
              posts.push({ method, body });
              action = { ...action, action_id: body.action_id };
              return ok({ action, order: { order_id: "paper-order", state: "PENDING" } });
            }
            case "GetOperatorAction":
              return ok({ action: { ...action, status: "COMPLETED" } });
            case "GetOrder":
              return ok({ order: { order_id: "paper-order", state: "FILLED" } });
            default:
              return route.fulfill({ status: 404, json: { ret_info: { code: 5, msg: `unmocked ${method}` } } });
          }
        }
      );
      await installE2ESession(page, "space-1");
      await page.goto("/#/trading/accounts?view=strategy&logical_account_id=paper-ui");
      const label = controlMode === 2 ? "下单" : "接管并下单";
      await expect(page.getByRole("heading", { name: "Paper UI", exact: true })).toBeVisible();
      const drawer = page.locator(".arco-drawer:visible");
      const bounds = await drawer.boundingBox();
      expect(bounds?.x).toBeGreaterThanOrEqual(0);
      expect(bounds?.width).toBeLessThanOrEqual(width);
      await page.getByRole("button", { name: label, exact: true }).click();
      const modal = page.locator(".arco-modal:visible");
      await expect(modal.getByText("市价", { exact: true })).toBeVisible();
      expect(await modal.evaluate(element => parseFloat(getComputedStyle(element).width))).toBeLessThanOrEqual(width);
      await expect(modal.getByText("提交前会暂停整个组合账户并取消活动目标订单。")).toHaveCount(controlMode === 1 ? 1 : 0);
      for (const [field, value] of [
        ["交易标的", "BTC-USDT-SPOT"],
        ["数量", "0.01"],
        ["原因", "ui-test"]
      ]) {
        await modal
          .locator(".arco-form-item")
          .filter({ has: page.locator(".arco-form-item-label", { hasText: field }) })
          .locator("input")
          .fill(value);
      }
      await modal.screenshot({ path: testInfo.outputPath("order-entry.png") });
      await modal.getByRole("button", { name: label, exact: true }).click();
      await expect.poll(() => posts.length).toBe(1);
      expect(posts[0].method).toBe(controlMode === 2 ? "SubmitOrder" : "PlaceManualOrder");
      if (controlMode === 2) expect(posts[0].body.logical_account_id).toBe("paper-ui");
      else expect(posts[0].body).not.toHaveProperty("logical_account_id");
      await expect(page.getByText("提交阶段完成", { exact: true })).toBeVisible();
      await expect(page.getByText("已成交", { exact: true })).toBeVisible();
      await page.getByText("已成交", { exact: true }).scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath("order-result.png"), fullPage: true });
    });
  }
}
