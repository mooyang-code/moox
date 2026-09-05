package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

const defaultLogicalAccountTimeout = 3 * time.Second

type logicalAccountOwnerClient struct {
	client  tradepb.TradeConsoleServiceClientProxy
	timeout time.Duration
}

func newLogicalAccountOwnerClient(
	target string,
	timeout time.Duration,
) *logicalAccountOwnerClient {
	if timeout <= 0 {
		timeout = defaultLogicalAccountTimeout
	}
	return &logicalAccountOwnerClient{
		client: tradepb.NewTradeConsoleServiceClientProxy(
			client.WithTarget(strings.TrimSpace(target)),
			client.WithNetwork("tcp"),
			client.WithProtocol("http"),
		),
		timeout: timeout,
	}
}

func (c *logicalAccountOwnerClient) Validate(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, "", false); err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.client.GetLogicalAccount(
		callCtx,
		&tradepb.GetLogicalAccountReq{LogicalAccountId: logicalAccountID},
		opts...,
	)
	if err != nil {
		return c.transportError(callCtx, "validate", err)
	}
	return validateLogicalAccountResponse(
		"validate",
		spaceID,
		logicalAccountID,
		"",
		response.GetRetInfo(),
		response.GetLogicalAccount(),
	)
}

// Generation returns the Trade-owned monotonic logical-account lifecycle
// token used to fence delayed Strategy target events.
func (c *logicalAccountOwnerClient) Generation(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	expectedOwner string,
) (int64, error) {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, expectedOwner, expectedOwner != ""); err != nil {
		return 0, err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	defer cancel()
	response, err := c.client.GetLogicalAccount(callCtx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalAccountID}, opts...)
	if err != nil {
		return 0, c.transportError(callCtx, "generation", err)
	}
	if err := validateLogicalAccountResponse("generation", spaceID, logicalAccountID, expectedOwner, response.GetRetInfo(), response.GetLogicalAccount()); err != nil {
		return 0, err
	}
	generation := response.GetLogicalAccount().GetOwnerGeneration()
	if generation <= 0 {
		return 0, fmt.Errorf("Trade LogicalAccount generation is not active")
	}
	return generation, nil
}

func (c *logicalAccountOwnerClient) Claim(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	_, err := c.ClaimWithGeneration(ctx, spaceID, logicalAccountID, runnerID)
	return err
}

// ClaimWithGeneration returns the Trade-owned lifecycle generation after a
// successful claim. The generation lets Strategy detect a repaired claim and
// clear any local target snapshot that belongs to the previous lifecycle.
func (c *logicalAccountOwnerClient) ClaimWithGeneration(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) (int64, error) {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, runnerID, true); err != nil {
		return 0, err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	defer cancel()
	response, err := c.client.ClaimLogicalAccountOwner(
		callCtx,
		&tradepb.ClaimLogicalAccountOwnerReq{
			LogicalAccountId: logicalAccountID,
			RunnerId:         runnerID,
		},
		opts...,
	)
	if err != nil {
		return 0, c.transportError(callCtx, "claim", err)
	}
	if err := validateLogicalAccountResponse(
		"claim",
		spaceID,
		logicalAccountID,
		runnerID,
		response.GetRetInfo(),
		response.GetLogicalAccount(),
	); err != nil {
		return 0, err
	}
	generation := response.GetLogicalAccount().GetOwnerGeneration()
	return generation, nil
}

func (c *logicalAccountOwnerClient) Release(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, runnerID, true); err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.client.ReleaseLogicalAccountOwner(
		callCtx,
		&tradepb.ReleaseLogicalAccountOwnerReq{
			LogicalAccountId: logicalAccountID,
			RunnerId:         runnerID,
		},
		opts...,
	)
	if err != nil {
		return c.transportError(callCtx, "release", err)
	}
	if err := validateLogicalAccountResponse(
		"release",
		spaceID,
		logicalAccountID,
		"",
		response.GetRetInfo(),
		response.GetLogicalAccount(),
	); err != nil {
		return err
	}
	if response.GetLogicalAccount().GetOwnerRunnerId() == runnerID {
		return fmt.Errorf(
			"Trade LogicalAccount release returned owner runner %q",
			runnerID,
		)
	}
	return nil
}

func (c *logicalAccountOwnerClient) ClaimSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, instanceID, true); err != nil {
		return err
	}
	expectedFence, err := c.readAuthFence(ctx, spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.client.ClaimLogicalAccountOwner(callCtx, &tradepb.ClaimLogicalAccountOwnerReq{LogicalAccountId: logicalAccountID, InstanceId: instanceID, SessionId: sessionID, ExpectedAuthFence: expectedFence}, opts...)
	if err != nil {
		return c.transportError(callCtx, "claim session", err)
	}
	if response.GetRetInfo().GetCode() != 0 {
		return fmt.Errorf("Trade claim session failed: %s", response.GetRetInfo().GetMsg())
	}
	if response.GetLogicalAccount().GetOwnerInstanceId() != instanceID || response.GetLogicalAccount().GetOwnerSessionId() != sessionID {
		return errors.New("Trade claim session returned mismatched owner")
	}
	return nil
}

