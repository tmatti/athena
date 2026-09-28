package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
	"github.com/tmatti/athena/internal/testdb"
)

const testAPIKey = "test-api-key-0123456789"

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

// testRouter builds the full authenticated router over a real Brain backed
// by the integration test database.
func testRouter(t *testing.T) http.Handler {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	brain := service.New(store.New(testdb.Pool(t)), nil, log)
	h := &Handlers{Brain: brain, Log: log}
	return NewRouter(RouterOptions{Log: log, APIKey: testAPIKey, V1: h.Routes})
}

func do(t *testing.T, router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func doJSON(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	return do(t, router, req)
}

// multipartBody builds a multipart/form-data body. A nil file omits the
// "file" part; an empty caption omits the "caption" part.
func multipartBody(t *testing.T, filename string, file []byte, caption string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if file != nil {
		fw, err := mw.CreateFormFile("file", filename)
		require.NoError(t, err)
		_, err = fw.Write(file)
		require.NoError(t, err)
	}
	if caption != "" {
		require.NoError(t, mw.WriteField("caption", caption))
	}
	require.NoError(t, mw.Close())
	return &buf, mw.FormDataContentType()
}

func upload(t *testing.T, router http.Handler, path, filename string, file []byte, caption string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartBody(t, filename, file, caption)
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", contentType)
	return do(t, router, req)
}

func createMemoryID(t *testing.T, router http.Handler) string {
	t.Helper()
	rec := doJSON(t, router, http.MethodPost, "/v1/memories", map[string]any{"content": "has images"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var m store.Memory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m.ID
}

func TestUploadImageToMemory(t *testing.T) {
	router := testRouter(t)
	memoryID := createMemoryID(t, router)

	rec := upload(t, router, "/v1/memories/"+memoryID+"/images", "cat.png", testPNG, "a cat")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	require.ElementsMatch(t,
		[]string{"id", "memory_id", "note_id", "filename", "content_type", "size_bytes", "sha256", "caption", "created_at"},
		keys(raw))
	require.Equal(t, memoryID, raw["memory_id"])
	require.Nil(t, raw["note_id"])
	require.Equal(t, "cat.png", raw["filename"])
	require.Equal(t, "image/png", raw["content_type"])
	require.EqualValues(t, len(testPNG), raw["size_bytes"])
	require.Equal(t, "a cat", raw["caption"])

	// The owner's GET now embeds the image, and the list endpoint wraps it.
	rec = do(t, router, httptest.NewRequest(http.MethodGet, "/v1/memories/"+memoryID, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var m store.Memory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	require.Len(t, m.Images, 1)
	require.Equal(t, raw["id"], m.Images[0].ID)

	rec = do(t, router, httptest.NewRequest(http.MethodGet, "/v1/memories/"+memoryID+"/images", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var list imageListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Images, 1)
	require.Equal(t, raw["id"], list.Images[0].ID)
}

func TestUploadImageToNoteAndEmptyList(t *testing.T) {
	router := testRouter(t)
	rec := doJSON(t, router, http.MethodPost, "/v1/notes", map[string]any{"title": "t", "content": "c"})
	require.Equal(t, http.StatusCreated, rec.Code)
	var n store.Note
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &n))

	rec = do(t, router, httptest.NewRequest(http.MethodGet, "/v1/notes/"+n.ID+"/images", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"images":[]}`, rec.Body.String())

	rec = upload(t, router, "/v1/notes/"+n.ID+"/images", "", testPNG, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var img store.Image
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &img))
	require.Equal(t, &n.ID, img.NoteID)
	require.Equal(t, "image.png", img.Filename)
	require.Nil(t, img.Caption)
}

func TestUploadImageErrors(t *testing.T) {
	router := testRouter(t)
	memoryID := createMemoryID(t, router)
	path := "/v1/memories/" + memoryID + "/images"

	t.Run("missing file", func(t *testing.T) {
		rec := upload(t, router, path, "", nil, "caption only")
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "invalid_body", decodeErrorBody(t, rec).Error.Code)
	})

	t.Run("not multipart", func(t *testing.T) {
		rec := doJSON(t, router, http.MethodPost, path, map[string]string{"file": "x"})
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "invalid_body", decodeErrorBody(t, rec).Error.Code)
	})

	t.Run("unsupported type", func(t *testing.T) {
		rec := upload(t, router, path, "notes.png", []byte("plain text, not an image"), "")
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
		require.Equal(t, "unsupported_media_type", decodeErrorBody(t, rec).Error.Code)
	})

	t.Run("empty file", func(t *testing.T) {
		rec := upload(t, router, path, "empty.png", []byte{}, "")
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	})

	t.Run("too large by file limit", func(t *testing.T) {
		// Just over the image limit but within the multipart overhead, so the
		// per-part cap (not MaxBytesReader) trips.
		big := append(bytes.Clone(testPNG), make([]byte, service.MaxImageBytes)...)
		rec := upload(t, router, path, "big.png", big, "")
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.Equal(t, "request_too_large", decodeErrorBody(t, rec).Error.Code)
	})

	t.Run("too large by body limit", func(t *testing.T) {
		big := append(bytes.Clone(testPNG), make([]byte, service.MaxImageBytes+maxUploadOverhead)...)
		rec := upload(t, router, path, "big.png", big, "")
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.Equal(t, "request_too_large", decodeErrorBody(t, rec).Error.Code)
	})

	t.Run("owner not found", func(t *testing.T) {
		rec := upload(t, router, "/v1/memories/00000000-0000-0000-0000-000000000000/images", "x.png", testPNG, "")
		require.Equal(t, http.StatusNotFound, rec.Code)
		rec = do(t, router, httptest.NewRequest(http.MethodGet, "/v1/notes/00000000-0000-0000-0000-000000000000/images", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("invalid owner id", func(t *testing.T) {
		rec := upload(t, router, "/v1/memories/not-a-uuid/images", "x.png", testPNG, "")
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "invalid_id", decodeErrorBody(t, rec).Error.Code)
	})
}

func TestImageContentDownload(t *testing.T) {
	router := testRouter(t)
	memoryID := createMemoryID(t, router)
	rec := upload(t, router, "/v1/memories/"+memoryID+"/images", `we"ird.png`, testPNG, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var img store.Image
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &img))

	rec = do(t, router, httptest.NewRequest(http.MethodGet, "/v1/images/"+img.ID+"/content", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, testPNG, rec.Body.Bytes())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, strconv.Itoa(len(testPNG)), rec.Header().Get("Content-Length"))
	etag := rec.Header().Get("ETag")
	require.Equal(t, `"`+img.SHA256+`"`, etag)
	require.Equal(t, `inline; filename="we\"ird.png"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "private, max-age=0", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	req := httptest.NewRequest(http.MethodGet, "/v1/images/"+img.ID+"/content", nil)
	req.Header.Set("If-None-Match", etag)
	rec = do(t, router, req)
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Body.Bytes())
	require.Equal(t, etag, rec.Header().Get("ETag"))

	req = httptest.NewRequest(http.MethodGet, "/v1/images/"+img.ID+"/content", nil)
	req.Header.Set("If-None-Match", `"something-else"`)
	rec = do(t, router, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, testPNG, rec.Body.Bytes())
}

func TestImageMetadataPatchAndDelete(t *testing.T) {
	router := testRouter(t)
	memoryID := createMemoryID(t, router)
	rec := upload(t, router, "/v1/memories/"+memoryID+"/images", "x.png", testPNG, "")
	require.Equal(t, http.StatusCreated, rec.Code)
	var img store.Image
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &img))
	path := "/v1/images/" + img.ID

	rec = do(t, router, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var got store.Image
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, img.ID, got.ID)

	rec = doJSON(t, router, http.MethodPatch, path, map[string]any{"caption": "labelled"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "labelled", *got.Caption)

	rec = doJSON(t, router, http.MethodPatch, path, map[string]any{"caption": nil})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Nil(t, got.Caption)

	rec = doJSON(t, router, http.MethodPatch, path, map[string]any{})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doJSON(t, router, http.MethodPatch, path, map[string]any{"caption": 42})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doJSON(t, router, http.MethodPatch, path, map[string]any{"filename": "nope"})
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = do(t, router, httptest.NewRequest(http.MethodDelete, path, nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = do(t, router, httptest.NewRequest(http.MethodDelete, path, nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = do(t, router, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = do(t, router, httptest.NewRequest(http.MethodGet, path+"/content", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWriteServiceErrorImageErrors(t *testing.T) {
	h := &Handlers{}
	for err, want := range map[error]int{
		service.ErrImageTooLarge:        http.StatusRequestEntityTooLarge,
		service.ErrUnsupportedImageType: http.StatusUnsupportedMediaType,
		service.ErrInvalidImageOwner:    http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		h.writeServiceError(rec, err)
		require.Equal(t, want, rec.Code, err.Error())
	}
}

func TestContentDisposition(t *testing.T) {
	require.Equal(t, `inline; filename="cat.png"`, contentDisposition("cat.png"))
	require.Equal(t, `inline; filename="a\"b\\c.png"`, contentDisposition(`a"b\c.png`))
	require.Equal(t, `inline; filename="caf_.png"; filename*=UTF-8''caf%C3%A9.png`, contentDisposition("café.png"))
}

func TestEtagMatches(t *testing.T) {
	require.True(t, etagMatches(`"abc"`, `"abc"`))
	require.True(t, etagMatches(`W/"abc"`, `"abc"`))
	require.True(t, etagMatches(`"x", "abc"`, `"abc"`))
	require.True(t, etagMatches(`*`, `"abc"`))
	require.False(t, etagMatches(`"abcd"`, `"abc"`))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
