package storageio

import (
	"context"
	"fmt"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

type AccessClient interface {
	WriteTimeSeriesRows(context.Context, *storagepb.WriteTimeSeriesRowsReq, ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error)
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
	WriteRecordRows(context.Context, *storagepb.WriteRecordRowsReq, ...client.Option) (*storagepb.WriteRecordRowsRsp, error)
	ReadRecordRows(context.Context, *storagepb.ReadRecordRowsReq, ...client.Option) (*storagepb.ReadRecordRowsRsp, error)
}

type Client struct {
	access   AccessClient
	auth     *storagepb.AuthInfo
	bindings map[string]Binding
}

func NewClientWithAccess(access AccessClient, auth *storagepb.AuthInfo, bindings []Binding) *Client {
	c := &Client{access: access, auth: auth, bindings: make(map[string]Binding, len(bindings))}
	for _, b := range bindings {
		c.bindings[b.DatasetID] = b
	}
	return c
}
func (c *Client) binding(datasetID string, role DatasetRole) (Binding, error) {
	b, ok := c.bindings[strings.TrimSpace(datasetID)]
	if !ok {
		return Binding{}, fmt.Errorf("unknown dataset %q", datasetID)
	}
	if b.Role != role {
		return Binding{}, fmt.Errorf("dataset %q role %q cannot be used as %q", datasetID, b.Role, role)
	}
	return b, nil
}
func ensureOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil || ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		if ret == nil {
			return fmt.Errorf("%s: empty response", action)
		}
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}
