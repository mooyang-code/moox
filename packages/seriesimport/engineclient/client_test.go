package engineclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/packages/seriesformat"
	"github.com/stretchr/testify/require"
)

func TestClientReadsFieldsSubjectAndBounds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("X-Moox-Health-Auth"))
		switch r.URL.Path {
		case "/v1/series/fields":
			require.Equal(t, "spot", r.URL.Query().Get("dataset"))
			_, _ = w.Write([]byte(`{"fields":[{"name":"close","type":"double","fill":"prev_close"},{"name":"trades","type":"int","fill":"zero"}]}`))
		case "/v1/series/subject":
			_, _ = w.Write([]byte(`{"in_scope":` + map[bool]string{true: "true", false: "false"}[r.URL.Query().Get("subject") == "BTC"] + `}`))
		case "/v1/series/bounds":
			_, _ = w.Write([]byte(`{"exists":true,"earliest":"2026-10-01T00:00:00Z","latest":"2026-10-01T01:00:00Z"}`))
		default:
			http.Error(w, `{"error":"没有这个接口"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, AccessKey: "ak", SecretKey: "secret-secret-secret"}
	fields, err := c.Fields(context.Background(), "crypto", "spot", "1m")
	require.NoError(t, err)
	require.Equal(t, []seriesformat.Field{{Name: "close", Type: seriesformat.TypeDouble}, {Name: "trades", Type: seriesformat.TypeInt}}, fields)
	in, err := c.SubjectInScope(context.Background(), "crypto", "spot", "BTC")
	require.NoError(t, err)
	require.True(t, in)
	in, err = c.SubjectInScope(context.Background(), "crypto", "spot", "DOGE")
	require.NoError(t, err)
	require.False(t, in)
	b, err := c.Bounds(context.Background(), "crypto", "spot", "1m", "BTC")
	require.NoError(t, err)
	require.True(t, b.Exists)
	require.Equal(t, "2026-10-01T01:00:00Z", b.Latest)

	c.Endpoint = srv.URL + "/missing"
	_, err = c.Bounds(context.Background(), "crypto", "spot", "1m", "BTC")
	require.ErrorContains(t, err, "404")
	require.ErrorContains(t, (&Client{Endpoint: srv.URL}).get(context.Background(), "/x", nil, nil), "缺少因子引擎本地接口的鉴权密钥")
}
