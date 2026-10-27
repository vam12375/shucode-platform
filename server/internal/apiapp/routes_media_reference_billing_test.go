package apiapp

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReferenceBillingErrorStatus(t *testing.T) {
	for _, test := range []struct {
		code   string
		status int
	}{{"insufficient_credits", 402}, {"budget_exhausted", 402}, {"team_inactive", 403}, {"source_import_settlement_pending", 409}, {"reference_pricing_unavailable", 503}, {"source_media_dependencies_missing", 503}, {"source_stream_duration_mismatch", 422}, {"source_stream_merge_failed", 502}, {"source_import_save_failed", 500}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		referenceError(c, nil, errors.New(test.code))
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.code) {
			t.Fatalf("%s: status=%d body=%s", test.code, w.Code, w.Body.String())
		}
	}
}
