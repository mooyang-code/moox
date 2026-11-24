package fetchplan

// 测试用：按原来几项协调指标的取值读出空间的协调状态（没有记录时为 0）。
func coordinationValue(m *ControlMetrics, spaceID, field string) float64 {
	m.coordination.mu.Lock()
	defer m.coordination.mu.Unlock()
	state := m.coordination.spaces[spaceID]
	if state == nil {
		return 0
	}
	switch field {
	case "healthy":
		if state.healthy {
			return 1
		}
	case "pending":
		if state.pending {
			return 1
		}
	case "pending_since":
		if !state.pendingSince.IsZero() {
			return float64(state.pendingSince.Unix())
		}
	case "last_success":
		if !state.lastSuccess.IsZero() {
			return float64(state.lastSuccess.Unix())
		}
	default:
		// 其余按失败原因：当前失败原因是它时为 1。
		if state.failure == field {
			return 1
		}
	}
	return 0
}
