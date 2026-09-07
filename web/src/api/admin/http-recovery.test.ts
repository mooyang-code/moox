import { beforeEach, expect, it, vi } from "vitest";

const transport = vi.hoisted(() => ({ post: vi.fn(), responseUse: vi.fn() }));
vi.mock("axios", () => ({ default: { create: () => ({ post: transport.post, interceptors: { response: { use: transport.responseUse } } }) } }));
vi.mock("./signed-client", () => ({ installSpaceAwareSignedClient: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

import { callControl } from "./http";

beforeEach(() => transport.post.mockReset());

it("preserves durable instance identity on a business failure", async () => {
  const response = { ret_info: { code: 1, msg: "state unavailable" }, instance: { instance_id: "instance-1", space_id: "space-1", strategy_id: "strategy-1" } };
  transport.post.mockResolvedValue({ data: response });
  const error = await callControl("strategy", "CreateStrategyInstance", {}, { headers: { Authorization: "fixture" } }).catch(error => error);
  expect(error).toBeInstanceOf(Error);
  expect(error.message).toBe("state unavailable");
  expect(error.response).toEqual(response);
  expect(error.response.instance).not.toHaveProperty("session_id");
});

it("preserves a recovery response returned with a non-2xx status", async () => {
  const response = { ret_info: { code: 1, msg: "enable failed" }, instance: { instance_id: "instance-1" } };
  const reject = transport.responseUse.mock.calls[0][1];
  await expect(reject({ response: { data: response } })).rejects.toMatchObject({ message: "enable failed", response });
});
