package store

import "time"

func toUnixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

func fromUnixMilli(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func toUnixMilliPtr(t *time.Time) *int64 {
	if t == nil || t.IsZero() {
		return nil
	}
	ms := t.UTC().UnixMilli()
	return &ms
}

func fromUnixMilliPtr(ms *int64) *time.Time {
	if ms == nil || *ms == 0 {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

func nowUnixMilli() int64 {
	return time.Now().UTC().UnixMilli()
}
