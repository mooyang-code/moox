package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadBoundedBodyAllowsExactlyFourMiB(t *testing.T) {
	want := bytes.Repeat([]byte("a"), maxRequestBodyBytes)
	req := httptest.NewRequest("POST", "/api/admin/test/Write", bytes.NewReader(want))

	got, err := readBoundedBody(req.Body)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestReadBoundedBodyRejectsMoreThanFourMiB(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/admin/test/Write", bytes.NewReader(bytes.Repeat([]byte("a"), maxRequestBodyBytes+1)))

	_, err := readBoundedBody(req.Body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errRequestBodyTooLarge))
}

func TestReadAndRestoreBodyPreservesExactBytes(t *testing.T) {
	want := []byte("{\"message\":\"signed bytes\"}")
	req := httptest.NewRequest("POST", "/api/admin/test/Write", bytes.NewReader(want))

	signed, err := readAndRestoreBody(req)
	require.NoError(t, err)
	forwarded, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, want, signed)
	assert.Equal(t, signed, forwarded)
}

func TestWriteRequestBodyErrorUsesPayloadTooLargeStatus(t *testing.T) {
	w := httptest.NewRecorder()

	writeRequestBodyError(w, errRequestBodyTooLarge)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
