package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type referenceTransport func(*http.Request) (*http.Response, error)

func (f referenceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReferenceImportClaimsBeforePaidCallAndReplaysPrivateAsset(t *testing.T) {
	f := setup(t)
	var calls atomic.Int64
	video := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...)
	client := &http.Client{Transport: referenceTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == SnapAnyExtractURL {
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer only-parser-secret" {
				t.Fatal("missing parser authorization")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"medias":[{"media_type":"video","resource_url":"https://cdn.example.test/video.mp4","headers":{"Authorization":"forbidden","Cookie":"forbidden","Referer":"https://example.test/"}}]}`)), Header: make(http.Header)}, nil
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("secret sent to CDN")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(video)), Header: make(http.Header)}, nil
	})}
	f.s.SourceParser = &SourceParser{Key: "only-parser-secret", Client: client, Preflight: referenceFixturePreflight}
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 8.4, Width: 1080, Height: 1920}, nil
	}
	id := uuid.NewString()
	a, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://example.test/watch/1")
	if err != nil {
		t.Fatal(err)
	}
	if a.DurationSeconds != 8.4 || a.Checksum == "" || a.SourceURL != "https://example.test/watch/1" || !strings.HasPrefix(a.URL, f.s.Blobs.Origin) {
		t.Fatalf("invalid source projection: %+v", a)
	}
	b, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://example.test/watch/1")
	if err != nil || b.ID != a.ID || calls.Load() != 1 {
		t.Fatalf("replay made another paid call: %+v %v %d", b, err, calls.Load())
	}
	if _, err = f.s.ImportReference(context.Background(), f.team, f.user, id, "https://example.test/watch/2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed operation accepted: %v", err)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "cdn.example") || strings.Contains(string(raw), "secret") {
		t.Fatal("temporary provider data leaked")
	}
}

func TestReferenceImportUnknownNeverRetriesAndConfigurationDoesNotPoison(t *testing.T) {
	f := setup(t)
	id := uuid.NewString()
	if _, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://example.test/1"); err == nil {
		t.Fatal("missing key accepted")
	}
	var calls atomic.Int64
	f.s.SourceParser = &SourceParser{Key: "secret", Preflight: referenceFixturePreflight, Client: &http.Client{Transport: referenceTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("network includes secret")
	})}}
	if _, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://example.test/1"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	if _, err := f.s.ImportReference(context.Background(), f.team, f.user, id, "https://example.test/1"); err == nil || calls.Load() != 1 {
		t.Fatal("unknown result retried")
	}
}

func TestReferenceParserErrorsAndURLBoundaries(t *testing.T) {
	for _, source := range []string{"http://example.test/v", "https://user:password@example.test/v", "https://127.0.0.1/v", "https://localhost/v", "https://example.test/a\\b", "https://example.test/\n"} {
		if _, err := canonicalReferenceURL(source); err == nil {
			t.Fatalf("accepted unsafe URL %q", source)
		}
	}
	for _, status := range []int{401, 402, 429, 503} {
		p := SourceParser{Key: "secret", Client: &http.Client{Transport: referenceTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("raw-secret")), Header: make(http.Header)}, nil
		})}}
		if _, err := p.Extract(context.Background(), "https://example.test/v"); err == nil || strings.Contains(err.Error(), "raw-secret") {
			t.Fatalf("unsafe error for %d: %v", status, err)
		}
	}
}

func TestReferenceUploadRejectsInvalidContainerAndProbeLimits(t *testing.T) {
	f := setup(t)
	if _, err := f.s.UploadReference(context.Background(), f.team, "invalid.mp4", strings.NewReader("not a video"), ""); err == nil {
		t.Fatal("invalid container accepted")
	}
	f.s.ReferenceProbe = func(context.Context, string) (ReferenceMetadata, error) {
		return ReferenceMetadata{DurationSeconds: 90.01, Width: 10, Height: 10}, nil
	}
	video := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...)
	if _, err := f.s.UploadReference(context.Background(), f.team, "long.mp4", bytes.NewReader(video), ""); err == nil {
		t.Fatal("overlong video accepted")
	}
	assets, _ := f.s.Repo.Assets(context.Background(), f.team)
	if len(assets) != 1 {
		t.Fatalf("failed upload saved an asset: %d", len(assets))
	}
}

func TestReferenceHTTPClientBypassesEnvironmentProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("reference request must not use an environment proxy")
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)

	client := NewReferenceHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("reference transport must bypass environment proxies")
	}
}

func TestBoundedReferenceOutputStopsAtLimit(t *testing.T) {
	output := boundedReferenceOutput{limit: 4}
	if n, err := output.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("initial write failed: %d %v", n, err)
	}
	if n, err := output.Write([]byte("def")); err != io.ErrShortWrite || n != 1 {
		t.Fatalf("overflow was not stopped: %d %v", n, err)
	}
	if output.Len() != 4 || !output.exceeded {
		t.Fatalf("output limit not enforced: %d %t", output.Len(), output.exceeded)
	}
}
