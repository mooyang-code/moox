package gateway

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxRequestBodyBytes = 4 << 20

var errRequestBodyTooLarge = errors.New("request body exceeds 4 MiB limit")

func readBoundedBody(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxRequestBodyBytes {
		return nil, errRequestBodyTooLarge
	}
	return body, nil
}

func writeRequestBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRequestBodyTooLarge) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "invalid request body", http.StatusBadRequest)
}
