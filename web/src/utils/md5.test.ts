import { createHash } from "node:crypto";
import { describe, expect, it } from "vitest";
import { computeFileMd5, createMd5 } from "./md5";

const encoder = new TextEncoder();

function nodeMd5(bytes: Uint8Array): string {
  return createHash("md5").update(bytes).digest("hex");
}

describe("RFC 1321 incremental MD5", () => {
  it("matches all official RFC 1321 test vectors", () => {
    const vectors: Array<[string, string]> = [
      ["", "d41d8cd98f00b204e9800998ecf8427e"],
      ["a", "0cc175b9c0f1b6a831c399e269772661"],
      ["abc", "900150983cd24fb0d6963f7d28e17f72"],
      ["message digest", "f96b697d7cb7938d525a2f31aaf161d0"],
      ["abcdefghijklmnopqrstuvwxyz", "c3fcd3d76192e4007dfb496cca67e13b"],
      ["ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", "d174ab98d277d9f5a5611c2c9f419d9f"],
      ["12345678901234567890123456789012345678901234567890123456789012345678901234567890", "57edf4a22be3c955ac49da2e2107b67a"]
    ];

    for (const [input, expected] of vectors) {
      expect(createMd5().update(encoder.encode(input)).digestHex(), input).toBe(expected);
    }
  });

  it("matches Node crypto across block padding boundaries and arbitrary streaming splits", () => {
    for (const size of [0, 1, 55, 56, 57, 63, 64, 65, 119, 120, 128, 1024]) {
      const data = new Uint8Array(size);
      for (let i = 0; i < size; i++) data[i] = (i * 31 + 17) & 0xff;
      const expected = nodeMd5(data);

      expect(createMd5().update(data).digestHex(), `single-${size}`).toBe(expected);

      // 故意用与 64 字节不对齐的 13 字节分片流式输入
      const streamed = createMd5();
      for (let pos = 0; pos < size; pos += 13) {
        streamed.update(data.subarray(pos, Math.min(pos + 13, size)));
      }
      expect(streamed.digestHex(), `chunked-${size}`).toBe(expected);
    }
  });

  it("computes multi-chunk File/Blob MD5 accurately for empty, single-chunk, and >2 MiB buffers", async () => {
    expect(await computeFileMd5(new Blob([]))).toBe("d41d8cd98f00b204e9800998ecf8427e");

    const medium = new Uint8Array(4096);
    for (let i = 0; i < medium.length; i++) medium[i] = (i * 7 + 3) & 0xff;
    expect(await computeFileMd5(new Blob([medium]), 512)).toBe(nodeMd5(medium));

    // 跨多个默认 2 MiB 分块（4.5 MiB）
    const large = new Uint8Array(4 * 1024 * 1024 + 512 * 1024 + 19);
    for (let i = 0; i < large.length; i++) large[i] = (i * 13 + 5) & 0xff;
    expect(await computeFileMd5(new Blob([large]))).toBe(nodeMd5(large));
  });
});
