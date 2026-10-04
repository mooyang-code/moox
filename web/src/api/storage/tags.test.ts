import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callStorage: vi.fn() }));

vi.mock("./http", () => ({ callStorage: mocks.callStorage }));

import { addTagMembers, deleteTag, listTagMembers, listTags, removeTagMembers, setTagMemberStatus, upsertTag } from "./metadata";

describe("Storage tag APIs", () => {
  beforeEach(() => {
    mocks.callStorage.mockReset();
    mocks.callStorage.mockResolvedValue({ ret_info: { code: 0, msg: "" }, tag: { tag_id: "crypto_spot" } });
  });

  it("uses the tag definition RPCs with the expected request shapes", async () => {
    const tag = {
      space_id: "crypto",
      tag_id: "crypto_spot",
      tag_name: "加密现货",
      mode: "manual" as const
    };

    await listTags("crypto", { page: 2, size: 50 });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("ListTags", {
      space_id: "crypto",
      page: { page: 2, size: 50 }
    });

    await upsertTag(tag);
    expect(mocks.callStorage).toHaveBeenLastCalledWith("UpsertTag", { tag });

    await deleteTag("crypto", "crypto_spot");
    expect(mocks.callStorage).toHaveBeenLastCalledWith("DeleteTag", {
      space_id: "crypto",
      tag_id: "crypto_spot"
    });
  });

  it("uses the member list and mutation RPCs with tag-scoped subjects", async () => {
    await listTagMembers({
      space_id: "crypto",
      tag_id: "crypto_spot",
      status: "active",
      keyword: "btc",
      page: { page: 1, size: 20 }
    });
    expect(mocks.callStorage).toHaveBeenLastCalledWith("ListTagMembers", {
      space_id: "crypto",
      tag_id: "crypto_spot",
      status: "active",
      keyword: "btc",
      page: { page: 1, size: 20 }
    });

    await addTagMembers("crypto", "crypto_spot", ["BTC-USDT"]);
    expect(mocks.callStorage).toHaveBeenLastCalledWith("AddTagMembers", {
      space_id: "crypto",
      tag_id: "crypto_spot",
      subject_ids: ["BTC-USDT"]
    });

    await removeTagMembers("crypto", "crypto_spot", ["BTC-USDT"]);
    expect(mocks.callStorage).toHaveBeenLastCalledWith("RemoveTagMembers", {
      space_id: "crypto",
      tag_id: "crypto_spot",
      subject_ids: ["BTC-USDT"]
    });

    await setTagMemberStatus("crypto", "crypto_spot", ["BTC-USDT"], "active");
    expect(mocks.callStorage).toHaveBeenLastCalledWith("SetTagMemberStatus", {
      space_id: "crypto",
      tag_id: "crypto_spot",
      subject_ids: ["BTC-USDT"],
      status: "active"
    });
  });
});
