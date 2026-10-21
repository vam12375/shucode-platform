package apiapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/crypto"
	"github.com/vam12375/shucode-platform/server/internal/store"
	"github.com/vam12375/shucode-platform/server/internal/teamassets"
)

type teamAssetTestActor struct {
	userID, name, token, deviceID string
}

type teamAssetTestEnvelope struct {
	Code int              `json:"code"`
	Msg  string           `json:"msg"`
	Data teamassets.Asset `json:"data"`
}

type teamAssetTestPage struct {
	Code int `json:"code"`
	Data struct {
		Team struct {
			ID, Name string
			Storage  teamassets.Storage `json:"storage"`
		} `json:"team"`
		Assets     []teamassets.Asset `json:"assets"`
		NextCursor *string            `json:"nextCursor"`
	} `json:"data"`
}

func newTeamAssetTestFixture(t *testing.T) (*apiFixture, string) {
	t.Helper()
	root := t.TempDir()
	// 路由注册时捕获独立 Service，必须在 New 前设置根目录，避免测试落入仓库 data/media。
	t.Setenv("MEDIA_STORAGE_DIR", root)
	return newAPIEmptyFixture(t), filepath.Join(root, "team-assets")
}

func newTeamAssetTestActor(t *testing.T, f *apiFixture, name string) teamAssetTestActor {
	t.Helper()
	user, err := f.store.CreateUser(context.Background(), store.UserInput{
		Email: uuid.NewString() + "@test.local", DisplayName: name, PasswordHash: "x", Role: store.RoleUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := crypto.GenerateDeviceToken()
	ciphertext, err := f.deps.SecretBox.Encrypt(token)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := uuid.NewString()
	if err := f.store.CreateDeviceToken(context.Background(), store.DeviceTokenRecord{
		ID: deviceID, UserID: user.ID, Name: "团队资产测试设备", TokenHash: crypto.HashToken(token),
		TokenCiphertext: ciphertext, TokenHint: crypto.DeviceTokenHint(token), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return teamAssetTestActor{userID: user.ID, name: name, token: token, deviceID: deviceID}
}

func newTeamAssetTestTeam(t *testing.T, f *apiFixture, actors ...teamAssetTestActor) string {
	t.Helper()
	team, err := f.store.CreateTeam(context.Background(), store.CreateTeamInput{Name: "团队资产测试 " + uuid.NewString(), QuotaCredits: "1"})
	if err != nil {
		t.Fatal(err)
	}
	for i, actor := range actors {
		role := store.MemberRoleMember
		if i == 0 {
			role = store.MemberRoleOwner
		}
		if err := f.store.AddMember(context.Background(), team.ID, actor.userID, role); err != nil {
			t.Fatal(err)
		}
	}
	return team.ID
}

func teamAssetTestRequest(f *apiFixture, actor teamAssetTestActor, method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if actor.token != "" {
		req.Header.Set("Authorization", "Bearer "+actor.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	f.engine.ServeHTTP(w, req)
	return w
}

func teamAssetTestMultipart(t *testing.T, id, name string, data []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("id", id); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), w.FormDataContentType()
}

func teamAssetTestUpload(t *testing.T, f *apiFixture, actor teamAssetTestActor, id, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := teamAssetTestMultipart(t, id, name, data)
	return teamAssetTestRequest(f, actor, http.MethodPost, "/api/v1/team-assets", contentType, bytes.NewReader(body))
}

func teamAssetTestDecodeAsset(t *testing.T, response *httptest.ResponseRecorder) teamassets.Asset {
	t.Helper()
	var envelope teamAssetTestEnvelope
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Code != 0 {
		t.Fatalf("asset response: %d %s", response.Code, response.Body.String())
	}
	return envelope.Data
}

func teamAssetTestDecodePage(t *testing.T, response *httptest.ResponseRecorder) teamAssetTestPage {
	t.Helper()
	var page teamAssetTestPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Code != 0 {
		t.Fatalf("list response: %d %s", response.Code, response.Body.String())
	}
	return page
}

func TestTeamAssetGenericFilesTeamIsolationAndDownloadHeaders(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	owner := newTeamAssetTestActor(t, f, "上传者")
	member := newTeamAssetTestActor(t, f, "普通成员")
	teamID := newTeamAssetTestTeam(t, f, owner, member)
	outsider := newTeamAssetTestActor(t, f, "其他团队")
	newTeamAssetTestTeam(t, f, outsider)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX1kAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		name, kind string
		data       []byte
	}{
		{"分析.md", "document", []byte("# 市场分析\n团队共享知识\n")},
		{"生成图片.png", "image", png},
		{"生成视频.mp4", "video", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom\x00\x00\x00\x08mdat")},
		{"网页.html", "website", []byte("<!doctype html><html><script>alert('fixture')</script></html>")},
		{"脚本.ts", "code", []byte("export const caption = '团队资产'\n")},
		{"原始文件.bin", "other", []byte{0, 1, 2, 3, 4, 5}},
	}
	var used int64
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			id := uuid.NewString()
			upload := teamAssetTestUpload(t, f, owner, id, fixture.name, fixture.data)
			if upload.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("upload metadata must not be cached: %q", upload.Header().Get("Cache-Control"))
			}
			asset := teamAssetTestDecodeAsset(t, upload)
			if asset.ID != id || asset.Name != fixture.name || asset.Kind != fixture.kind || asset.Size != int64(len(fixture.data)) || asset.UploadedBy.ID != owner.userID || asset.UploadedBy.Name != owner.name || asset.Mime == "" || asset.CreatedAt.IsZero() {
				t.Fatalf("unexpected asset: %+v", asset)
			}
			for _, secret := range []string{"blobId", "sha256", "url", "teamId", root, owner.token} {
				if strings.Contains(upload.Body.String(), secret) {
					t.Fatalf("asset projection exposes %q: %s", secret, upload.Body.String())
				}
			}
			metadataResponse := teamAssetTestRequest(f, member, http.MethodGet, "/api/v1/team-assets/"+id, "", nil)
			if metadataResponse.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("asset metadata must not be cached: %q", metadataResponse.Header().Get("Cache-Control"))
			}
			metadata := teamAssetTestDecodeAsset(t, metadataResponse)
			if metadata.ID != asset.ID || metadata.UploadedBy.ID != owner.userID {
				t.Fatalf("teammate metadata: %+v", metadata)
			}
			content := teamAssetTestRequest(f, member, http.MethodGet, "/api/v1/team-assets/"+id+"/content", "", nil)
			if content.Code != http.StatusOK || !bytes.Equal(content.Body.Bytes(), fixture.data) {
				t.Fatalf("download: %d %s", content.Code, content.Body.String())
			}
			disposition, params, err := mime.ParseMediaType(content.Header().Get("Content-Disposition"))
			if err != nil || disposition != "attachment" || params["filename"] != fixture.name {
				t.Fatalf("unsafe disposition: %q %v", content.Header().Get("Content-Disposition"), err)
			}
			if content.Header().Get("X-Content-Type-Options") != "nosniff" || content.Header().Get("Cache-Control") != "private, no-store" || content.Header().Get("Location") != "" {
				t.Fatalf("unsafe download headers: %v", content.Header())
			}
			for _, suffix := range []string{"", "/content"} {
				for _, missingID := range []string{id, uuid.NewString()} {
					if response := teamAssetTestRequest(f, outsider, http.MethodGet, "/api/v1/team-assets/"+missingID+suffix, "", nil); response.Code != http.StatusNotFound {
						t.Fatalf("cross-team or missing asset: %d %s", response.Code, response.Body.String())
					}
				}
				if response := teamAssetTestRequest(f, member, http.MethodGet, "/api/v1/team-assets/"+uuid.NewString()+suffix, "", nil); response.Code != http.StatusNotFound {
					t.Fatalf("missing asset: %d %s", response.Code, response.Body.String())
				}
			}
			used += int64(len(fixture.data))
		})
	}
	pageResponse := teamAssetTestRequest(f, member, http.MethodGet, "/api/v1/team-assets?teamId="+url.QueryEscape(uuid.NewString()), "", nil)
	if pageResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("team asset listing must not be cached: %q", pageResponse.Header().Get("Cache-Control"))
	}
	page := teamAssetTestDecodePage(t, pageResponse)
	if page.Data.Team.ID != teamID || len(page.Data.Assets) != len(fixtures) || page.Data.Team.Storage.UsedBytes != used || page.Data.Team.Storage.LimitBytes != teamassets.TeamStorageLimitBytes || page.Data.NextCursor != nil {
		t.Fatalf("shared team listing: %+v", page.Data)
	}
	otherPage := teamAssetTestDecodePage(t, teamAssetTestRequest(f, outsider, http.MethodGet, "/api/v1/team-assets?teamId="+url.QueryEscape(teamID), "", nil))
	if len(otherPage.Data.Assets) != 0 || otherPage.Data.Team.Storage.UsedBytes != 0 || otherPage.Data.Team.ID == teamID {
		t.Fatalf("client teamId must not select a team: %+v", otherPage.Data)
	}
	entries, err := os.ReadDir(filepath.Join(root, teamID))
	if err != nil || len(entries) != len(fixtures) {
		t.Fatalf("isolated asset blob root: %d %v", len(entries), err)
	}
}

