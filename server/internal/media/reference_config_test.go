package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestReferenceImportFreezesResolvedKeyBeforeAdmission(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := uuid.NewString()
	key := "first-key"
	reads := 0
	paid := 0
	blocked := true
	video := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...)
	f.s.SourceParser = &SourceParser{
		Preflight: referenceFixturePreflight,
		ResolveKey: func(context.Context) (string, error) {
			reads++
			if blocked {
				return "", errors.New("store error contains sensitive data")
			}
			return key, nil
		},
		Client: &http.Client{Transport: referenceTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() == SnapAnyExtractURL {
				paid++
				want := "first-key"
				if paid == 2 {
					want = "second-key"
				}
				if r.Header.Get("Authorization") != "Bearer "+want {
					t.Fatal("import used a stale or unfrozen key")
				}
				key = "second-key"
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"medias":[{"media_type":"video","resource_url":"https://cdn.example.test/video.mp4"}]}`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(video)), Header: make(http.Header)}, nil
		})},
	}
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 1, Width: 100, Height: 200}, nil
	}
	if _, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video"); err == nil || err.Error() != "source_parser_configuration_unavailable" || paid != 0 {
		t.Fatal("configuration failure was not safely rejected before admission")
	}
	blocked = false
	a, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
	if err != nil || reads != 2 || paid != 1 {
		t.Fatalf("configuration failure poisoned operation or key read more than once: %v, reads=%d paid=%d", err, reads, paid)
	}
	blocked = true
	b, err := f.s.ImportReference(ctx, f.team, f.user, id, "https://example.test/video")
	if err != nil || b.ID != a.ID || reads != 2 || paid != 1 {
		t.Fatal("replay resolved configuration or invoked provider again")
	}
	blocked = false
	if _, err := f.s.ImportReference(ctx, f.team, f.user, uuid.NewString(), "https://example.test/video"); err != nil || reads != 3 || paid != 2 {
		t.Fatalf("new operation did not read updated key: %v", err)
	}
}
