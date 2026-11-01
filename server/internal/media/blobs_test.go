package media

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type blockedBlobReader struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockedBlobReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.reader.Read(p)
}

type unreadBlobReader struct{}

func (unreadBlobReader) Read([]byte) (int, error) {
	return 0, errors.New("existing stable identity must not consume replacement bytes")
}

func TestPutWithIDConcurrentPublishNeverOverwritesExistingDelivery(t *testing.T) {
	b := Blobs{Root: t.TempDir()}
	team, id := uuid.NewString(), uuid.NewString()
	stale := &blockedBlobReader{reader: strings.NewReader("stale-worker-content"), entered: make(chan struct{}), release: make(chan struct{})}
	type result struct {
		id   string
		size int64
		err  error
	}
	finished := make(chan result, 1)
	go func() { id, n, err := b.PutWithID(team, id, stale, MaxVideo); finished <- result{id, n, err} }()
	select {
	case <-stale.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first writer did not reach blocked copy")
	}
	delivered := "accepted-delivery"
	actual, size, err := b.PutWithID(team, id, strings.NewReader(delivered), MaxVideo)
	if err != nil || actual != id || size != int64(len(delivered)) {
		close(stale.release)
		t.Fatalf("winner publish id=%s size=%d err=%v", actual, size, err)
	}
	close(stale.release)
	select {
	case staleResult := <-finished:
		if staleResult.err != nil || staleResult.id != id || staleResult.size != size {
			t.Fatalf("stale writer must reuse winner: %+v", staleResult)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale writer did not finish")
	}
	raw, err := os.ReadFile(b.Path(team, id))
	if err != nil || string(raw) != delivered {
		t.Fatalf("stale writer overwrote delivered bytes: %q %v", raw, err)
	}
	actual, size, err = b.PutWithID(team, id, unreadBlobReader{}, MaxVideo)
	if err != nil || actual != id || size != int64(len(delivered)) {
		t.Fatalf("stable read unexpectedly consumed reader: %s %d %v", actual, size, err)
	}
	temporary, err := filepath.Glob(filepath.Join(b.Root, team, ".upload-*"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary writer files leaked: %v %v", temporary, err)
	}
}

func TestPutWithIDRejectsIncompleteExistingBlobWithoutReplacement(t *testing.T) {
	for _, existing := range []string{"", "too-large"} {
		t.Run(existing, func(t *testing.T) {
			b := Blobs{Root: t.TempDir()}
			team, id := uuid.NewString(), uuid.NewString()
			if err := os.MkdirAll(filepath.Join(b.Root, team), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(b.Path(team, id), []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.PutWithID(team, id, strings.NewReader("new"), 3); err == nil {
				t.Fatal("invalid existing blob was replaced")
			}
			raw, err := os.ReadFile(b.Path(team, id))
			if err != nil || string(raw) != existing {
				t.Fatalf("invalid original changed: %q %v", raw, err)
			}
		})
	}
}
