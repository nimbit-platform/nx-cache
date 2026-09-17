package cacheapi

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nimbit-platform/nx-cache/internal/cleanup"
	"github.com/nimbit-platform/nx-cache/internal/storage"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

var hashPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type Handler struct {
	Backend       storage.Backend
	Store         store.Store
	Cleaner       *cleanup.Cleaner
	CleanupOnSave bool
	MaxUpload     int64
	Now           func() time.Time
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func ValidHash(hash string) bool {
	return hashPattern.MatchString(hash)
}

func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	if !ValidHash(hash) {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	cl := r.Header.Get("Content-Length")
	if cl == "" {
		http.Error(w, "Content-Length header is required", http.StatusLengthRequired)
		return
	}
	size, err := strconv.ParseInt(cl, 10, 64)
	if err != nil || size < 0 {
		http.Error(w, "Content-Length header is required", http.StatusLengthRequired)
		return
	}
	if h.MaxUpload > 0 && size > h.MaxUpload {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	exists, err := h.Backend.Exists(r.Context(), hash)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, "Cannot override an existing record", http.StatusConflict)
		return
	}

	err = h.Backend.Put(r.Context(), hash, r.Body, size)
	if errors.Is(err, storage.ErrExists) {
		http.Error(w, "Cannot override an existing record", http.StatusConflict)
		return
	}
	if errors.Is(err, storage.ErrIncomplete) {
		http.Error(w, "upload did not match Content-Length", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := h.Store.UpsertEntry(r.Context(), hash, size, h.now()); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if h.CleanupOnSave && h.Cleaner != nil {
		h.Cleaner.MaybeRun(r.Context())
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "Successfully uploaded")
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	if !ValidHash(hash) {
		http.Error(w, "The record was not found", http.StatusNotFound)
		return
	}
	body, size, err := h.Backend.Get(r.Context(), hash)
	if errors.Is(err, storage.ErrNotFound) {
		_ = h.Store.RecordMiss(r.Context(), h.now())
		http.Error(w, "The record was not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer body.Close()
	_ = h.Store.RecordHit(r.Context(), hash, h.now())

	w.Header().Set("Content-Type", "application/octet-stream")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *Handler) Head(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	if !ValidHash(hash) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	ok, err := h.Backend.Exists(r.Context(), hash)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}
