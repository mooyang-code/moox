type FactorBindingLike = {
  source_dataset?: string;
  target_dataset?: string;
  status?: string;
};

export function factorResultDataset(sourceDataset: string) {
  const source = sourceDataset.trim().toLowerCase();
  const base = source.endsWith('_kline') ? source.slice(0, -'_kline'.length) : source;
  const candidate = `${base}_factor`;
  if (candidate.length <= 20) {
    return candidate;
  }
  const suffix = `_f${sha1Hex(source).slice(0, 4)}`;
  const prefixLen = 20 - suffix.length;
  let prefix = trimRight(base, '_');
  if (prefix.length > prefixLen) {
    prefix = trimRight(prefix.slice(0, prefixLen), '_');
  }
  if (!prefix) {
    prefix = 'dataset';
  }
  return `${prefix}${suffix}`;
}

export function factorBindingTargetDataset(binding: FactorBindingLike) {
  const explicitTarget = (binding.target_dataset || '').trim();
  if (explicitTarget) {
    return explicitTarget;
  }
  const source = (binding.source_dataset || '').trim();
  return source ? factorResultDataset(source) : '';
}

export function factorBindingTargetDatasetIds(bindings: FactorBindingLike[]) {
  const ids = new Set<string>();
  for (const binding of bindings) {
    if (binding.status && binding.status !== 'enabled') {
      continue;
    }
    const datasetId = factorBindingTargetDataset(binding);
    if (datasetId) {
      ids.add(datasetId);
    }
  }
  return Array.from(ids);
}

function trimRight(value: string, char: string) {
  let end = value.length;
  while (end > 0 && value[end - 1] === char) {
    end -= 1;
  }
  return value.slice(0, end);
}

function sha1Hex(input: string) {
  const bytes = new TextEncoder().encode(input);
  const words: number[] = [];
  for (let i = 0; i < bytes.length; i += 1) {
    words[i >> 2] = (words[i >> 2] || 0) | (bytes[i] << (24 - (i % 4) * 8));
  }
  words[bytes.length >> 2] = (words[bytes.length >> 2] || 0) | (0x80 << (24 - (bytes.length % 4) * 8));
  words[(((bytes.length + 8) >> 6) << 4) + 15] = bytes.length * 8;

  let h0 = 0x67452301;
  let h1 = 0xefcdab89;
  let h2 = 0x98badcfe;
  let h3 = 0x10325476;
  let h4 = 0xc3d2e1f0;
  const w = new Array<number>(80);

  for (let i = 0; i < words.length; i += 16) {
    for (let j = 0; j < 16; j += 1) {
      w[j] = words[i + j] || 0;
    }
    for (let j = 16; j < 80; j += 1) {
      w[j] = rotateLeft(w[j - 3] ^ w[j - 8] ^ w[j - 14] ^ w[j - 16], 1);
    }

    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;

    for (let j = 0; j < 80; j += 1) {
      let f = 0;
      let k = 0;
      if (j < 20) {
        f = (b & c) | (~b & d);
        k = 0x5a827999;
      } else if (j < 40) {
        f = b ^ c ^ d;
        k = 0x6ed9eba1;
      } else if (j < 60) {
        f = (b & c) | (b & d) | (c & d);
        k = 0x8f1bbcdc;
      } else {
        f = b ^ c ^ d;
        k = 0xca62c1d6;
      }
      const temp = (rotateLeft(a, 5) + f + e + k + w[j]) | 0;
      e = d;
      d = c;
      c = rotateLeft(b, 30);
      b = a;
      a = temp;
    }

    h0 = (h0 + a) | 0;
    h1 = (h1 + b) | 0;
    h2 = (h2 + c) | 0;
    h3 = (h3 + d) | 0;
    h4 = (h4 + e) | 0;
  }

  return [h0, h1, h2, h3, h4].map((word) => (word >>> 0).toString(16).padStart(8, '0')).join('');
}

function rotateLeft(value: number, bits: number) {
  return ((value << bits) | (value >>> (32 - bits))) | 0;
}
