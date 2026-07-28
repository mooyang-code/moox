package consumer

import (
	"context"
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/stretchr/testify/require"
)

type submissionServiceStub struct {
	current      orderdomain.Order
	submitted    orderdomain.Order
	submitCalls  int
	resolveCalls int
}

func (s *submissionServiceStub) ResolveUnknown(
	context.Context,
	string,
	string,
) (orderdomain.Order, error) {
	s.resolveCalls++
	return s.submitted, nil
}

func (s *submissionServiceStub) Get(
	context.Context,
	string,
	string,
) (orderdomain.Order, error) {
	return s.current, nil
}

func (s *submissionServiceStub) Submit(
	context.Context,
	string,
	string,
) (orderdomain.Order, error) {
	s.submitCalls++
	return s.submitted, nil
}

func TestSubmissionWorkerSubmitsOnlyPendingOrder(t *testing.T) {
	service := &submissionServiceStub{
		current:   orderdomain.Order{State: orderdomain.Pending},
		submitted: orderdomain.Order{State: orderdomain.Open},
	}
	got, err := (SubmissionWorker{Service: service}).Handle(
		context.Background(),
		"space-1",
		"order-1",
	)
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, got.State)
	require.Equal(t, 1, service.submitCalls)

	service.current.State = orderdomain.Open
	got, err = (SubmissionWorker{Service: service}).Handle(
		context.Background(),
		"space-1",
		"order-1",
	)
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, got.State)
	require.Equal(t, 1, service.submitCalls)

	service.current.State = orderdomain.SubmitUnknown
	got, err = (SubmissionWorker{Service: service}).Handle(
		context.Background(),
		"space-1",
		"order-1",
	)
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, got.State)
	require.Equal(t, 1, service.resolveCalls)

	service.current.State = orderdomain.Submitting
	_, err = (SubmissionWorker{Service: service}).Handle(
		context.Background(),
		"space-1",
		"order-1",
	)
	require.NoError(t, err)
	require.Equal(t, 2, service.resolveCalls)
}
