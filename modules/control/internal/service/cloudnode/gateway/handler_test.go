package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseMethodToRouteDeleteCloudAccountUsesBodyAccountIDAsQuery(t *testing.T) {
	handler := &CloudNodeGatewayHandler{}

	route, err := handler.parseMethodToRoute("DeleteCloudAccount", []byte(`{"account_id":"account-1"}`))
	if err != nil {
		t.Fatalf("parseMethodToRoute returned error: %v", err)
	}

	if route.HTTPMethod != "DELETE" {
		t.Fatalf("HTTPMethod = %q, want DELETE", route.HTTPMethod)
	}
	if route.Path != "/api/v1/cloud_account/delete?account_id=account-1" {
		t.Fatalf("Path = %q, want account_id query", route.Path)
	}
	if len(route.Body) != 0 {
		t.Fatalf("Body length = %d, want 0", len(route.Body))
	}
}

func TestParseMethodToRouteInvokeFunction(t *testing.T) {
	handler := &CloudNodeGatewayHandler{}
	body := []byte(`{"node_id":"node-1","event_data":{"action":"task"}}`)

	route, err := handler.parseMethodToRoute("InvokeFunction", body)
	if err != nil {
		t.Fatalf("parseMethodToRoute returned error: %v", err)
	}

	if route.HTTPMethod != "POST" {
		t.Fatalf("HTTPMethod = %q, want POST", route.HTTPMethod)
	}
	if route.Path != "/api/v1/cloud_node/invoke" {
		t.Fatalf("Path = %q, want invoke route", route.Path)
	}
	if string(route.Body) != string(body) {
		t.Fatalf("Body = %s, want %s", string(route.Body), string(body))
	}
}

func TestExecuteRequestReturnsConflictBodyWithoutWrappingAsError(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.DELETE("/api/v1/cloud_account/delete", func(c *gin.Context) {
		c.JSON(http.StatusConflict, gin.H{
			"code":    http.StatusConflict,
			"message": "account is referenced",
			"data":    []any{},
		})
	})
	handler := &CloudNodeGatewayHandler{engine: engine}

	req, err := http.NewRequest(http.MethodDelete, "/api/v1/cloud_account/delete?account_id=account-1", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}

	body, err := handler.executeRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("executeRequest returned error: %v", err)
	}
	if !strings.Contains(string(body), `"code":409`) {
		t.Fatalf("expected conflict response body to be returned, got %s", string(body))
	}
}
