package routing

import "time"

type HealthState string

const (
	HealthClosed   HealthState = "closed"
	HealthOpen     HealthState = "open"
	HealthHalfOpen HealthState = "half_open"
)

type Circuit struct {
	State             HealthState
	ConsecutiveErrors int
	OpenedAt          time.Time
	ProbeInFlight     bool
}

type CircuitPolicy struct {
	FailureThreshold int
	Cooldown         time.Duration
}

func (c Circuit) Allow(now time.Time, policy CircuitPolicy) (Circuit, bool) {
	if c.State == "" {
		c.State = HealthClosed
	}
	if c.State == HealthOpen && !now.Before(c.OpenedAt.Add(policy.Cooldown)) {
		c.State, c.ProbeInFlight = HealthHalfOpen, true
		return c, true
	}
	if c.State == HealthHalfOpen {
		return c, !c.ProbeInFlight
	}
	return c, c.State == HealthClosed
}

func (c Circuit) Success() Circuit {
	c.State, c.ConsecutiveErrors, c.OpenedAt, c.ProbeInFlight = HealthClosed, 0, time.Time{}, false
	return c
}

func (c Circuit) TemporaryFailure(now time.Time, policy CircuitPolicy) Circuit {
	if c.State == "" {
		c.State = HealthClosed
	}
	c.ConsecutiveErrors++
	if c.State == HealthHalfOpen || policy.FailureThreshold <= 1 || c.ConsecutiveErrors >= policy.FailureThreshold {
		c.State, c.OpenedAt, c.ProbeInFlight = HealthOpen, now, false
	}
	return c
}
