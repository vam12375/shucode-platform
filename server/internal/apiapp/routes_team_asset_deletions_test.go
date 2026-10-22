package apiapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
)

func decodeDeletion(t *testing.T, response *httptest.ResponseRecorder) contracts.CloudAssetDeletionRequest {
	t.Helper()
	var envelope struct {
		Code int                                 `json:"code"`
		Data contracts.CloudAssetDeletionRequest `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Code != 0 {
		t.Fatalf("deletion response: %d %s", response.Code, response.Body.String())
	}
	return envelope.Data
}

func TestTeamAssetDeletionApprovalAndAuthorization(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	admin := newTeamAssetTestActor(t, f, "管理员")
	author := newTeamAssetTestActor(t, f, "申请者")
	other := newTeamAssetTestActor(t, f, "其他成员")
	team := newTeamAssetTestTeam(t, f, admin, author, other)
	outsider := newTeamAssetTestActor(t, f, "他团队")
	newTeamAssetTestTeam(t, f, outsider)
	id := uuid.NewString()
	teamAssetTestDecodeAsset(t, teamAssetTestUpload(t, f, author, id, "E2E_DELETE.md", []byte("keep until approved")))
	requestPath := "/api/v1/team-assets/" + id + "/deletion-requests"
	for _, actor := range []teamAssetTestActor{admin, other} {
		r := teamAssetTestRequest(f, actor, http.MethodPost, requestPath, "", nil)
		if r.Code != 403 {
			t.Fatalf("not real uploader: %d %s", r.Code, r.Body.String())
		}
	}
	if r := teamAssetTestRequest(f, outsider, http.MethodPost, requestPath, "", nil); r.Code != 404 {
		t.Fatalf("cross team request %d", r.Code)
	}
	if r := teamAssetTestRequest(f, teamAssetTestActor{}, http.MethodPost, requestPath, "", nil); r.Code != 401 {
		t.Fatalf("unauthorized request %d", r.Code)
	}
	if r := teamAssetTestRequest(f, author, http.MethodPost, requestPath, "application/json", strings.NewReader(`{"userId":"forged"}`)); r.Code != 400 {
		t.Fatalf("forged requester %d", r.Code)
	}
	request := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, requestPath, "application/json", strings.NewReader(`{}`)))
	if request.Status != "pending" || request.AssetID != id || request.RequestedBy.ID != author.userID || request.ReviewedAt != nil {
		t.Fatalf("pending wire: %+v", request)
	}
	if retry := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, requestPath, "", nil)); retry.ID != request.ID {
		t.Fatal("duplicate request")
	}
	for _, actor := range []teamAssetTestActor{author, other, admin} {
		r := teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets/deletion-requests", "", nil)
		var envelope struct {
			Data contracts.CloudAssetDeletionOverview `json:"data"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &envelope) != nil {
			t.Fatalf("list %d %s", r.Code, r.Body.String())
		}
		expected := 1
		if actor.userID == other.userID {
			expected = 0
		}
		if len(envelope.Data.Requests) != expected || envelope.Data.Viewer.ID != actor.userID || envelope.Data.Viewer.CanReview != (actor.userID == admin.userID) {
			t.Fatalf("scoped list %+v", envelope.Data)
		}
		for _, secret := range []string{"blobId", "sha256", "size", "blobCleanedAt", root} {
			if strings.Contains(r.Body.String(), secret) {
				t.Fatalf("private deletion fact leaked: %s", secret)
			}
		}
	}
	reviewPath := "/api/v1/team-assets/deletion-requests/" + request.ID + "/review"
	for _, actor := range []teamAssetTestActor{author, other} {
		if r := teamAssetTestRequest(f, actor, http.MethodPost, reviewPath, "application/json", strings.NewReader(`{"decision":"approve"}`)); r.Code != 403 {
			t.Fatalf("member review %d", r.Code)
		}
	}
	if r := teamAssetTestRequest(f, outsider, http.MethodPost, reviewPath, "application/json", strings.NewReader(`{"decision":"approve"}`)); r.Code != 404 {
		t.Fatalf("cross team review %d", r.Code)
	}
	for _, body := range []string{`{"decision":"approve","teamId":"forged"}`, `{"decision":"delete"}`, `{"decision":"approve"} {}`} {
		if r := teamAssetTestRequest(f, admin, http.MethodPost, reviewPath, "application/json", strings.NewReader(body)); r.Code != 400 {
			t.Fatalf("invalid review %s: %d", body, r.Code)
		}
	}
	rejected := decodeDeletion(t, teamAssetTestRequest(f, admin, http.MethodPost, reviewPath, "application/json", strings.NewReader(`{"decision":"reject"}`)))
	if rejected.Status != "rejected" {
		t.Fatal("not rejected")
	}
	if r := teamAssetTestRequest(f, author, http.MethodGet, "/api/v1/team-assets/"+id+"/content", "", nil); r.Code != 200 {
		t.Fatal("rejection lost content")
	}
	request = decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, requestPath, "", nil))
	reviewPath = "/api/v1/team-assets/deletion-requests/" + request.ID + "/review"
	approved := decodeDeletion(t, teamAssetTestRequest(f, admin, http.MethodPost, reviewPath, "application/json", strings.NewReader(`{"decision":"approve"}`)))
	if approved.Status != "approved" || approved.ReviewedBy == nil || approved.ReviewedBy.ID != admin.userID || approved.ReviewedAt == nil {
		t.Fatalf("approval wire: %+v", approved)
	}
	decodeDeletion(t, teamAssetTestRequest(f, admin, http.MethodPost, reviewPath, "application/json", strings.NewReader(`{"decision":"approve"}`)))
	if r := teamAssetTestRequest(f, author, http.MethodGet, "/api/v1/team-assets/"+id+"/content", "", nil); r.Code != 404 {
		t.Fatalf("approved still readable %d", r.Code)
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, author, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 0 || page.Data.Team.Storage.UsedBytes != 0 {
		t.Fatalf("approval quota %+v", page.Data)
	}
	var files []string
	_ = filepath.WalkDir(filepath.Join(root, team), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if len(files) != 0 {
		t.Fatalf("approved bytes remain: %v", files)
	}
	if r := teamAssetTestUpload(t, f, author, id, "E2E_DELETE.md", []byte("keep until approved")); r.Code != 409 {
		t.Fatalf("resurrection %d", r.Code)
	}
}
