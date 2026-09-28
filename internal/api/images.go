package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tmatti/athena/internal/service"
	"github.com/tmatti/athena/internal/store"
)

const (
	// maxUploadOverhead is headroom above MaxImageBytes for multipart framing
	// and the optional caption field.
	maxUploadOverhead = 64 << 10 // 64 KiB
	maxCaptionBytes   = 16 << 10 // 16 KiB
)

type imageListResponse struct {
	Images []store.Image `json:"images"`
}

// imageUpdateRequest keeps caption raw so an absent field ("nothing to
// update") is distinguishable from an explicit null ("clear the caption").
type imageUpdateRequest struct {
	Caption json.RawMessage `json:"caption"`
}

func (h *Handlers) uploadMemoryImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	h.uploadImage(w, r, store.MemoryOwner(id))
}

func (h *Handlers) uploadNoteImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	h.uploadImage(w, r, store.NoteOwner(id))
}

// uploadImage streams a multipart/form-data body with a required "file" part
// and an optional "caption" part. Nothing spills to disk: the file part is
// read into memory with a hard cap one byte over the limit so oversize
// uploads are detected without buffering them whole.
func (h *Handlers) uploadImage(w http.ResponseWriter, r *http.Request, owner store.ImageOwner) {
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxImageBytes+maxUploadOverhead)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request must be multipart/form-data")
		return
	}

	var (
		data     []byte
		filename string
		caption  *string
		gotFile  bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeUploadReadError(w, err)
			return
		}
		switch part.FormName() {
		case "file":
			if gotFile {
				writeError(w, http.StatusBadRequest, "invalid_body", "only one file may be uploaded per request")
				return
			}
			gotFile = true
			filename = part.FileName()
			data, err = io.ReadAll(io.LimitReader(part, service.MaxImageBytes+1))
			if err != nil {
				writeUploadReadError(w, err)
				return
			}
			if len(data) > service.MaxImageBytes {
				h.writeServiceError(w, service.ErrImageTooLarge)
				return
			}
		case "caption":
			raw, err := io.ReadAll(io.LimitReader(part, maxCaptionBytes+1))
			if err != nil {
				writeUploadReadError(w, err)
				return
			}
			if len(raw) > maxCaptionBytes {
				writeError(w, http.StatusBadRequest, "invalid_request", "caption is too long")
				return
			}
			c := string(raw)
			caption = &c
		}
		_ = part.Close()
	}
	if !gotFile {
		writeError(w, http.StatusBadRequest, "invalid_body", `multipart field "file" is required`)
		return
	}

	img, err := h.Brain.AttachImage(r.Context(), owner, filename, data, caption)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, img)
}

// writeUploadReadError maps a failure reading the multipart body: exceeding
// the MaxBytesReader cap is 413, anything else is a malformed body.
func writeUploadReadError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", service.ErrImageTooLarge.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_body", "malformed multipart body")
}

func (h *Handlers) listMemoryImages(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	h.listImages(w, r, store.MemoryOwner(id))
}

func (h *Handlers) listNoteImages(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	h.listImages(w, r, store.NoteOwner(id))
}

func (h *Handlers) listImages(w http.ResponseWriter, r *http.Request, owner store.ImageOwner) {
	images, err := h.Brain.ListImages(r.Context(), owner)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	if images == nil {
		images = []store.Image{}
	}
	writeJSON(w, http.StatusOK, imageListResponse{Images: images})
}

func (h *Handlers) getImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	img, err := h.Brain.GetImage(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (h *Handlers) getImageContent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// Answer conditional requests from metadata alone so a cache hit never
	// reads the image bytes.
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		img, err := h.Brain.GetImage(r.Context(), id)
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		if etagMatches(inm, imageETag(img)) {
			setImageCacheHeaders(w, img)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	img, data, err := h.Brain.GetImageData(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	setImageCacheHeaders(w, img)
	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Content-Disposition", contentDisposition(img.Filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *Handlers) updateImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req imageUpdateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Caption) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "nothing to update")
		return
	}
	var caption *string
	if err := json.Unmarshal(req.Caption, &caption); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "caption must be a string or null")
		return
	}
	img, err := h.Brain.UpdateImageCaption(r.Context(), id, caption)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, img)
}

func (h *Handlers) deleteImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.Brain.DeleteImage(r.Context(), id); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func imageETag(img store.Image) string {
	return `"` + img.SHA256 + `"`
}

func setImageCacheHeaders(w http.ResponseWriter, img store.Image) {
	w.Header().Set("ETag", imageETag(img))
	w.Header().Set("Cache-Control", "private, max-age=0")
}

// etagMatches implements If-None-Match's weak comparison against a strong
// ETag: "*" or any listed tag (with or without a W/ prefix) matches.
func etagMatches(header, etag string) bool {
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}

// contentDisposition builds an inline Content-Disposition with a quoted
// ASCII filename (quotes/backslashes escaped, other non-printables replaced)
// plus an RFC 5987 filename* when the name is not plain ASCII.
func contentDisposition(name string) string {
	var ascii strings.Builder
	plain := true
	for _, r := range name {
		switch {
		case r == '"' || r == '\\':
			ascii.WriteByte('\\')
			ascii.WriteRune(r)
		case r < 0x20 || r >= 0x7f:
			ascii.WriteByte('_')
			plain = false
		default:
			ascii.WriteRune(r)
		}
	}
	v := fmt.Sprintf(`inline; filename="%s"`, ascii.String())
	if !plain {
		v += "; filename*=UTF-8''" + rfc5987Escape(name)
	}
	return v
}

// rfc5987Escape percent-encodes everything outside RFC 5987 attr-char.
func rfc5987Escape(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte(attrChars, c) >= 0 {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", c)
	}
	return sb.String()
}
