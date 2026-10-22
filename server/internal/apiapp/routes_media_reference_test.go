package apiapp

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestReferenceRoutesAuthenticateLimitUploadsAndHideConfiguration(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	f.deps.Media.ReferenceProbe = func(context.Context, string) (media.ReferenceMetadata, error) {
		return media.ReferenceMetadata{DurationSeconds: 1.3, Width: 100, Height: 200}, nil
	}
	request := func(method, path string, body *bytes.Buffer, auth bool, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/api/v1/media/references/import", bytes.NewBufferString(`{"id":"bad","url":"https://example.test/v"}`), false, "application/json"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/console/api/media/source-parser", bytes.NewBuffer(nil), true, ""); w.Code == 200 {
		t.Fatal("device can read admin config")
	}
	if w := request("POST", "/api/v1/media/references/import", bytes.NewBufferString(`{"id":"bad","url":"https://example.test/v","apiKey":"secret"}`), true, "application/json"); w.Code != 400 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "original.mp4")
	part.Write(append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}, make([]byte, 512)...))
	writer.Close()
	w := request("POST", "/api/v1/media/references/upload", body, true, writer.FormDataContentType())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"durationSeconds":1.3`) || !strings.Contains(w.Body.String(), `"checksum"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
