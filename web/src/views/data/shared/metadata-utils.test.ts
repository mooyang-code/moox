import { describe, expect, it } from 'vitest';
import { dataKindOptions } from './metadata-utils';

describe('dataKindOptions', () => {
  it('only exposes record and time-series datasets', () => {
    expect(dataKindOptions.map((item) => item.value)).toEqual([
      'DATA_KIND_TIME_SERIES',
      'DATA_KIND_RECORD',
    ]);
  });
});
