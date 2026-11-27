/**
 * 零依赖 RFC 1321 增量 MD5 实现，面向大文件分块哈希（避免整文件读入内存与逐字节字符串拼接）。
 */

const SHIFT = new Uint8Array([
  7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 4, 11, 16,
  23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21
]);

const K = Uint32Array.from({ length: 64 }, (_, i) => Math.floor(Math.abs(Math.sin(i + 1)) * 0x100000000) >>> 0);

/** 云函数代码包 MD5 分块读取大小：2 MiB。 */
export const DEFAULT_MD5_CHUNK_SIZE = 2 * 1024 * 1024;

export interface Md5Hasher {
  update(chunk: Uint8Array): Md5Hasher;
  digestHex(): string;
}

function toHexLE(word: number): string {
  let out = "";
  for (let shift = 0; shift < 32; shift += 8) {
    out += ((word >>> shift) & 0xff).toString(16).padStart(2, "0");
  }
  return out;
}

export function createMd5(): Md5Hasher {
  let a0 = 0x67452301;
  let b0 = 0xefcdab89;
  let c0 = 0x98badcfe;
  let d0 = 0x10325476;

  const block = new Uint8Array(64);
  const words = new Uint32Array(16);
  let blockOffset = 0;
  let totalBytes = 0;

  function compress(buf: Uint8Array, offset: number) {
    for (let i = 0; i < 16; i++) {
      const j = offset + i * 4;
      words[i] = (buf[j] | (buf[j + 1] << 8) | (buf[j + 2] << 16) | (buf[j + 3] << 24)) >>> 0;
    }

    let a = a0;
    let b = b0;
    let c = c0;
    let d = d0;

    for (let i = 0; i < 64; i++) {
      let f: number;
      let g: number;
      if (i < 16) {
        f = (b & c) | (~b & d);
        g = i;
      } else if (i < 32) {
        f = (d & b) | (~d & c);
        g = (5 * i + 1) & 15;
      } else if (i < 48) {
        f = b ^ c ^ d;
        g = (3 * i + 5) & 15;
      } else {
        f = c ^ (b | ~d);
        g = (7 * i) & 15;
      }
      const sum = (a + f + K[i] + words[g]) >>> 0;
      const s = SHIFT[i];
      const rotated = ((sum << s) | (sum >>> (32 - s))) >>> 0;
      a = d;
      d = c;
      c = b;
      b = (b + rotated) >>> 0;
    }

    a0 = (a0 + a) >>> 0;
    b0 = (b0 + b) >>> 0;
    c0 = (c0 + c) >>> 0;
    d0 = (d0 + d) >>> 0;
  }

  const hasher: Md5Hasher = {
    update(chunk: Uint8Array): Md5Hasher {
      let pos = 0;
      const len = chunk.length;
      totalBytes += len;

      if (blockOffset > 0) {
        const take = Math.min(64 - blockOffset, len);
        block.set(chunk.subarray(0, take), blockOffset);
        blockOffset += take;
        pos += take;
        if (blockOffset === 64) {
          compress(block, 0);
          blockOffset = 0;
        }
      }

      while (pos + 64 <= len) {
        compress(chunk, pos);
        pos += 64;
      }

      if (pos < len) {
        block.set(chunk.subarray(pos), 0);
        blockOffset = len - pos;
      }
      return hasher;
    },

    digestHex(): string {
      const bitLenLow = (totalBytes * 8) >>> 0;
      const bitLenHigh = Math.floor(totalBytes / 0x20000000) >>> 0;

      block[blockOffset++] = 0x80;
      if (blockOffset > 56) {
        block.fill(0, blockOffset, 64);
        compress(block, 0);
        blockOffset = 0;
      }
      block.fill(0, blockOffset, 56);

      const view = new DataView(block.buffer);
      view.setUint32(56, bitLenLow, true);
      view.setUint32(60, bitLenHigh, true);
      compress(block, 0);
      blockOffset = 0;

      return toHexLE(a0) + toHexLE(b0) + toHexLE(c0) + toHexLE(d0);
    }
  };

  return hasher;
}

/** 按固定分块增量计算 Blob / File 的 MD5 十六进制摘要（32 位小写）。 */
export async function computeFileMd5(file: Blob, chunkSize = DEFAULT_MD5_CHUNK_SIZE): Promise<string> {
  const step = Math.max(64, Math.floor(chunkSize));
  const hasher = createMd5();
  for (let offset = 0; offset < file.size; offset += step) {
    const buffer = await file.slice(offset, Math.min(offset + step, file.size)).arrayBuffer();
    hasher.update(new Uint8Array(buffer));
  }
  return hasher.digestHex();
}
