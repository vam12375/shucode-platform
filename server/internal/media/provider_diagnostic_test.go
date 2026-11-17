package media

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderDiagnosticCapturesAndRedactsRejection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "request-fixture")
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"duration unsupported; fixture-key https://media.example.test/a?signature=private","api_key":"private","details":{"token":"private"}}}`))
	}))
	defer upstream.Close()
	provider := Provider{BaseURL: upstream.URL, Client: upstream.Client(), Key: "fixture-key"}
	_, err := provider.Submit(context.Background(), Model{}, Brief{}, ShotPlan{}, "https://example.test/input.png")
	var failure *providerHTTPError
	if !errors.As(err, &failure) || failure.diagnostic.HTTPStatus != 422 || failure.diagnostic.RequestID != "request-fixture" || !strings.Contains(failure.diagnostic.Message, "duration unsupported") {
		t.Fatalf("diagnostic lost: %+v %v", failure, err)
	}
	raw, _ := json.Marshal(failure.diagnostic)
	for _, private := range []string{"fixture-key", "https://", "private"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("private data retained: %s", raw)
		}
	}
}

func TestTerminalFailureDiagnosticSurvivesPersistenceAndRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Tt-Logid", "request-terminal")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"task-original"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"failed","error":{"code":"generation_failed","message":"upstream renderer unavailable"}}`))
	}))
	defer upstream.Close()
	f.s.Provider = func(context.Context, Model) (Provider, error) {
		return Provider{BaseURL: upstream.URL, Client: upstream.Client()}, nil
	}
	p := f.project(t)
	for range 4 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if len(p.ProviderDiagnostics) != 1 || p.ProviderDiagnostics[0].Message != "upstream renderer unavailable" || p.ProviderDiagnostics[0].RequestID != "request-terminal" || p.ProviderDiagnostics[0].Attempt != 1 {
		t.Fatalf("failure not persisted: %+v", p.ProviderDiagnostics)
	}
	raw, _ := json.Marshal(p.View())
	if strings.Contains(string(raw), "upstream renderer") || strings.Contains(string(raw), "providerDiagnostics") {
		t.Fatalf("private diagnostic leaked: %s", raw)
	}
	next, err := f.s.Action(ctx, f.team, p.ID, p.Version, "retry")
	if err != nil || len(next.ProviderDiagnostics) != 1 {
		t.Fatalf("retry discarded history: %+v %v", next, err)
	}
}

func TestDiagnosticRedactionBoundsAndDeduplication(t *testing.T) {
	p := &Project{Shots: []Shot{{Attempt: 1}}}
	d := ProviderDiagnostic{Message: strings.Repeat("x", 9000), Response: `{"authorization":"Bearer private","nested":{"password":"private","url":"https://private.test/path?token=private"}}`}
	recordProviderDiagnostic(p, 0, "video_query", d)
	recordProviderDiagnostic(p, 0, "video_query", d)
	if len(p.ProviderDiagnostics) != 1 || len(p.ProviderDiagnostics[0].Message) > 8192 || strings.Contains(p.ProviderDiagnostics[0].Response, "private") {
		t.Fatalf("unsafe/unbounded duplicate: %+v", p.ProviderDiagnostics)
	}
	for i := 0; i < 100; i++ {
		p.Shots[0].Attempt++
		recordProviderDiagnostic(p, 0, "video_query", d)
	}
	if len(p.ProviderDiagnostics) != 64 {
		t.Fatalf("unbounded history: %d", len(p.ProviderDiagnostics))
	}
}

func TestImageFailureKeepsPrivateDiagnostic(t *testing.T) {
	f, p := newToAPIsImageFixture(t, 0)
	ctx := context.Background()
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	f.status.Store("failed")
	p = forceImageQueryDue(t, f, p)
	f.s.Tick(ctx)
	p = current(t, f.fixture, p)
	if len(p.ProviderDiagnostics) != 1 || p.ProviderDiagnostics[0].Kind != "image" || p.ProviderDiagnostics[0].Code != "generation_failed" || p.ProviderDiagnostics[0].TaskID != "tsk_img_worker" {
		t.Fatalf("image diagnostic lost: %+v", p.ProviderDiagnostics)
	}
}

func TestProviderUnstructuredTerminalErrorRetainsRedactedText(t *testing.T) {
	var failure providerErrorResponse
	if err := json.Unmarshal([]byte(`"render engine failed at https://private.example/file?signature=secret"`), &failure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure.Message, "render engine failed") || strings.Contains(failure.Response, "https://") {
		t.Fatalf("unstructured failure lost or unsafe: %+v", failure)
	}
}
