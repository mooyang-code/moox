package tencentscf

import (
	"context"
	"fmt"
	"sort"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

const discoveryPageSize int64 = 100

// DiscoveryFunction is the non-sensitive inventory returned by ListFunctions.
type DiscoveryFunction struct {
	FunctionRef
	Status  string
	Runtime string
	Type    string
}

type discoveryAPI interface {
	ListNamespacesWithContext(context.Context, *scf.ListNamespacesRequest) (*scf.ListNamespacesResponse, error)
	ListFunctionsWithContext(context.Context, *scf.ListFunctionsRequest) (*scf.ListFunctionsResponse, error)
}

// ListFunctionInventory scans every Namespace and function in one SCF region.
func (c *Client) ListFunctionInventory(ctx context.Context, region string) ([]DiscoveryFunction, error) {
	api, err := c.newClient(region)
	if err != nil {
		return nil, err
	}
	return listFunctionInventory(ctx, region, api)
}

func listFunctionInventory(ctx context.Context, region string, api discoveryAPI) ([]DiscoveryFunction, error) {
	if api == nil {
		return nil, fmt.Errorf("scf discovery client is required")
	}
	namespaces, err := listNamespaces(ctx, api)
	if err != nil {
		return nil, err
	}
	functions := make([]DiscoveryFunction, 0)
	for _, namespace := range namespaces {
		items, err := listFunctions(ctx, api, region, namespace)
		if err != nil {
			return nil, fmt.Errorf("list functions namespace %s: %w", namespace, err)
		}
		functions = append(functions, items...)
	}
	sort.Slice(functions, func(i, j int) bool {
		if functions[i].Namespace != functions[j].Namespace {
			return functions[i].Namespace < functions[j].Namespace
		}
		return functions[i].FunctionName < functions[j].FunctionName
	})
	return functions, nil
}

func listNamespaces(ctx context.Context, api discoveryAPI) ([]string, error) {
	result := make([]string, 0)
	for offset := int64(0); ; {
		request := scf.NewListNamespacesRequest()
		request.Limit = common.Int64Ptr(discoveryPageSize)
		request.Offset = common.Int64Ptr(offset)
		response, err := api.ListNamespacesWithContext(ctx, request)
		if err != nil {
			return nil, err
		}
		if response == nil || response.Response == nil {
			return result, nil
		}
		items := response.Response.Namespaces
		for _, item := range items {
			if item != nil && item.Name != nil && *item.Name != "" {
				result = append(result, *item.Name)
			}
		}
		next := int64(len(items))
		if next == 0 || next < discoveryPageSize || (response.Response.TotalCount != nil && int64(len(result)) >= *response.Response.TotalCount) {
			break
		}
		offset += next
	}
	return result, nil
}

func listFunctions(ctx context.Context, api discoveryAPI, region, namespace string) ([]DiscoveryFunction, error) {
	result := make([]DiscoveryFunction, 0)
	for offset := int64(0); ; {
		request := scf.NewListFunctionsRequest()
		request.Limit = common.Int64Ptr(discoveryPageSize)
		request.Offset = common.Int64Ptr(offset)
		request.Namespace = common.StringPtr(namespace)
		request.Order = common.StringPtr("ASC")
		request.Orderby = common.StringPtr("FunctionName")
		response, err := api.ListFunctionsWithContext(ctx, request)
		if err != nil {
			return nil, err
		}
		if response == nil || response.Response == nil {
			return result, nil
		}
		items := response.Response.Functions
		for _, item := range items {
			if item == nil || item.FunctionName == nil || *item.FunctionName == "" {
				continue
			}
			status, runtime, functionType := "", "", ""
			if item.Status != nil {
				status = *item.Status
			}
			if item.Runtime != nil {
				runtime = *item.Runtime
			}
			if item.Type != nil {
				functionType = *item.Type
			}
			result = append(result, DiscoveryFunction{FunctionRef: FunctionRef{Region: region, Namespace: namespace, FunctionName: *item.FunctionName}, Status: status, Runtime: runtime, Type: functionType})
		}
		next := int64(len(items))
		if next == 0 || next < discoveryPageSize || (response.Response.TotalCount != nil && int64(len(result)) >= *response.Response.TotalCount) {
			break
		}
		offset += next
	}
	return result, nil
}
