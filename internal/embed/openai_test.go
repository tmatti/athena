package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICompatibleEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/embeddings", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req embeddingsRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "openai/text-embedding-3-small", req.Model)
		require.Equal(t, []string{"one", "two"}, req.Input)

		// Return out of order to verify index-based reordering.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0, 1, 0}},
				{"index": 0, "embedding": []float32{1, 0, 0}},
			},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatible(srv.URL+"/v1", "test-key", "openai/text-embedding-3-small", 3)
	vecs, err := c.Embed(context.Background(), []string{"one", "two"})
	require.NoError(t, err)
	require.Equal(t, [][]float32{{1, 0, 0}, {0, 1, 0}}, vecs)
}

func TestOpenAICompatibleErrors(t *testing.T) {
	t.Run("http error surfaces body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
		}))
		defer srv.Close()

		c := NewOpenAICompatible(srv.URL, "bad", "m", 3)
		_, err := c.Embed(context.Background(), []string{"x"})
		require.ErrorContains(t, err, "invalid api key")
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"index": 0, "embedding": []float32{1, 2}}},
			})
		}))
		defer srv.Close()

		c := NewOpenAICompatible(srv.URL, "k", "m", 3)
		_, err := c.Embed(context.Background(), []string{"x"})
		require.ErrorContains(t, err, "dimensions")
	})
}
