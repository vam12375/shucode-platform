package apiapp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/contracts"
)

func TestTeamAssetDeletionWithdrawalAuthorizationAndContent(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	admin := newTeamAssetTestActor(t, f, "管理员")
	author := newTeamAssetTestActor(t, f, "申请者")
	other := newTeamAssetTestActor(t, f, "其他成员")
	newTeamAssetTestTeam(t, f, admin, author, other)
	outsider := newTeamAssetTestActor(t, f, "他团队")
	newTeamAssetTestTeam(t, f, outsider)
	id := uuid.NewString()
	content := []byte("withdrawal keeps original bytes")
	teamAssetTestDecodeAsset(t, teamAssetTestUpload(t, f, author, id, "E2E_WITHDRAW.md", content))
	requestPath := "/api/v1/team-assets/" + id + "/deletion-requests"
	request := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, requestPath, "application/json", strings.NewReader(`{}`)))
	withdrawPath := "/api/v1/team-assets/deletion-requests/" + request.ID + "/withdraw"
	for _, actor := range []teamAssetTestActor{admin, other} {
		if r := teamAssetTestRequest(f, actor, http.MethodPost, withdrawPath, "application/json", strings.NewReader(`{}`)); r.Code != 403 {
			t.Fatalf("other actor withdrawal: %d %s", r.Code, r.Body.String())
		}
	}
	if r := teamAssetTestRequest(f, outsider, http.MethodPost, withdrawPath, "application/json", strings.NewReader(`{}`)); r.Code != 404 {
		t.Fatalf("cross-team withdrawal: %d", r.Code)
	}
	if r := teamAssetTestRequest(f, teamAssetTestActor{}, http.MethodPost, withdrawPath, "application/json", strings.NewReader(`{}`)); r.Code != 401 {
		t.Fatalf("unauthenticated withdrawal: %d", r.Code)
	}
	for _, body := range []string{"", `null`, `[]`, `{"requestedBy":"forged"}`, `{} {}`, strings.Repeat(" ", 1024) + `{}`} {
		if r := teamAssetTestRequest(f, author, http.MethodPost, withdrawPath, "application/json", strings.NewReader(body)); r.Code != 400 {
			t.Fatalf("invalid withdrawal body: %q %d", body, r.Code)
		}
	}
	withdrawn := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, withdrawPath, "application/json", strings.NewReader(`{}`)))
	if withdrawn.ID != request.ID || withdrawn.Status != "withdrawn" || withdrawn.WithdrawnAt == nil || withdrawn.ReviewedBy != nil || withdrawn.ReviewedAt != nil {
		t.Fatalf("withdrawal wire: %+v", withdrawn)
	}
	retry := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, withdrawPath, "application/json", strings.NewReader(`{}`)))
	if retry.WithdrawnAt == nil || !retry.WithdrawnAt.Equal(*withdrawn.WithdrawnAt) {
		t.Fatalf("withdrawal retry changed time: %+v", retry)
	}
	if r := teamAssetTestRequest(f, author, http.MethodGet, "/api/v1/team-assets/"+id+"/content", "", nil); r.Code != 200 || r.Body.String() != string(content) {
		t.Fatalf("withdrawal lost bytes: %d %s", r.Code, r.Body.String())
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, author, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 1 || page.Data.Team.Storage.UsedBytes != int64(len(content)) {
		t.Fatalf("withdrawal changed quota/metadata: %+v", page.Data)
	}
	for _, actor := range []teamAssetTestActor{author, admin} {
		r := teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets/deletion-requests", "", nil)
		var envelope struct {
			Data contracts.CloudAssetDeletionOverview `json:"data"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &envelope) != nil || envelope.Data.PendingCount != 0 || len(envelope.Data.Requests) != 1 || envelope.Data.Requests[0].Status != "withdrawn" {
			t.Fatalf("withdrawal history/count: %d %s", r.Code, r.Body.String())
		}
	}
	reviewPath := "/api/v1/team-assets/deletion-requests/" + request.ID + "/review"
	for _, decision := range []string{"approve", "reject"} {
		if r := teamAssetTestRequest(f, admin, http.MethodPost, reviewPath, "application/json", strings.NewReader(`{"decision":"`+decision+`"}`)); r.Code != 409 {
			t.Fatalf("withdrawal reviewed: %d %s", r.Code, r.Body.String())
		}
	}
	reapplied := decodeDeletion(t, teamAssetTestRequest(f, author, http.MethodPost, requestPath, "application/json", strings.NewReader(`{}`)))
	if reapplied.ID == request.ID || reapplied.Status != "pending" {
		t.Fatalf("reapplication: %+v", reapplied)
	}
	decodeDeletion(t, teamAssetTestRequest(f, admin, http.MethodPost, "/api/v1/team-assets/deletion-requests/"+reapplied.ID+"/review", "application/json", strings.NewReader(`{"decision":"reject"}`)))
	if r := teamAssetTestRequest(f, author, http.MethodPost, "/api/v1/team-assets/deletion-requests/"+reapplied.ID+"/withdraw", "application/json", strings.NewReader(`{}`)); r.Code != 409 {
		t.Fatalf("rejected request withdrawn: %d", r.Code)
	}
}
