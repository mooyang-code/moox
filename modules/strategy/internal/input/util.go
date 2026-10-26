package input

import "sort"

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortStrings(values []string) { sort.Strings(values) }

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// nearestNames 返回 candidates 里与 name 最接近的至多 limit 个名字（编辑距离升序，同距离按字典序）。
// 距离超过 name 长度一半的不算接近，没有接近的返回 nil：列名写错时用它给出提示。
func nearestNames(name string, candidates []string, limit int) []string {
	type scored struct {
		name     string
		distance int
	}
	maxDistance := max(2, len([]rune(name))/2)
	var near []scored
	for _, candidate := range candidates {
		if distance := editDistance(name, candidate); distance <= maxDistance {
			near = append(near, scored{candidate, distance})
		}
	}
	sort.Slice(near, func(i, j int) bool {
		if near[i].distance != near[j].distance {
			return near[i].distance < near[j].distance
		}
		return near[i].name < near[j].name
	})
	if len(near) > limit {
		near = near[:limit]
	}
	if len(near) == 0 {
		return nil
	}
	out := make([]string, len(near))
	for i, item := range near {
		out[i] = item.name
	}
	return out
}

// editDistance 是两个字符串的 Levenshtein 编辑距离（按字符）。
func editDistance(a, b string) int {
	left, right := []rune(a), []rune(b)
	previous := make([]int, len(right)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(left); i++ {
		current := make([]int, len(right)+1)
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(right)]
}
