package apiapp

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFormulaTestPlanErrorRemainsPublic(t *testing.T) {
	// 新准入码若未列入白名单会被抹为通用失败，客户端因此无法定位需要修改的测试方案。
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	mediaError(c, &Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, fmt.Errorf("video_formula_test_plan_invalid"))
	var body struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 422 || body.Msg != "video_formula_test_plan_invalid" {
		t.Fatalf("formula rejection: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestReplicaReadinessErrorsRemainPublic(t *testing.T) {
	for _, code := range []string{"video_replica_purpose_invalid", "video_replica_adaptation_required", "video_replica_adaptation_invalid", "video_replica_adaptation_conflict", "video_replica_adaptation_blocked", "video_replica_adaptation_revision_required"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		mediaError(c, &Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, fmt.Errorf("%s", code))
		var body struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != 422 || body.Msg != code {
			t.Fatalf("replica rejection lost actionable code: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}
