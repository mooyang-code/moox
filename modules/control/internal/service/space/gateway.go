package space

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/gateway"
	"trpc.group/trpc-go/trpc-go/log"
)

type gatewayHandler struct {
	service Service
}

// RegisterGateway 注册 Space 管理台网关。
func RegisterGateway(service Service) {
	handler := &gatewayHandler{service: service}
	gateway.GetGatewayHandleInstance().Register(handler)
	log.Infof("[Space Gateway] registered service: %s", handler.ServiceID())
}

func (h *gatewayHandler) ServiceID() string { return "space" }

func (h *gatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	switch method {
	case "CreateSpace":
		var req struct {
			Space Space `json:"space"`
		}
		if err := decodeControlRequest(body, &req); err != nil {
			return nil, err
		}
		space, err := h.service.CreateSpace(ctx, &req.Space)
		return encodeControlResponse(map[string]interface{}{"space": space}, err)
	case "UpdateSpace":
		var req struct {
			Space Space `json:"space"`
		}
		if err := decodeControlRequest(body, &req); err != nil {
			return nil, err
		}
		space, err := h.service.UpdateSpace(ctx, &req.Space)
		return encodeControlResponse(map[string]interface{}{"space": space}, err)
	case "ListSpaces":
		var req struct {
			Owner  string  `json:"owner"`
			Status string  `json:"status"`
			Page   PageReq `json:"page"`
		}
		if err := decodeControlRequest(body, &req); err != nil {
			return nil, err
		}
		spaces, page, err := h.service.ListSpaces(ctx, req.Owner, req.Status, req.Page)
		return encodeControlResponse(map[string]interface{}{"spaces": spaces, "page_result": page}, err)
	case "ListSpaceMembers":
		var req struct {
			SpaceID string  `json:"space_id"`
			Page    PageReq `json:"page"`
		}
		if err := decodeControlRequest(body, &req); err != nil {
			return nil, err
		}
		members, page, err := h.service.ListSpaceMembers(ctx, req.SpaceID, req.Page)
		return encodeControlResponse(map[string]interface{}{"members": members, "page_result": page}, err)
	default:
		return nil, fmt.Errorf("unsupported space method: %s", method)
	}
}

func decodeControlRequest(body []byte, target interface{}) error {
	if len(body) == 0 {
		body = []byte("{}")
	}
	return json.Unmarshal(body, target)
}

func encodeControlResponse(data map[string]interface{}, err error) ([]byte, error) {
	if err != nil {
		return json.Marshal(map[string]interface{}{"code": 1, "message": err.Error()})
	}
	data["code"] = 0
	data["message"] = "success"
	return json.Marshal(data)
}
