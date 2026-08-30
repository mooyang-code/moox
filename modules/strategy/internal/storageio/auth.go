package storageio

// Auth carries the non-secret identity fields required by Storage metadata and
// primary-store requests. Secrets are resolved by the deployment adapter.
type Auth struct {
	AppID     string
	AppKey    string
	Operator  string
	RequestID string
}
