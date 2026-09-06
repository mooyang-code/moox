import { beforeEach, describe, expect, it, vi } from "vitest";

const { client } = vi.hoisted(() => ({ client: { post: vi.fn(), interceptors: { response: { use: vi.fn() } } } }));
vi.mock("axios", () => ({ default: { create: () => client } }));
vi.mock("@/api/gateway", () => ({ gatewayOrigin: () => "http://localhost" }));
vi.mock("../admin/signed-client", () => ({ installSpaceAwareSignedClient: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

import { callTrade } from "./http";

describe("Trade response errors", () => {
  beforeEach(() => client.post.mockReset());

  it("retains the durable action when admission returns a retryable error", async () => {
    const response = {
      ret_info: { code: 1, msg: "market data unavailable" },
      action: { action_id: "same-action", status: "RUNNING" }
    };
    client.post.mockResolvedValue({ data: response });
    await expect(callTrade("console", "SubmitOrder", { action_id: "same-action" }))
      .rejects.toMatchObject({ message: "market data unavailable", response });
  });

  it("returns successful pending acceptance without converting it to a fill", async () => {
    const response = { ret_info: { code: 0, msg: "" }, action: { status: "RUNNING" }, order: { state: "PENDING" } };
    client.post.mockResolvedValue({ data: response });
    await expect(callTrade("console", "SubmitOrder", {})).resolves.toEqual(response);
  });

  it("does not invent acceptance when the response envelope is missing", async () => {
    client.post.mockResolvedValue({ data: {} });
    await expect(callTrade("console", "SubmitOrder", {})).rejects.toThrow("missing ret_info");
  });
});
