package apiapp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vam12375/shucode-platform/server/internal/contracts"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestMediaLibraryUploadRenameReviewAndStrictIdentity(t *testing.T) {
	f, token, _ := newUserConfigPolicyFixture(t)
	f.deps.Media.Blobs.Root = t.TempDir()
	request := func(method, path, body, contentType string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		f.engine.ServeHTTP(w, r)
		return w
	}
	raw, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "product.png")
	part.Write(raw)
	writer.Close()
	response := request("POST", "/api/v1/media/assets", body.String(), writer.FormDataContentType(), true)
	var uploaded struct {
		Data media.Asset `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &uploaded) != nil || uploaded.Data.Management == nil || !uploaded.Data.Management.CanRename {
		t.Fatal(response.Code, response.Body.String())
	}
	id := uploaded.Data.ID
	for _, test := range []struct {
		path, body string
		auth       bool
		code       int
	}{
		{"/assets/" + id + "/rename", `{"name":"updated"}`, false, 401},
		{"/assets/" + id + "/rename", `{"name":"updated","userId":"forged"}`, true, 400},
		{"/assets/" + id + "/rename", `{"name":"a/b"}`, true, 400},
		{"/assets/" + id + "/rename", `{"name":"New name"}`, true, 200},
		{"/assets/missing/rename", `{"name":"updated"}`, true, 404},
		{"/assets/" + id + "/deletion-requests", `{"teamId":"forged"}`, true, 400},
		{"/assets/" + id + "/deletion-requests", `null`, true, 400},
	} {
		w := request("POST", "/api/v1/media"+test.path, test.body, "application/json", test.auth)
		if w.Code != test.code {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
	response = request("POST", "/api/v1/media/assets/"+id+"/deletion-requests", "{}", "application/json", true)
	var deletion struct {
		Data contracts.MediaAssetDeletionRequest `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &deletion) != nil || deletion.Data.Status != "pending" {
		t.Fatal(response.Code, response.Body.String())
	}
	response = request("GET", "/api/v1/media/overview", "", "", true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"pendingDeletionId"`) || strings.Contains(response.Body.String(), `"uploaderId"`) || strings.Contains(response.Body.String(), `"requesterId"`) {
		t.Fatal(response.Code, response.Body.String())
	}
	for range 2 {
		response = request("POST", "/api/v1/media/asset-deletion-requests/"+deletion.Data.ID+"/review", `{"decision":"approve"}`, "application/json", true)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	response = request("GET", "/api/v1/media/overview", "", "", true)
	var overview struct {
		Data struct {
			Assets  []media.Asset          `json:"assets"`
			Library contracts.MediaLibrary `json:"mediaLibrary"`
		} `json:"data"`
	}
	if json.Unmarshal(response.Body.Bytes(), &overview) != nil || len(overview.Data.Assets) != 0 || len(overview.Data.Library.DeletionRequests) != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	signed := strings.TrimPrefix(uploaded.Data.URL, f.deps.Env.PublicAPIOrigin)
	if w := request("GET", signed, "", "", false); w.Code != 200 {
		t.Fatal("existing signed playback must survive catalog deletion", w.Code)
	}
}
