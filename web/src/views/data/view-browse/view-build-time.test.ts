import { describe, expect, it } from 'vitest';
import { viewBuildTimeLabel } from './view-build-time';

describe('viewBuildTimeLabel', () => {
  it('uses finished_at only for the active index', () => {
    expect(viewBuildTimeLabel({ active_index_id: 'idx', index_build: { index_id: 'idx', finished_at: '2026-07-15T01:02:03Z' } } as any)).toContain('构建完成');
    expect(viewBuildTimeLabel({ active_index_id: 'idx', index_build: { index_id: 'other', finished_at: '2026-07-15T01:02:03Z' } } as any)).toBe('构建时间未知');
  });

  it('marks an unfinished active build as building', () => {
    expect(viewBuildTimeLabel({ active_index_id: 'idx', index_build: { index_id: 'idx', started_at: '2026-07-15T01:02:03Z' } } as any)).toContain('构建中');
  });
});
