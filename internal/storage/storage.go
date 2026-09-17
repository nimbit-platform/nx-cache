package storage

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	ErrNotFound    = errors.New("cache record not found")
	ErrExists      = errors.New("cache record already exists")
	ErrIncomplete  = errors.New("upload did not match Content-Length")
	ErrInvalidHash = errors.New("invalid cache hash")
)

// Object is a stored cache artifact.
type Object struct {
	Hash         string
	Size         int64
	LastModified time.Time
}

// Backend stores Nx task output archives.
type Backend interface {
	Exists(ctx context.Context, hash string) (bool, error)
	Put(ctx context.Context, hash string, r io.Reader, size int64) error
	Get(ctx context.Context, hash string) (body io.ReadCloser, size int64, err error)
	Delete(ctx context.Context, hashes []string) error
	List(ctx context.Context) ([]Object, error)
	EnsureBucket(ctx context.Context) error
}

type memObj struct {
	data     []byte
	modified time.Time
}

// Memory is an in-process backend used by tests.
type Memory struct {
	mu      sync.Mutex
	objects map[string]memObj
	now     func() time.Time
}

func NewMemory() *Memory {
	return &Memory{
		objects: make(map[string]memObj),
		now:     time.Now,
	}
}

func (m *Memory) Exists(_ context.Context, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[hash]
	return ok, nil
}

func (m *Memory) Put(_ context.Context, hash string, r io.Reader, size int64) error {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return ErrIncomplete
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[hash]; ok {
		return ErrExists
	}
	m.objects[hash] = memObj{data: data, modified: m.now()}
	return nil
}

func (m *Memory) Get(_ context.Context, hash string) (io.ReadCloser, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[hash]
	if !ok {
		return nil, 0, ErrNotFound
	}
	return io.NopCloser(newBytesReader(obj.data)), int64(len(obj.data)), nil
}

func (m *Memory) Delete(_ context.Context, hashes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range hashes {
		delete(m.objects, h)
	}
	return nil
}

func (m *Memory) List(_ context.Context) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Object, 0, len(m.objects))
	for hash, obj := range m.objects {
		out = append(out, Object{Hash: hash, Size: int64(len(obj.data)), LastModified: obj.modified})
	}
	return out, nil
}

func (m *Memory) EnsureBucket(context.Context) error { return nil }

func (m *Memory) SetTime(t time.Time) {
	m.now = func() time.Time { return t }
}

type bytesReader struct {
	b []byte
	i int
}

func newBytesReader(b []byte) *bytesReader { return &bytesReader{b: b} }

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
