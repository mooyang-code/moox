package dsl

import "testing"

// include 与 tags 同时出现时 include 是追加而不是白名单；显式 pool 的规则不看 universe 的大小（固定 pool 小于 min_universe 时要写 allow_empty）：都不在保存时拒绝。
func TestValidateIncludeOnlyUniverseScope(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"include 追加到标签":          "name: x\nuniverse: {tags: [majors], include: [A]}\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1}\nportfolio: {min_universe: 3}\n",
		"显式标签 pool":              "name: x\nuniverse: {include: [A]}\nrules:\n  - {id: a, type: rank, pool: {tags: [majors]}, score: close, select: {top: 1}, weight: 1}\nportfolio: {min_universe: 3}\n",
		"固定 pool 写了 allow_empty": "name: x\nuniverse: {include: [A]}\nrules:\n  - {id: a, type: rank, pool: [A], allow_empty: true, score: \"0\", select: {top: 10000}, hold: {entry: \"close > 1\", exit: \"close < 1\"}, weight: 1}\nportfolio: {min_universe: 3}\n",
	} {
		raw := raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse([]byte(raw)); err != nil {
				t.Fatalf("不应拒绝：%v", err)
			}
		})
	}
}
