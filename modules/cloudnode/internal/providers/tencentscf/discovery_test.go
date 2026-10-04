package tencentscf

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

type fakeDiscoveryAPI struct {
	namespacePages []*scf.ListNamespacesResponse
	functionPages  map[string][]*scf.ListFunctionsResponse
	namespaceCalls int
	functionCalls  map[string]int
	err            error
}

func (f *fakeDiscoveryAPI) ListNamespacesWithContext(context.Context, *scf.ListNamespacesRequest) (*scf.ListNamespacesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	response := f.namespacePages[f.namespaceCalls]
	f.namespaceCalls++
	return response, nil
}

func (f *fakeDiscoveryAPI) ListFunctionsWithContext(_ context.Context, request *scf.ListFunctionsRequest) (*scf.ListFunctionsResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	namespace := *request.Namespace
	pages := f.functionPages[namespace]
	if f.functionCalls == nil {
		f.functionCalls = make(map[string]int)
	}
	response := pages[f.functionCalls[namespace]]
	f.functionCalls[namespace]++
	return response, nil
}

func TestListFunctionInventorySortsAndSkipsNilEntries(t *testing.T) {
	api := &fakeDiscoveryAPI{
		namespacePages: []*scf.ListNamespacesResponse{{Response: &scf.ListNamespacesResponseParams{Namespaces: []*scf.Namespace{{Name: str("ns-b")}, nil, {Name: str("ns-a")}}}}},
		functionPages: map[string][]*scf.ListFunctionsResponse{
			"ns-a": {{Response: &scf.ListFunctionsResponseParams{Functions: []*scf.Function{{FunctionName: str("f-2"), Status: str("Active"), Runtime: str("Go"), Type: str("Event")}, {FunctionName: str("f-1")}}}}},
			"ns-b": {{Response: &scf.ListFunctionsResponseParams{Functions: []*scf.Function{{FunctionName: str("f-3")}}}}},
		},
	}
	items, err := listFunctionInventory(context.Background(), "ap-guangzhou", api)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.Equal(t, "ns-a", items[0].Namespace)
	require.Equal(t, "f-1", items[0].FunctionName)
	require.Equal(t, "ns-a", items[1].Namespace)
	require.Equal(t, "f-2", items[1].FunctionName)
	require.Equal(t, "Event", items[1].Type)
	require.Equal(t, "ns-b", items[2].Namespace)
}

func TestListFunctionInventoryPropagatesProviderError(t *testing.T) {
	api := &fakeDiscoveryAPI{err: errors.New("provider failed")}
	_, err := listFunctionInventory(context.Background(), "ap-guangzhou", api)
	require.ErrorContains(t, err, "provider failed")
}

func str(value string) *string { return &value }
