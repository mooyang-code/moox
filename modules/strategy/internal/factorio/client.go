package factorio

import (
	"context"
	"fmt"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// Client is an adapter boundary for the Factor metadata API. Keeping the
// generated tRPC proxy behind these function fields makes compilation easy to
// test and prevents the Strategy domain from depending on transport details.
type Client struct {
	GetFactorFunc    func(context.Context, string) (compiler.FactorDescriptor, error)
	ListBindingsFunc func(context.Context, string) ([]compiler.BindingDescriptor, error)
}

// RPCClient adapts the FactorMgr metadata proxy to the compiler catalog.
type RPCClient struct {
	Proxy    factorpb.FactorMgrClientProxy
	PageSize uint32
}

func (c *RPCClient) GetFactor(ctx context.Context, id string) (compiler.FactorDescriptor, error) {
	if c == nil || c.Proxy == nil {
		return compiler.FactorDescriptor{}, context.Canceled
	}
	rsp, err := c.Proxy.GetFactor(ctx, &factorpb.GetFactorReq{FactorId: id})
	if err != nil {
		return compiler.FactorDescriptor{}, err
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		if rsp == nil || rsp.GetRetInfo() == nil {
			return compiler.FactorDescriptor{}, context.Canceled
		}
		err := fmt.Errorf("factor rpc %s: %s", rsp.GetRetInfo().GetCode().String(), rsp.GetRetInfo().GetMsg())
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INNER_ERR {
			return compiler.FactorDescriptor{}, compiler.DependencyMismatchError(err)
		}
		return compiler.FactorDescriptor{}, err
	}
	factor := rsp.GetFactor()
	if factor == nil {
		return compiler.FactorDescriptor{}, compiler.DependencyMismatchError(fmt.Errorf("factor %s is empty", id))
	}
	return compiler.FactorDescriptor{
		ID: factor.GetFactorId(), Status: factor.GetStatus(), SourceHash: factor.GetSourceHash(),
		InputColumns: append([]string(nil), factor.GetInputColumns()...), ParamsJSON: factor.GetParamsJson(),
		LookbackPeriods: int(factor.GetLookbackPeriods()), Outputs: append([]string(nil), factor.GetOutputs()...),
	}, nil
}

func (c *RPCClient) ListBindings(ctx context.Context, factorID string) ([]compiler.BindingDescriptor, error) {
	if c == nil || c.Proxy == nil {
		return nil, context.Canceled
	}
	pageSize := c.PageSize
	if pageSize == 0 {
		pageSize = 500
	}
	var result []compiler.BindingDescriptor
	for page := uint32(1); ; page++ {
		rsp, err := c.Proxy.ListBindings(ctx, &factorpb.ListBindingsReq{Page: &commonpb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			if rsp == nil || rsp.GetRetInfo() == nil {
				return nil, context.Canceled
			}
			err := fmt.Errorf("factor rpc %s: %s", rsp.GetRetInfo().GetCode().String(), rsp.GetRetInfo().GetMsg())
			if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INNER_ERR {
				return nil, compiler.DependencyMismatchError(err)
			}
			return nil, err
		}
		for _, binding := range rsp.GetBindings() {
			if binding == nil || binding.GetFactorId() != factorID {
				continue
			}
			result = append(result, compiler.BindingDescriptor{ID: binding.GetBindingId(), FactorID: binding.GetFactorId(), SpaceID: binding.GetSpaceId(), SourceViewID: binding.GetSourceViewId(), Frequency: binding.GetFreq(), Status: binding.GetStatus(), ResultDatasetID: binding.GetResultDatasetId(), ResultViewID: binding.GetResultViewId(), SubjectMode: binding.GetSubjectMode(), SubjectsJSON: binding.GetSubjectsJson()})
		}
		if !rsp.GetPageResult().GetHasMore() && len(rsp.GetBindings()) < int(pageSize) {
			break
		}
	}
	return result, nil
}

func (c Client) GetFactor(ctx context.Context, id string) (compiler.FactorDescriptor, error) {
	if c.GetFactorFunc == nil {
		return compiler.FactorDescriptor{}, context.Canceled
	}
	return c.GetFactorFunc(ctx, id)
}

func (c Client) ListBindings(ctx context.Context, id string) ([]compiler.BindingDescriptor, error) {
	if c.ListBindingsFunc == nil {
		return nil, context.Canceled
	}
	return c.ListBindingsFunc(ctx, id)
}
