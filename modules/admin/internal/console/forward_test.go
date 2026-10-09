package console

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"trpc.group/trpc-go/trpc-go/errs"
)

func TestWriteForwardResponseGzipsLargeJSONWhenAccepted(t *testing.T) {
	body := []byte(`{"rows":[` + strings.Repeat(`{"value":"1234567890"},`, 200) + `{}]}`)
	rr := httptest.NewRecorder()

	writeForwardResponse(rr, body, map[string]string{"accept_encoding": "gzip"})

	if got := rr.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rr.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	if rr.Body.Len() >= len(body) {
		t.Fatalf("compressed body len = %d, want less than original %d", rr.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("decoded body differs from original")
	}
}

func TestWriteForwardResponseKeepsSmallOrUnsupportedResponsesPlain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		headers map[string]string
	}{
		{name: "small", body: []byte(`{"ok":true}`), headers: map[string]string{"accept_encoding": "gzip"}},
		{name: "unsupported", body: []byte(strings.Repeat("x", 2048)), headers: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()

			writeForwardResponse(rr, tc.body, tc.headers)

			if got := rr.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("Content-Encoding = %q, want empty", got)
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if !bytes.Equal(rr.Body.Bytes(), tc.body) {
				t.Fatal("body differs from original")
			}
		})
	}
}

func TestWriteForwardError_ShouldWriteRetInfo(t *testing.T) {
	rr := httptest.NewRecorder()
	writeForwardError(context.Background(), rr, errs.New(10001, "forward failed"), map[string]string{"origin": "https://app.example.com"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ret_info")
	assert.Equal(t, "10001", rr.Header().Get("trpc-ret"))
}

func TestAddVaryHeader_ExistingValue_ShouldAppend(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Vary", "Origin")
	addVaryHeader(rr, "Accept-Encoding")
	assert.Equal(t, "Origin, Accept-Encoding", rr.Header().Get("Vary"))
}

func TestAddVaryHeader_DuplicateValue_ShouldKeepSingle(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Vary", "Accept-Encoding")
	addVaryHeader(rr, "Accept-Encoding")
	assert.Equal(t, "Accept-Encoding", rr.Header().Get("Vary"))
}