type teamAssetTestMembershipStore struct {
	store.PlatformStore
	ambiguous bool
	err       error
}

func (s teamAssetTestMembershipStore) ListTeamsForUser(ctx context.Context, userID string) ([]store.TeamMembership, error) {
	if s.err != nil {
		return nil, s.err
	}
	memberships, err := s.PlatformStore.ListTeamsForUser(ctx, userID)
	if s.ambiguous && err == nil && len(memberships) > 0 {
		memberships = append(memberships, memberships[0])
	}
	return memberships, err
}

func TestTeamAssetAuthenticationAndFreshMembership(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	owner := newTeamAssetTestActor(t, f, "所有者")
	member := newTeamAssetTestActor(t, f, "成员")
	removedMember := newTeamAssetTestActor(t, f, "被移除成员")
	teamID := newTeamAssetTestTeam(t, f, owner, member, removedMember)
	noTeam := newTeamAssetTestActor(t, f, "无团队")
	id := uuid.NewString()
	teamAssetTestDecodeAsset(t, teamAssetTestUpload(t, f, owner, id, "分析.md", []byte("analysis")))
	paths := []string{"/api/v1/team-assets", "/api/v1/team-assets/" + id, "/api/v1/team-assets/" + id + "/content"}
	for _, path := range paths {
		for _, test := range []struct {
			actor teamAssetTestActor
			want  int
		}{{teamAssetTestActor{}, http.StatusUnauthorized}, {noTeam, http.StatusForbidden}} {
			if response := teamAssetTestRequest(f, test.actor, http.MethodGet, path, "", nil); response.Code != test.want {
				t.Fatalf("auth %s: %d %s", path, response.Code, response.Body.String())
			}
		}
	}
	if response := teamAssetTestUpload(t, f, noTeam, uuid.NewString(), "a.md", []byte("a")); response.Code != http.StatusForbidden {
		t.Fatalf("upload without team: %d", response.Code)
	}
	for _, test := range []struct {
		name  string
		store store.PlatformStore
		want  int
	}{
		{"multiple active teams", teamAssetTestMembershipStore{PlatformStore: f.store, ambiguous: true}, http.StatusForbidden},
		{"team store failure", teamAssetTestMembershipStore{PlatformStore: f.store, err: errors.New("membership storage unavailable")}, http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.deps.Store = test.store
			f.engine = New(f.deps)
			for _, path := range paths {
				if response := teamAssetTestRequest(f, member, http.MethodGet, path, "", nil); response.Code != test.want {
					t.Fatalf("team resolution %s: %d %s", path, response.Code, response.Body.String())
				}
			}
		})
	}
	f.deps.Store = f.store
	f.engine = New(f.deps)
	now := time.Now()
	if _, err := f.store.UpdateTeamMember(context.Background(), teamID, member.userID, store.TeamMemberPatch{HasDisabled: true, DisabledAt: &now}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if response := teamAssetTestRequest(f, member, http.MethodGet, path, "", nil); response.Code != http.StatusForbidden {
			t.Fatalf("disabled member %s: %d", path, response.Code)
		}
	}
	if _, err := f.store.RemoveMember(context.Background(), teamID, removedMember.userID); err != nil {
		t.Fatal(err)
	}
	if response := teamAssetTestRequest(f, removedMember, http.MethodGet, paths[2], "", nil); response.Code != http.StatusForbidden {
		t.Fatalf("removed member download: %d", response.Code)
	}
	if err := f.store.RevokeDeviceToken(context.Background(), owner.deviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if response := teamAssetTestRequest(f, owner, http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("revoked device %s: %d", path, response.Code)
		}
	}
}

