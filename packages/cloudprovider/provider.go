package cloudprovider

import "context"

type CallerIdentity struct {
	Provider  string
	AccountID string
	RequestID string
}

type IdentityValidator interface {
	GetCallerIdentity(context.Context) (CallerIdentity, error)
}