func (c *logicalAccountOwnerClient) ReleaseSession(ctx context.Context, spaceID, logicalAccountID, instanceID, sessionID string) error {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, instanceID, true); err != nil {
		return err
	}
	expectedFence, err := c.readAuthFence(ctx, spaceID, logicalAccountID)
	if err != nil {
		return err
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return err
	}
	defer cancel()
	response, err := c.client.ReleaseLogicalAccountOwner(callCtx, &tradepb.ReleaseLogicalAccountOwnerReq{LogicalAccountId: logicalAccountID, InstanceId: instanceID, SessionId: sessionID, ExpectedAuthFence: expectedFence}, opts...)
	if err != nil {
		return c.transportError(callCtx, "release session", err)
	}
	if response.GetRetInfo().GetCode() != 0 {
		return fmt.Errorf("Trade release session failed: %s", response.GetRetInfo().GetMsg())
	}
	return nil
}

func (c *logicalAccountOwnerClient) readAuthFence(ctx context.Context, spaceID, logicalAccountID string) (string, error) {
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return "", err
	}
	defer cancel()
	response, err := c.client.GetLogicalAccount(callCtx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalAccountID}, opts...)
	if err != nil {
		return "", c.transportError(callCtx, "read auth fence", err)
	}
	if err := validateLogicalAccountResponse("read auth fence", spaceID, logicalAccountID, "", response.GetRetInfo(), response.GetLogicalAccount()); err != nil {
		return "", err
	}
	return response.GetLogicalAccount().GetAuthFence(), nil
}

// Rebind starts a fresh Trade owner lifecycle without releasing the current
// owner. This fences delayed targets when an archived V1 runner is reused by
// a V2 runner with the same identity.
func (c *logicalAccountOwnerClient) Rebind(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
	runnerID string,
	rebindKey string,
) (int64, error) {
	if err := validateLogicalAccountIdentity(spaceID, logicalAccountID, runnerID, true); err != nil {
		return 0, err
	}
	if strings.TrimSpace(rebindKey) == "" {
		return 0, errors.New("rebind key is required")
	}
	callCtx, cancel, opts, err := c.call(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	defer cancel()
	response, err := c.client.RebindLogicalAccountOwner(
		callCtx,
		&tradepb.RebindLogicalAccountOwnerReq{
			LogicalAccountId: logicalAccountID,
			RunnerId:         runnerID,
			RebindKey:        rebindKey,
		},
		opts...,
	)
	if err != nil {
		return 0, c.transportError(callCtx, "rebind", err)
	}
	if err := validateLogicalAccountResponse(
		"rebind",
		spaceID,
		logicalAccountID,
		runnerID,
		response.GetRetInfo(),
		response.GetLogicalAccount(),
	); err != nil {
		return 0, err
	}
	generation := response.GetLogicalAccount().GetOwnerGeneration()
	if generation <= 0 {
		return 0, fmt.Errorf("Trade LogicalAccount rebind returned inactive owner generation")
	}
	return generation, nil
}

func (c *logicalAccountOwnerClient) call(
	ctx context.Context,
	spaceID string,
) (context.Context, context.CancelFunc, []client.Option, error) {
	if c == nil || c.client == nil {
		return nil, nil, nil, errors.New("Trade LogicalAccount client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultLogicalAccountTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	reqHead := &thttp.ClientReqHeader{Header: make(http.Header)}
	reqHead.Header.Set("X-Space-Id", spaceID)
	return callCtx, cancel, []client.Option{
		client.WithReqHead(reqHead),
		client.WithTimeout(timeout),
	}, nil
}

func (c *logicalAccountOwnerClient) transportError(
	ctx context.Context,
	operation string,
	err error,
) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("Trade LogicalAccount %s timed out: %w", operation, ctxErr)
	}
	return fmt.Errorf("Trade LogicalAccount %s RPC failed: %w", operation, err)
}

func validateLogicalAccountResponse(
	operation string,
	spaceID string,
	logicalAccountID string,
	expectedOwner string,
	retInfo *tradepb.RetInfo,
	account *tradepb.LogicalAccount,
) error {
	if retInfo == nil {
		return fmt.Errorf("Trade LogicalAccount %s returned no status", operation)
	}
	if retInfo.GetCode() != tradepb.ErrorCode_SUCCESS {
		return fmt.Errorf(
			"Trade LogicalAccount %s failed (code=%d): %s",
			operation,
			retInfo.GetCode(),
			retInfo.GetMsg(),
		)
	}
	if account == nil {
		return fmt.Errorf("Trade LogicalAccount %s returned no account", operation)
	}
	if account.GetLogicalAccountId() != logicalAccountID ||
		account.GetSpaceId() != spaceID {
		return fmt.Errorf(
			"Trade LogicalAccount %s returned mismatched account %q in space %q",
			operation,
			account.GetLogicalAccountId(),
			account.GetSpaceId(),
		)
	}
	if expectedOwner != "" && account.GetOwnerRunnerId() != expectedOwner {
		return fmt.Errorf(
			"Trade LogicalAccount %s returned owner runner %q, want %q",
			operation,
			account.GetOwnerRunnerId(),
			expectedOwner,
		)
	}
	return nil
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