func TestTeamAssetStrictMultipartAndInputValidation(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	actor := newTeamAssetTestActor(t, f, "上传者")
	newTeamAssetTestTeam(t, f, actor)
	for _, test := range []struct{ name, id, filename string }{
		{"invalid id", "not-a-uuid", "a.md"},
		{"windows path", uuid.NewString(), `C:\\tmp\\a.md`},
		{"blank filename", uuid.NewString(), " "},
	} {
		t.Run(test.name, func(t *testing.T) {
			if response := teamAssetTestUpload(t, f, actor, test.id, test.filename, []byte("a")); response.Code != http.StatusBadRequest {
				t.Fatalf("invalid input: %d %s", response.Code, response.Body.String())
			}
		})
	}
	for _, test := range []struct {
		name  string
		write func(*multipart.Writer) error
	}{
		{"duplicate id", func(w *multipart.Writer) error { return w.WriteField("id", uuid.NewString()) }},
		{"unknown field", func(w *multipart.Writer) error { return w.WriteField("teamId", uuid.NewString()) }},
		{"duplicate file", func(w *multipart.Writer) error {
			p, err := w.CreateFormFile("file", "second.md")
			if err == nil {
				_, err = p.Write([]byte("second"))
			}
			return err
		}},
		{"unknown file", func(w *multipart.Writer) error {
			p, err := w.CreateFormFile("extra", "second.md")
			if err == nil {
				_, err = p.Write([]byte("second"))
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			if err := writer.WriteField("id", uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			part, err := writer.CreateFormFile("file", "first.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("first")); err != nil {
				t.Fatal(err)
			}
			if err := test.write(writer); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			response := teamAssetTestRequest(f, actor, http.MethodPost, "/api/v1/team-assets", writer.FormDataContentType(), &body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("strict multipart: %d %s", response.Code, response.Body.String())
			}
		})
	}
	for _, body := range []struct{ contentType, body string }{
		{"application/json", `{"id":"` + uuid.NewString() + `"}`},
		{"multipart/form-data; boundary=missing", "broken"},
	} {
		if response := teamAssetTestRequest(f, actor, http.MethodPost, "/api/v1/team-assets", body.contentType, strings.NewReader(body.body)); response.Code != http.StatusBadRequest {
			t.Fatalf("malformed multipart: %d %s", response.Code, response.Body.String())
		}
	}
	if response := teamAssetTestUpload(t, f, actor, uuid.NewString(), "empty.md", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("empty file: %d %s", response.Code, response.Body.String())
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 0 || page.Data.Team.Storage.UsedBytes != 0 {
		t.Fatalf("rejected inputs created assets: %+v", page.Data)
	}
}

func TestTeamAssetIdempotencyAndConcurrentRetries(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	owner := newTeamAssetTestActor(t, f, "上传者")
	member := newTeamAssetTestActor(t, f, "同团队成员")
	teamID := newTeamAssetTestTeam(t, f, owner, member)
	id := uuid.NewString()
	data := []byte("# immutable analysis\n")
	body, contentType := teamAssetTestMultipart(t, id, "analysis.md", data)
	const concurrent = 12
	responses := make(chan *httptest.ResponseRecorder, concurrent)
	var workers sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			responses <- teamAssetTestRequest(f, owner, http.MethodPost, "/api/v1/team-assets", contentType, bytes.NewReader(body))
		}()
	}
	workers.Wait()
	close(responses)
	var first teamassets.Asset
	for response := range responses {
		asset := teamAssetTestDecodeAsset(t, response)
		if first.ID == "" {
			first = asset
		}
		if asset != first {
			t.Fatalf("retry changed original projection: %+v %+v", first, asset)
		}
	}
	for _, test := range []struct {
		actor teamAssetTestActor
		name  string
		data  []byte
	}{{owner, "renamed.md", data}, {owner, "analysis.md", []byte("changed")}, {member, "analysis.md", data}} {
		if response := teamAssetTestUpload(t, f, test.actor, id, test.name, test.data); response.Code != http.StatusConflict {
			t.Fatalf("immutable conflict: %d %s", response.Code, response.Body.String())
		}
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, member, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 1 || page.Data.Team.Storage.UsedBytes != int64(len(data)) {
		t.Fatalf("retry counted more than once: %+v", page.Data)
	}
	entries, err := os.ReadDir(filepath.Join(root, teamID))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unreferenced retry blobs remain: %d %v", len(entries), err)
	}
}

type teamAssetTestFillReader struct{ remaining int64 }

func (r *teamAssetTestFillReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	clear(p[:n])
	r.remaining -= n
	return int(n), nil
}

func TestTeamAssetOversizedUploadIsRejectedWithoutMetadata(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	actor := newTeamAssetTestActor(t, f, "上传者")
	teamID := newTeamAssetTestTeam(t, f, actor)
	// 用流式 reader 产生超限请求，避免在测试内额外分配整个 100 MiB 文件。
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	if err := writer.WriteField("id", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="large.bin"`)
	header.Set("Content-Type", "application/octet-stream")
	if _, err := writer.CreatePart(header); err != nil {
		t.Fatal(err)
	}
	boundary := writer.Boundary()
	body := io.MultiReader(bytes.NewReader(prefix.Bytes()), &teamAssetTestFillReader{remaining: teamassets.MaxAssetBytes + 1}, strings.NewReader("\r\n--"+boundary+"--\r\n"))
	response := teamAssetTestRequest(f, actor, http.MethodPost, "/api/v1/team-assets", writer.FormDataContentType(), body)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "team_asset_upload_size_limit") {
		t.Fatalf("over-limit upload: %d %s", response.Code, response.Body.String())
	}
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets", "", nil))
	if len(page.Data.Assets) != 0 || page.Data.Team.Storage.UsedBytes != 0 {
		t.Fatalf("over-limit upload committed: %+v", page.Data)
	}
	entries, err := os.ReadDir(filepath.Join(root, teamID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("over-limit upload retained blobs: %d", len(entries))
	}
}

func teamAssetTestSeedRecord(t *testing.T, f *apiFixture, teamID string, record teamassets.Record) teamassets.Record {
	t.Helper()
	committed, err := (teamassets.Repository{Store: f.store}).Commit(context.Background(), teamID, record)
	if err != nil {
		t.Fatalf("seed team asset metadata: %v", err)
	}
	return committed
}

func newTeamAssetTestRecord(id, name string, uploader teamAssetTestActor, size int64, digest string, createdAt time.Time) teamassets.Record {
	return teamassets.Record{
		Asset: teamassets.Asset{
			ID: id, Name: name, Kind: "other", Mime: "application/octet-stream", Size: size,
			CreatedAt: createdAt, UploadedBy: teamassets.Uploader{ID: uploader.userID, Name: uploader.name},
		},
		SHA256: digest, BlobID: uuid.NewString(),
	}
}

func TestTeamAssetPaginationStableCursorAndTeamBinding(t *testing.T) {
	f, _ := newTeamAssetTestFixture(t)
	actor := newTeamAssetTestActor(t, f, "上传者")
	teamID := newTeamAssetTestTeam(t, f, actor)
	other := newTeamAssetTestActor(t, f, "其他团队")
	newTeamAssetTestTeam(t, f, other)
	createdAt := time.Date(2026, 10, 21, 8, 30, 0, 123456789, time.UTC)
	ids := make([]string, 205)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		record := newTeamAssetTestRecord(ids[i], fmt.Sprintf("asset-%03d.bin", i), actor, 1, fmt.Sprintf("%064x", i+1), createdAt)
		teamAssetTestSeedRecord(t, f, teamID, record)
	}
	path := "/api/v1/team-assets"
	first := teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, path, "", nil))
	if len(first.Data.Assets) != 100 || first.Data.NextCursor == nil || first.Data.Team.Storage.UsedBytes != 205 {
		t.Fatalf("first page: count=%d cursor=%v storage=%+v", len(first.Data.Assets), first.Data.NextCursor, first.Data.Team.Storage)
	}
	second := teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, path+"?cursor="+url.QueryEscape(*first.Data.NextCursor), "", nil))
	if len(second.Data.Assets) != 100 || second.Data.NextCursor == nil {
		t.Fatalf("second page: count=%d cursor=%v", len(second.Data.Assets), second.Data.NextCursor)
	}
	third := teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, path+"?cursor="+url.QueryEscape(*second.Data.NextCursor), "", nil))
	if len(third.Data.Assets) != 5 || third.Data.NextCursor != nil {
		t.Fatalf("third page: count=%d cursor=%v", len(third.Data.Assets), third.Data.NextCursor)
	}
	seen := make(map[string]struct{}, 205)
	for _, page := range []teamAssetTestPage{first, second, third} {
		for _, asset := range page.Data.Assets {
			if _, ok := seen[asset.ID]; ok {
				t.Fatalf("cursor repeated asset %s", asset.ID)
			}
			seen[asset.ID] = struct{}{}
		}
	}
	if len(seen) != 205 || first.Data.Assets[0].ID != ids[204] || third.Data.Assets[4].ID != ids[0] {
		t.Fatalf("descending createdAt/id ordering is unstable: first=%s last=%s", first.Data.Assets[0].ID, third.Data.Assets[4].ID)
	}
	for _, cursor := range []string{"not-a-cursor", "abc"} {
		response := teamAssetTestRequest(f, actor, http.MethodGet, path+"?cursor="+cursor, "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid cursor %q: %d %s", cursor, response.Code, response.Body.String())
		}
	}
	response := teamAssetTestRequest(f, other, http.MethodGet, path+"?cursor="+url.QueryEscape(*first.Data.NextCursor), "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cross-team cursor: %d %s", response.Code, response.Body.String())
	}
}

func TestTeamAssetStorageLimitAndIdempotencyAtExactCapacity(t *testing.T) {
	f, root := newTeamAssetTestFixture(t)
	actor := newTeamAssetTestActor(t, f, "上传者")
	teamID := newTeamAssetTestTeam(t, f, actor)
	createdAt := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	const hundredMiB int64 = 100 << 20
	for i := 0; i < 51; i++ {
		record := newTeamAssetTestRecord(uuid.NewString(), fmt.Sprintf("large-%02d.bin", i), actor, hundredMiB, fmt.Sprintf("%064x", i+1), createdAt)
		teamAssetTestSeedRecord(t, f, teamID, record)
	}
	sameBytes := []byte("x")
	sameDigest := sha256.Sum256(sameBytes)
	retryID := uuid.NewString()
	retryName := "same.bin"
	teamAssetTestSeedRecord(t, f, teamID, newTeamAssetTestRecord(uuid.NewString(), "remaining.bin", actor, (20<<20)-1, strings.Repeat("a", 64), createdAt))
	teamAssetTestSeedRecord(t, f, teamID, newTeamAssetTestRecord(retryID, retryName, actor, 1, hex.EncodeToString(sameDigest[:]), createdAt))
	page := teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets", "", nil))
	if page.Data.Team.Storage.UsedBytes != teamassets.TeamStorageLimitBytes || len(page.Data.Assets) != 53 {
		t.Fatalf("exact quota metadata: used=%d assets=%d", page.Data.Team.Storage.UsedBytes, len(page.Data.Assets))
	}
	if response := teamAssetTestUpload(t, f, actor, retryID, retryName, sameBytes); response.Code != http.StatusOK {
		t.Fatalf("same upload must succeed at full quota: %d %s", response.Code, response.Body.String())
	}
	if response := teamAssetTestUpload(t, f, actor, uuid.NewString(), "extra.bin", []byte("z")); response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "team_asset_storage_limit") {
		t.Fatalf("over-quota upload: %d %s", response.Code, response.Body.String())
	}
	page = teamAssetTestDecodePage(t, teamAssetTestRequest(f, actor, http.MethodGet, "/api/v1/team-assets", "", nil))
	if page.Data.Team.Storage.UsedBytes != teamassets.TeamStorageLimitBytes || len(page.Data.Assets) != 53 {
		t.Fatalf("over-quota upload changed committed metadata: used=%d assets=%d", page.Data.Team.Storage.UsedBytes, len(page.Data.Assets))
	}
	entries, err := os.ReadDir(filepath.Join(root, teamID))
	if err != nil || len(entries) != 0 {
		t.Fatalf("idempotency/quota cleanup retained blobs: entries=%d err=%v", len(entries), err)
	}
}
