package apiapp

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

func TestDirectPromptAdmissionErrorRemainsPublic(t *testing.T) {
	// 准入层返回了直出业务码，但 API 白名单遗漏会把它降为通用失败，客户端因此丢失可修改拒绝语义。
	err := media.ValidateBrief(media.Brief{PromptPolicy: "direct"}, nil, nil)
	if err == nil || err.Error() != "video_direct_prompt_ineligible" {
		t.Fatalf("unexpected admission error: %v", err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	deps := &Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mediaError(c, deps, err)
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &body); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if recorder.Code != http.StatusUnprocessableEntity || body.Code != http.StatusUnprocessableEntity || body.Msg != "video_direct_prompt_ineligible" {
		t.Fatalf("admission response: %d %s", recorder.Code, recorder.Body.String())
	}
}
