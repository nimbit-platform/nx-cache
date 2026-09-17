package cacheapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

func TestValidHash(t *testing.T) {
	if !ValidHash("abc123") || !ValidHash("A.b_c-9") {
		t.Fatal("expected valid hashes")
	}
	if ValidHash("") || ValidHash("../etc") || ValidHash("has space") || ValidHash("slash/x") || ValidHash(".meta") || ValidHash(".") || ValidHash("..") {
		t.Fatal("expected invalid hashes")
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if ValidHash(string(long)) {
		t.Fatal("hash too long")
	}
}

func testHandler() (*Handler, http.Handler) {
	mem := storage.NewMemory()
	h := &Handler{
		Backend:   mem,
		Store:     store.NewObject(mem),
		MaxUpload: 64,
	}
	r := chi.NewRouter()
	r.Put("/v1/cache/{hash}", h.Put)
	r.Get("/v1/cache/{hash}", h.Get)
	r.Head("/v1/cache/{hash}", h.Head)
	return h, r
}

func TestPutGetHeadContract(t *testing.T) {
	_, mux := testHandler()
	payload := []byte("nx-tar-bytes")

	req := httptest.NewRequest(http.MethodPut, "/v1/cache/task-1", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusLengthRequired {
		t.Fatalf("missing content-length: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/cache/task-1", bytes.NewReader(payload))
	req.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "Successfully uploaded" {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/cache/task-1", bytes.NewReader(payload))
	req.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("overwrite: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/cache/task-1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 200 || !bytes.Equal(body, payload) {
		t.Fatalf("get: %d %s", rec.Code, body)
	}
	if rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("content-type %s", rec.Header().Get("Content-Type"))
	}

	req = httptest.NewRequest(http.MethodHead, "/v1/cache/task-1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("head: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/cache/missing", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("miss: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/cache/not%20ok", bytes.NewReader(payload))
	req.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("invalid hash: %d", rec.Code)
	}

	big := bytes.Repeat([]byte("x"), 80)
	req = httptest.NewRequest(http.MethodPut, "/v1/cache/huge", bytes.NewReader(big))
	req.Header.Set("Content-Length", strconv.Itoa(len(big)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", rec.Code)
	}
}

type failUpsert struct {
	store.Store
}

func (f failUpsert) UpsertEntry(context.Context, string, int64, time.Time, store.TaskInfo) error {
	return errors.New("catalog down")
}

func TestPutRollsBackObjectWhenCatalogFails(t *testing.T) {
	mem := storage.NewMemory()
	h := &Handler{
		Backend:   mem,
		Store:     failUpsert{Store: store.NewObject(mem)},
		MaxUpload: 64,
	}
	r := chi.NewRouter()
	r.Put("/v1/cache/{hash}", h.Put)
	payload := []byte("nx-tar-bytes")
	req := httptest.NewRequest(http.MethodPut, "/v1/cache/task-9", bytes.NewReader(payload))
	req.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	ok, err := mem.Exists(context.Background(), "task-9")
	if err != nil || ok {
		t.Fatalf("object should be deleted after catalog failure, exists=%v err=%v", ok, err)
	}
}
