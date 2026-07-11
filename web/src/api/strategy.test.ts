import { describe, expect, it } from 'vitest';
import { normalizePerformance } from './strategy';

describe('strategy api normalization', () => {
  it('keeps performance sources separate', () => {
    const result = normalizePerformance({ groups: [{ performance_source: 'paper', points: [] }, { performance_source: 'live', points: [] }] });
    expect(result.groups.map((item) => item.performance_source)).toEqual(['paper', 'live']);
  });
});
