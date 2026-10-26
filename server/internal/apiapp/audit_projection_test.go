package apiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestTeamAuditHidesRootUsageExportRoutingAndKeepsPublicModelName(t *testing.T) {
	f := newTenantFixture(t)
	installDisplayModels(t, f.apiFixture)
	ctx := context.Background()
	provider, err := f.store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
		Name: "private-supplier-name", BaseURL: f.upstream.URL, Protocol: store.ProtocolOpenAIChatCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.InsertUsage(ctx, store.UsageRecord{
		ID: uuid.NewString(), RequestID: "audit-export-fixture", UserID: f.member.ID, TeamID: f.team.ID,
		Model: "deepseek-flash", ProviderID: provider.ID, UpstreamModel: "private-flash-route",
		Status: store.UsageStatusOk, CreditsMicro: 1, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	exportPath := fmt.Sprintf("/console/api/usage/export?teamId=%s&providerId=%s&model=deepseek-flash", f.team.ID, provider.ID)
	if rec := f.do("GET", exportPath, f.rootCookie, ""); rec.Code != 200 {
		t.Fatalf("root export: %d %s", rec.Code, rec.Body.String())
	}
	for _, cookie := range []string{f.rootCookie, f.adminCookie} {
		rec := f.do("GET", "/console/api/audit?teamId="+f.team.ID, cookie, "")
		if rec.Code != 200 {
			t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
		}
		found := false
		for _, raw := range dataOf(t, rec)["logs"].([]any) {
			row := raw.(map[string]any)
			if row["action"] != "usage.export" {
				continue
			}
			found = true
			detail := row["detail"].(map[string]any)
			if cookie == f.rootCookie {
				if detail["providerId"] != provider.ID || detail["model"] != "deepseek-flash" {
					t.Fatalf("root must keep original routing facts: %+v", detail)
				}
			} else if detail["model"] != "DeepSeek Flash" || detail["providerId"] != nil || detail["count"] != float64(1) {
				t.Fatalf("team admin must see public export facts: %+v", detail)
			}
		}
		if !found {
			t.Fatal("missing export audit")
		}
		if cookie == f.adminCookie && (strings.Contains(rec.Body.String(), provider.ID) || strings.Contains(rec.Body.String(), "deepseek-flash")) {
			t.Fatalf("root routing leaked through team audit: %s", rec.Body.String())
		}
	}
}

func TestTeamAuditUsesActionAndFieldWhitelist(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	secretID := uuid.NewString()
	for _, action := range []string{"member.update", "future.provider.operation", "usage.export"} {
		detail, _ := json.Marshal(map[string]any{
			"fields": []any{"role", "disabled", "private-supplier-name"}, "costMicro": 123,
			"providerId": secretID, "upstreamModel": "private-upstream", "model": "deleted-private-supplier-model",
			"nested": map[string]any{"providerName": "private-supplier-name"}, "count": 2,
		})
		if err := f.store.InsertAuditLog(ctx, store.InsertAuditLogInput{
			TeamID: f.team.ID, ActorUserID: f.root.ID, Action: action, TargetType: "provider", TargetID: secretID, Detail: detail,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.do("GET", "/console/api/audit", f.adminCookie, "")
	if rec.Code != 200 {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{secretID, "costMicro", "providerId", "upstreamModel", "private-upstream", "private-supplier-name", "deleted-private-supplier-model"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("audit leaked %s: %s", forbidden, rec.Body.String())
		}
	}
	for _, raw := range dataOf(t, rec)["logs"].([]any) {
		row := raw.(map[string]any)
		switch row["action"] {
		case "member.update":
			fields := row["detail"].(map[string]any)["fields"].([]any)
			if len(fields) != 2 || fields[0] != "role" || fields[1] != "disabled" {
				t.Fatalf("safe member fields lost: %+v", fields)
			}
		case "future.provider.operation":
			if row["detail"] != nil || row["targetType"] != nil || row["targetId"] != nil {
				t.Fatalf("unknown actions must fail closed: %+v", row)
			}
		case "usage.export":
			if row["detail"].(map[string]any)["model"] != "文字模型" {
				t.Fatalf("deleted model must use safe public fallback: %+v", row)
			}
		}
	}
	if rec := f.do("GET", "/console/api/audit", f.memberCookie, ""); rec.Code != 403 {
		t.Fatalf("member cannot read team audit: %d", rec.Code)
	}
	rootRec := f.do("GET", "/console/api/audit?teamId="+f.team.ID, f.rootCookie, "")
	if rootRec.Code != 200 || !strings.Contains(rootRec.Body.String(), secretID) || !strings.Contains(rootRec.Body.String(), "costMicro") {
		t.Fatalf("root audit must keep internal facts: %d %s", rootRec.Code, rootRec.Body.String())
	}
}
