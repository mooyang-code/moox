package factorio

import (
	"context"
	"fmt"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// Client is an adapter boundary for the Factor metadata API. Keeping the
// generated tRPC proxy behind function fields makes catalog use easy to test.
type Client struct {
	ListFactorSetsFunc func(context.Context) ([]compiler.FactorSetDescriptor, error)
	ListFactorsFunc    func(context.Context, compiler.FactorSetDescriptor) ([]compiler.FactorDescriptor, error)
}

// RPCClient adapts the FactorMgr metadata proxy to the compiler catalog.
type RPCClient struct {
	Proxy    factorpb.FactorMgrClientProxy
	PageSize uint32
}

func (c *RPCClient) ListFactorSets(ctx context.Context) ([]compiler.FactorSetDescriptor, error) {
	if c == nil || c.Proxy == nil {
		return nil, context.Canceled
	}
	pageSize := c.pageSize()
	var result []compiler.FactorSetDescriptor
	for page := uint32(1); ; page++ {
		rsp, err := c.Proxy.ListFactorSets(ctx, &factorpb.ListFactorSetsReq{Page: &commonpb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, context.Canceled
		}
		if err := factorRPCReturnError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, item := range rsp.GetFactorSets() {
			if item == nil || item.GetFactorSet() == nil {
				continue
			}
			set := item.GetFactorSet()
			result = append(result, compiler.FactorSetDescriptor{
				SetID: set.GetSetId(), Status: set.GetStatus(), ResultDatasetID: set.GetResultDatasetId(),
				SourceDatasetID: set.GetSourceDatasetId(), Frequency: set.GetFreq(), SubjectMode: set.GetSubjectMode(),
				Subjects: append([]string(nil), set.GetSubjects()...),
			})
		}
		if pageDone(rsp.GetPageResult(), len(rsp.GetFactorSets()), pageSize) {
			break
		}
	}
	return result, nil
}

func (c *RPCClient) ListFactors(ctx context.Context, set compiler.FactorSetDescriptor) ([]compiler.FactorDescriptor, error) {
	if c == nil || c.Proxy == nil {
		return nil, context.Canceled
	}
	setID := set.SetID
	if setID == "" {
		return nil, compiler.DependencyMismatchError(fmt.Errorf("factor set id is empty"))
	}
	pageSize := c.pageSize()
	var result []compiler.FactorDescriptor
	for page := uint32(1); ; page++ {
		rsp, err := c.Proxy.ListFactors(ctx, &factorpb.ListFactorsReq{SetId: setID, Page: &commonpb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, context.Canceled
		}
		if err := factorRPCReturnError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, info := range rsp.GetFactors() {
			if info == nil || info.GetFactor() == nil {
				continue
			}
			factor := info.GetFactor()
			status, used := usageStatus(info.GetUsages(), setID)
			if !used {
				// The backend filters by set_id, so this is only a defensive guard.
				continue
			}
			result = append(result, compiler.FactorDescriptor{
				FactorID: factor.GetFactorId(), SetID: setID, Outputs: append([]string(nil), factor.GetOutputs()...),
				Status: status, ResultDatasetID: set.ResultDatasetID,
				SourceHash: factor.GetSourceHash(), InputColumns: append([]string(nil), factor.GetInputColumns()...),
				ParamsJSON: factor.GetParamsJson(), LookbackPeriods: int(factor.GetLookbackPeriods()),
			})
		}
		if pageDone(rsp.GetPageResult(), len(rsp.GetFactors()), pageSize) {
			break
		}
	}
	return result, nil
}

// usageStatus returns the member status of a definition inside one set.
func usageStatus(usages []*factorpb.FactorUsage, setID string) (string, bool) {
	for _, usage := range usages {
		if usage.GetSetId() == setID {
			return usage.GetStatus(), true
		}
	}
	return "", false
}

func (c *RPCClient) pageSize() uint32 {
	if c != nil && c.PageSize > 0 {
		return c.PageSize
	}
	return 500
}

func factorRPCReturnError(info *commonpb.RetInfo) error {
	if info == nil {
		return context.Canceled
	}
	if info.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	err := fmt.Errorf("factor rpc %s: %s", info.GetCode().String(), info.GetMsg())
	if info.GetCode() != commonpb.ErrorCode_INNER_ERR {
		return compiler.DependencyMismatchError(err)
	}
	return err
}

func pageDone(page *commonpb.PageResult, count int, pageSize uint32) bool {
	if page != nil {
		return !page.GetHasMore()
	}
	return count < int(pageSize)
}

func (c Client) ListFactorSets(ctx context.Context) ([]compiler.FactorSetDescriptor, error) {
	if c.ListFactorSetsFunc == nil {
		return nil, context.Canceled
	}
	return c.ListFactorSetsFunc(ctx)
}

func (c Client) ListFactors(ctx context.Context, set compiler.FactorSetDescriptor) ([]compiler.FactorDescriptor, error) {
	if c.ListFactorsFunc == nil {
		return nil, context.Canceled
	}
	return c.ListFactorsFunc(ctx, set)
}
