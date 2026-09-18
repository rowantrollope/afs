package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// BlobSink preserves the scanner's concurrent, incremental blob submission.
// Both transports ultimately use the retained pipelined Redis BlobWriter.
type BlobSink interface {
	Submit(context.Context, string, []byte, int64) error
	Flush(context.Context) error
}

func (s *Service) ImportWorkspace(ctx context.Context, name string, build func(BlobSink) (Manifest, error)) (WorkspaceMeta, error) {
	if build == nil {
		return WorkspaceMeta{}, errors.New("manifest builder is required")
	}
	return s.CreateWorkspaceStreaming(ctx, name, func(_ string, writer *BlobWriter) (Manifest, error) { return build(writer) })
}

type multipartBlobSink struct {
	mu     sync.Mutex
	writer *multipart.Writer
}

func (s *multipartBlobSink) Submit(ctx context.Context, id string, data []byte, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if size != int64(len(data)) {
		return errors.New("blob size does not match content")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="blob"`)
	header.Set("Content-Type", "application/octet-stream")
	header.Set("X-AFS-Blob-ID", id)
	header.Set("X-AFS-Blob-Size", strconv.FormatInt(size, 10))
	part, err := s.writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}
func (s *multipartBlobSink) Flush(ctx context.Context) error { return ctx.Err() }

// ImportWorkspace streams individual binary blobs; it never serializes all
// directory bodies into one JSON request or publishes a partly imported name.
func (c *CLIClient) ImportWorkspace(ctx context.Context, name string, build func(BlobSink) (Manifest, error)) (WorkspaceMeta, error) {
	if build == nil {
		return WorkspaceMeta{}, errors.New("manifest builder is required")
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	built := make(chan error, 1)
	go func() {
		manifest, err := build(&multipartBlobSink{writer: multipartWriter})
		if err == nil {
			var part io.Writer
			part, err = multipartWriter.CreateFormField("manifest")
			if err == nil {
				err = json.NewEncoder(part).Encode(manifest)
			}
		}
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = writer.CloseWithError(err)
		built <- err
	}()
	var result WorkspaceMeta
	err := c.request(ctx, http.MethodPost, "/v1/cli/import?name="+url.QueryEscape(name), multipartWriter.FormDataContentType(), reader, &result)
	_ = reader.CloseWithError(io.ErrClosedPipe)
	buildErr := <-built
	if buildErr != nil && !errors.Is(buildErr, io.ErrClosedPipe) {
		return WorkspaceMeta{}, buildErr
	}
	return result, err
}

func (h *serverHandler) cliImportRoute(w http.ResponseWriter, r *http.Request) {
	h.cliImportRouteWithIdleTimeout(w, r, cliHTTPIdleTimeout)
}

func (h *serverHandler) cliImportRouteWithIdleTimeout(w http.ResponseWriter, r *http.Request, idle time.Duration) {
	if r.Method != http.MethodPost {
		serverMethod(w, "POST")
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(idle)); err != nil {
		h.cliError(w, errors.New("cannot set import stream read deadline"))
		return
	}
	complete := false
	defer func() {
		if !complete {
			// Do not let net/http drain an incomplete body without a deadline
			// after an early rejection, malformed stream or read timeout.
			_ = controller.SetReadDeadline(time.Now())
		}
	}()
	fail := func(err error) {
		if !complete {
			w.Header().Set("Connection", "close")
			_ = controller.SetReadDeadline(time.Now())
		}
		h.cliError(w, err)
	}
	r.Body = &idleRequestBody{ReadCloser: r.Body, controller: controller, idle: idle}
	parts, err := r.MultipartReader()
	if err != nil {
		fail(errors.New("expected a multipart import stream"))
		return
	}
	meta, err := h.service.CreateWorkspaceStreaming(r.Context(), r.URL.Query().Get("name"), func(_ string, writer *BlobWriter) (Manifest, error) {
		uploaded := make(map[string]int64)
		for {
			part, err := parts.NextPart()
			if errors.Is(err, io.EOF) {
				return Manifest{}, errors.New("import stream has no manifest")
			}
			if err != nil {
				return Manifest{}, err
			}
			switch part.FormName() {
			case "blob":
				id := part.Header.Get("X-AFS-Blob-ID")
				decoded, err := hex.DecodeString(id)
				if err != nil || len(decoded) != sha256.Size {
					return Manifest{}, errors.New("invalid import blob ID")
				}
				size, err := strconv.ParseInt(part.Header.Get("X-AFS-Blob-Size"), 10, 64)
				if err != nil || size < 0 || size == int64(^uint64(0)>>1) {
					return Manifest{}, errors.New("invalid import blob size")
				}
				// The retained scanner and BlobWriter own one whole file at a
				// time. Network buffering stays bounded by that same unit.
				data, err := io.ReadAll(io.LimitReader(part, size+1))
				if err != nil {
					return Manifest{}, err
				}
				if int64(len(data)) != size {
					return Manifest{}, errors.New("import blob size does not match content")
				}
				sum := sha256.Sum256(data)
				if hex.EncodeToString(sum[:]) != id {
					return Manifest{}, errors.New("import blob hash does not match content")
				}
				if err = writer.Submit(r.Context(), id, data, size); err != nil {
					return Manifest{}, err
				}
				uploaded[id] = size
			case "manifest":
				var manifest Manifest
				decoder := json.NewDecoder(part)
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&manifest); err != nil {
					return Manifest{}, fmt.Errorf("invalid import manifest: %w", err)
				}
				if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
					return Manifest{}, errors.New("import manifest must contain one JSON value")
				}
				for id, size := range manifestBlobRefs(manifest) {
					if uploadedSize, ok := uploaded[id]; !ok || uploadedSize != size {
						return Manifest{}, errors.New("import manifest references a missing or mismatched blob")
					}
				}
				if _, err := parts.NextPart(); !errors.Is(err, io.EOF) {
					return Manifest{}, errors.New("manifest must end the import stream")
				}
				// Multipart EOF alone does not mean HTTP EOF: an unfinished
				// epilogue must not leave an unbounded drain after publication.
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					return Manifest{}, err
				}
				complete = true
				_ = controller.SetReadDeadline(time.Time{})
				return manifest, nil
			default:
				return Manifest{}, errors.New("unknown import part")
			}
		}
	})
	if err != nil {
		fail(err)
		return
	}
	serverJSON(w, http.StatusOK, meta)
}
