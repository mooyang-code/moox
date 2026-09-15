package health

import "github.com/mooyang-code/moox/packages/healthz"

type State = healthz.State

func New(module, instance, version, commit string) *State {
	return healthz.NewState(module, instance, version, commit)
}
