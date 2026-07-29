package bootstrap

import (
	"context"
	"fmt"
	"strings"
)

// logicalAccountOwnerClient keeps Strategy independent from physical Exchange
// accounts. The concrete Trade RPC adapter is installed with the new
// LogicalAccountService contract; until then this stub fails closed.
type logicalAccountOwnerClient struct {
	target string
}

func newLogicalAccountOwnerClient(target string) *logicalAccountOwnerClient {
	return &logicalAccountOwnerClient{target: strings.TrimSpace(target)}
}

func (c *logicalAccountOwnerClient) Validate(
	_ context.Context,
	spaceID string,
	logicalAccountID string,
) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, "", false); err != nil {
		return err
	}
	return c.unavailable()
}

func (c *logicalAccountOwnerClient) Claim(
	_ context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, runnerID, true); err != nil {
		return err
	}
	return c.unavailable()
}

func (c *logicalAccountOwnerClient) Release(
	_ context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, runnerID, true); err != nil {
		return err
	}
	return c.unavailable()
}

func (c *logicalAccountOwnerClient) unavailable() error {
	target := ""
	if c != nil {
		target = c.target
	}
	return fmt.Errorf("Trade LogicalAccount owner RPC is unavailable at %q", target)
}

func validateLogicalAccountIdentity(
	spaceID string,
	logicalAccountID string,
	runnerID string,
	requireRunner bool,
) error {
	if strings.TrimSpace(spaceID) == "" {
		return fmt.Errorf("space_id is required for LogicalAccount ownership")
	}
	if strings.TrimSpace(logicalAccountID) == "" {
		return fmt.Errorf("logical_account_id is required for LogicalAccount ownership")
	}
	if !requireRunner {
		return nil
	}
	if strings.TrimSpace(runnerID) == "" {
		return fmt.Errorf("runner_id is required for LogicalAccount ownership")
	}
	return nil
}
