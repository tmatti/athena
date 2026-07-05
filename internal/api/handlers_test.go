package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/store"
)

// decodeErrorBody unmarshals the {"error":{"code":...,"message":...}} shape
// written by writeError/writeServiceError.
func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestWriteServiceErrorInvalidCursor(t *testing.T) {
	h := &Handlers{}
	rec := httptest.NewRecorder()

	h.writeServiceError(rec, store.ErrInvalidCursor)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeErrorBody(t, rec)
	require.Equal(t, "invalid_request", body.Error.Code)
}

func TestWriteServiceErrorDefaultHidesInternalDetails(t *testing.T) {
	h := &Handlers{} // no Log set: must not panic, should default to slog.Default()
	rec := httptest.NewRecorder()

	h.writeServiceError(rec, errors.New("connection to db at 10.0.0.5 refused"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	body := decodeErrorBody(t, rec)
	require.Equal(t, "internal", body.Error.Code)
	require.NotContains(t, body.Error.Message, "10.0.0.5")
	require.Equal(t, "internal error", body.Error.Message)
}

func TestDecodeBodyRejectsOversizedBody(t *testing.T) {
	large := strings.Repeat("a", maxBodyBytes+1)
	payload, err := json.Marshal(map[string]string{"content": large})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/memories", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	var dst map[string]string
	ok := decodeBody(rec, req, &dst)

	require.False(t, ok)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	body := decodeErrorBody(t, rec)
	require.Equal(t, "request_too_large", body.Error.Code)
}

func TestDecodeBodyAcceptsBodyWithinLimit(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"content": "hello"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/memories", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	var dst map[string]string
	ok := decodeBody(rec, req, &dst)

	require.True(t, ok)
	require.Equal(t, "hello", dst["content"])
}
