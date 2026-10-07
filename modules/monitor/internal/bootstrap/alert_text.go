package bootstrap

import (
	"fmt"
	"time"
)

// alertZone renders alert times in Beijing time, which operators read.
var alertZone = time.FixedZone("CST", 8*60*60)

// formatAlertTime renders a time for alert text, such as "10-07 20:15".
func formatAlertTime(at time.Time) string {
	if at.IsZero() {
		return "未知"
	}
	return at.In(alertZone).Format("01-02 15:04")
}

// formatAlertDuration renders a duration for alert text, such as
// "1 小时 5 分钟".
func formatAlertDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	minutes := int(d.Minutes())
	hours, minutes := minutes/60, minutes%60
	switch {
	case hours == 0:
		return fmt.Sprintf("%d 分钟", minutes)
	case hours >= 48:
		return fmt.Sprintf("%d 天 %d 小时", hours/24, hours%24)
	case minutes == 0:
		return fmt.Sprintf("%d 小时", hours)
	default:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	}
}
