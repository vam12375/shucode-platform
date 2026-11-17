package apiapp

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/media"
)

type mediaFailureRow struct {
	ID                   string           `json:"id"`
	TeamID               string           `json:"teamId"`
	Kind                 string           `json:"kind"`
	Product              string           `json:"product"`
	Status               string           `json:"status"`
	Error                string           `json:"error"`
	UpdatedAt            time.Time        `json:"updatedAt"`
	QuoteMicro           int64            `json:"quoteMicro"`
	ChargedMicro         int64            `json:"chargedMicro"`
	DiagnosticsAvailable bool             `json:"diagnosticsAvailable"`
	Shots                []media.ShotView `json:"shots"`
}

func mediaFailureSummary(p media.Project) mediaFailureRow {
	kind := "video"
	if p.Canvas != nil {
		kind = p.Canvas.Kind
	} else if p.Agent != nil {
		kind = p.Agent.Kind
	}
	if n := len(p.ProviderDiagnostics); n > 0 {
		if last := p.ProviderDiagnostics[n-1]; last.Kind == "image" || last.Kind == "video" {
			kind = last.Kind
		}
	}
	return mediaFailureRow{ID: p.ID, TeamID: p.TeamID, Kind: kind, Product: p.Brief.Product, Status: p.Status, Error: p.Error, UpdatedAt: p.UpdatedAt, QuoteMicro: p.QuoteMicro, ChargedMicro: p.ChargedMicro, DiagnosticsAvailable: len(p.ProviderDiagnostics) > 0, Shots: p.View().Shots}
}

func registerConsoleMediaFailureRoutes(router *gin.Engine, deps *Deps) {
	group := router.Group("/console/api/ops/media-failures")
	group.GET("", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		c.Header("Cache-Control", "no-store")
		number := func(name string, fallback, min, max int) (int, bool) {
			raw, exists := c.GetQuery(name)
			if !exists {
				return fallback, true
			}
			n, err := strconv.Atoi(raw)
			return n, err == nil && n >= min && n <= max
		}
		hours, okHours := number("hours", 24, 1, 720)
		limit, okLimit := number("limit", 50, 1, 100)
		offset, okOffset := number("offset", 0, 0, 100000)
		kind, category, status := c.Query("kind"), c.Query("category"), c.Query("status")
		if !okHours || !okLimit || !okOffset || kind != "" && kind != "image" && kind != "video" || !slices.Contains([]string{"", "content_policy", "invalid_input", "authentication", "quota", "rate_limit", "service_unavailable", "unclassified"}, category) || !slices.Contains([]string{"", "failed", "analysis_failed", "reconciliation_required", "cancelled", "queued", "running", "composing", "completed"}, status) || len(c.Query("q")) > 200 {
			c.JSON(400, fail(400, "筛选参数不合法"))
			return
		}
		projects, err := deps.Media.Repo.All(c.Request.Context())
		if err != nil {
			internalError(c, deps, err)
			return
		}
		since := time.Now().Add(-time.Duration(hours) * time.Hour)
		q := strings.ToLower(strings.TrimSpace(c.Query("q")))
		rows := []mediaFailureRow{}
		for _, p := range projects {
			if p.UpdatedAt.Before(since) || c.Query("teamId") != "" && p.TeamID != c.Query("teamId") {
				continue
			}
			if len(p.ProviderDiagnostics) == 0 && !slices.Contains([]string{"failed", "analysis_failed", "reconciliation_required", "cancelled"}, p.Status) {
				continue
			}
			row := mediaFailureSummary(p)
			if kind != "" && row.Kind != kind || status != "" && row.Status != status {
				continue
			}
			categories := []string{}
			search := p.ID + " " + p.Brief.Product + " " + p.Error
			for _, shot := range row.Shots {
				search += " " + shot.Error + " " + shot.VideoTaskID
				if shot.ProviderError != nil {
					search += " " + shot.ProviderError.Code
					if shot.ProviderError.Category != "" {
						categories = append(categories, shot.ProviderError.Category)
					}
				}
			}
			for _, d := range p.ProviderDiagnostics {
				search += " " + d.ModelID + " " + d.UpstreamModel + " " + d.ProviderID + " " + d.TaskID + " " + d.Code
				if public := media.PublicProviderError(d.Code); public != nil && public.Category != "" {
					categories = append(categories, public.Category)
				}
			}
			if category == "unclassified" && len(categories) > 0 || category != "" && category != "unclassified" && !slices.Contains(categories, category) || q != "" && !strings.Contains(strings.ToLower(search), q) {
				continue
			}
			rows = append(rows, row)
		}
		slices.SortFunc(rows, func(a, b mediaFailureRow) int {
			if result := b.UpdatedAt.Compare(a.UpdatedAt); result != 0 {
				return result
			}
			return strings.Compare(a.ID, b.ID)
		})
		total := len(rows)
		start := min(offset, total)
		end := min(start+limit, total)
		c.JSON(http.StatusOK, okData(gin.H{"records": rows[start:end], "total": total, "limit": limit, "offset": offset}))
	})
	group.GET("/:team/:id", func(c *gin.Context) {
		if _, ok := requireRoot(c.Request.Context(), deps, c); !ok {
			return
		}
		c.Header("Cache-Control", "no-store")
		p, err := deps.Media.Repo.Get(c.Request.Context(), c.Param("team"), c.Param("id"))
		if err != nil {
			internalError(c, deps, err)
			return
		}
		if p == nil {
			c.JSON(404, fail(404, "制作单不存在"))
			return
		}
		diagnostics := p.ProviderDiagnostics
		if diagnostics == nil {
			diagnostics = []media.ProviderDiagnostic{}
		}
		c.JSON(200, okData(gin.H{"project": mediaFailureSummary(*p), "diagnostics": diagnostics}))
	})
}
