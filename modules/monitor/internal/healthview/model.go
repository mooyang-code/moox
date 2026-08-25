package healthview

import "time"

type Alert struct {
	ID, Title, Status, Reason, Severity string
	CheckedAt                           time.Time
}
type Instance struct {
	Name, NodeID, InstanceID, Status, Conclusion string
	LastCheckedAt                                time.Time
}
type Item struct {
	ID, Group, Name, Description, Status, Reason string
	CheckedAt                                    time.Time
	OmittedInstanceCount                         int32
	Instances                                    []Instance
}
type Overview struct {
	GeneratedAt            time.Time
	Alerts                 []Alert
	BusinessItems          []Item
	ServiceItems           []Item
	NotificationType       string
	NotificationConfigured bool
	NotificationMasked     string
}
