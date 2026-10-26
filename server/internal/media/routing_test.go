package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/routepool"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func TestMediaRouteSchedulerConcurrentInitialization(t *testing.T) {
	service := &Service{}
	results := make(chan *routepool.Scheduler, 32)
	var pending sync.WaitGroup
	for range cap(results) {
		pending.Add(1)
		go func() {
			defer pending.Done()
			results <- service.routeScheduler()
		}()
	}
	pending.Wait()
	close(results)
	for scheduler := range results {
		if scheduler == nil || scheduler != service.RouteScheduler {
			t.Fatal("concurrent initialization created different schedulers")
		}
	}
	other := &Service{}
	if other.routeScheduler() == service.RouteScheduler {
		t.Fatal("separate services must have separate route state")
	}
}

func routingProviders(t *testing.T, f *fixture) []ModelRoute {
	t.Helper()
	ctx := context.Background()
	routes := []ModelRoute{}
	for _, name := range []string{"first", "second"} {
		enabled := true
		provider, err := f.s.Repo.Store.CreateUpstreamProvider(ctx, store.CreateUpstreamProviderInput{
			Name: name, BaseURL: f.server.URL, Protocol: store.ProtocolOpenAIChatCompletions, Enabled: &enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		routes = append(routes, ModelRoute{ProviderID: provider.ID, Priority: 100, Weight: 1, Enabled: true})
	}
	return routes
}

func TestValidateMediaRoutePool(t *testing.T) {
	model := fixtureVideoModel()
	model.Routes = []ModelRoute{{ProviderID: "first", Priority: 0, Weight: 1, Enabled: true}, {ProviderID: "second", Priority: 100, Weight: 10_000}}
	if err := ValidateModels([]Model{model}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Model){
		"empty-provider":     func(m *Model) { m.Routes[0].ProviderID = "" },
		"spaced-provider":    func(m *Model) { m.Routes[0].ProviderID = " first " },
		"duplicate-provider": func(m *Model) { m.Routes[1].ProviderID = "first" },
		"negative-priority":  func(m *Model) { m.Routes[0].Priority = -1 },
		"excessive-priority": func(m *Model) { m.Routes[0].Priority = 1_000_001 },
		"zero-weight":        func(m *Model) { m.Routes[0].Weight = 0 },
		"excessive-weight":   func(m *Model) { m.Routes[0].Weight = 10_001 },
		"excessive-routes": func(m *Model) {
			m.Routes = nil
			for i := range 33 {
				m.Routes = append(m.Routes, ModelRoute{ProviderID: fmt.Sprint(i), Weight: 1})
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := model
			m.Routes = append([]ModelRoute(nil), model.Routes...)
			mutate(&m)
			if err := ValidateModels([]Model{m}); err == nil {
				t.Fatal("invalid route pool accepted")
			}
		})
	}
}

func TestMediaImageRoutesRotateOutputsAndSettleSelectedProvider(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	request := canvasRequest(t, f)
	routes := routingProviders(t, f)
	models, _ := f.s.Repo.Models(ctx)
	models[len(models)-1].Routes = routes
	raw, _ := json.Marshal(models)
	if err := f.s.Repo.Store.SetConfigValue(ctx, ConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), request)
	if err != nil {
		t.Fatal(err)
	}
	originalProvider := f.s.Provider
	selected := []string{}
	f.s.Provider = func(ctx context.Context, model Model) (Provider, error) {
		stored, err := f.s.Repo.Get(ctx, f.team, p.ID)
		if err != nil {
			return Provider{}, err
		}
		found := false
		for _, shot := range stored.Shots {
			if shot.Status == "pending" && shot.ProviderID == model.ProviderID && shot.Stage == "image" {
				found = true
			}
		}
		if !found {
			t.Fatal("provider called before the selected route was persisted")
		}
		selected = append(selected, model.ProviderID)
		return originalProvider(ctx, model)
	}
	for range 16 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "completed" || p.ChargedMicro != request.ExpectedQuoteMicro || len(selected) != 2 || selected[0] == selected[1] {
		t.Fatalf("image route rotation: project=%+v selected=%v", p, selected)
	}
	usage, err := f.s.Repo.Store.ListUsage(ctx, store.UsageFilter{TeamID: f.team})
	if err != nil || len(usage) != 2 {
		t.Fatalf("usage count=%d err=%v", len(usage), err)
	}
	providers := map[string]int{}
	for _, record := range usage {
		providers[record.ProviderID]++
		if record.Model != "media:i" || record.UpstreamModel != "image" || record.CreditsMicro != 100_000 {
			t.Fatalf("usage lost model or quote identity: %+v", record)
		}
	}
	for _, route := range routes {
		if providers[route.ProviderID] != 1 {
			t.Fatalf("settlement attributed to the wrong provider: %v", providers)
		}
	}
	view, _ := json.Marshal(p.View())
	for _, route := range routes {
		if strings.Contains(string(view), route.ProviderID) {
			t.Fatal("public projection leaked route identity")
		}
	}
}

func TestMediaVideoRouteBindingSurvivesRestartAndProviderDisable(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	routes := routingProviders(t, f)
	p := f.project(t)
	p, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Models[0].Routes = routes
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	baseProvider := f.s.Provider
	selected := []string{}
	providerFactory := func(ctx context.Context, model Model) (Provider, error) {
		selected = append(selected, model.ProviderID)
		provider, err := f.s.Repo.Store.GetUpstreamProvider(ctx, model.ProviderID)
		if err != nil || provider == nil || !provider.Enabled {
			return Provider{}, fmt.Errorf("media_provider_unavailable")
		}
		return baseProvider(ctx, model)
	}
	f.s.Provider = providerFactory
	f.s.Tick(ctx)
	p = current(t, f, p)
	bound := p.Shots[0].ProviderID
	if p.Shots[0].Status != "video_running" || bound == "" || f.submits.Load() != 1 {
		t.Fatalf("video submission binding: %+v", p.Shots[0])
	}
	if _, err := f.s.Repo.Store.UpdateUpstreamProvider(ctx, bound, store.UpdateUpstreamProviderPatch{Enabled: boolAddress(false)}); err != nil {
		t.Fatal(err)
	}
	// 新进程没有调度状态，已受理任务仍必须使用持久化的渠道；停用不能触发另一渠道重复生成。
	f.s = &Service{Repo: f.s.Repo, Blobs: f.s.Blobs, WorkerID: uuid.NewString(), Provider: providerFactory}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Shots[0].ProviderID != bound || p.Shots[0].Status != "video_running" || f.queries.Load() != 0 || f.submits.Load() != 1 {
		t.Fatalf("disabled provider changed accepted task: %+v", p.Shots[0])
	}
	if _, err := f.s.Repo.Store.UpdateUpstreamProvider(ctx, bound, store.UpdateUpstreamProviderPatch{Enabled: boolAddress(true)}); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		f.s.Tick(ctx)
		p = current(t, f, p)
		if p.Shots[0].Status == "succeeded" {
			break
		}
	}
	p = current(t, f, p)
	if p.Shots[0].Status != "succeeded" || f.submits.Load() != 1 || len(selected) < 3 {
		t.Fatalf("accepted task recovery: %+v selected=%v", p.Shots[0], selected)
	}
	for _, providerID := range selected {
		if providerID != bound {
			t.Fatalf("accepted task changed route: %v", selected)
		}
	}
	usage, err := f.s.Repo.Store.ListUsage(ctx, store.UsageFilter{TeamID: f.team})
	if err != nil || len(usage) != 1 || usage[0].ProviderID != bound {
		t.Fatalf("recovered usage lost provider binding: %+v err=%v", usage, err)
	}
}

func boolAddress(value bool) *bool { return &value }

func TestMediaRoutePoolUnavailableNeverFallsBackToSingleProvider(t *testing.T) {
	for _, reason := range []string{"route-disabled", "provider-disabled", "provider-missing"} {
		t.Run(reason, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			routes := routingProviders(t, f)[:1]
			switch reason {
			case "route-disabled":
				routes[0].Enabled = false
			case "provider-disabled":
				if _, err := f.s.Repo.Store.UpdateUpstreamProvider(ctx, routes[0].ProviderID, store.UpdateUpstreamProviderPatch{Enabled: boolAddress(false)}); err != nil {
					t.Fatal(err)
				}
			case "provider-missing":
				routes[0].ProviderID = uuid.NewString()
			}
			p := f.project(t)
			if _, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Models[0].Routes = routes; return nil }); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			if f.submits.Load() != 0 || p.Shots[0].ProviderID != "" || p.Shots[0].ReservationID != "" {
				t.Fatalf("unavailable pool used fallback: %+v submits=%d", p.Shots[0], f.submits.Load())
			}
		})
	}
}

func TestMediaUnknownSubmissionKeepsSelectedRouteWithoutFailover(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.unknown = true
	routes := routingProviders(t, f)
	p := f.project(t)
	if _, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Models[0].Routes = routes; return nil }); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "reconciliation_required" || p.Shots[0].ProviderID == "" || p.Shots[0].ReservationID == "" || f.submits.Load() != 1 {
		t.Fatalf("unknown submission must stay bound for reconciliation: %+v submits=%d", p.Shots[0], f.submits.Load())
	}
}

func TestMediaImageAndVideoStagesBindTheirOwnRoutes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	routes := routingProviders(t, f)
	p := f.project(t)
	if _, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Models[0].Routes = routes[:1]
		v.Models = append(v.Models, Model{ID: "i", Kind: "image", Adapter: "ark", ProviderID: "legacy-image", UpstreamModel: "image", PriceMicro: 200_000, PriceVersion: "v1", Routes: routes[1:]})
		v.Brief.ImageModel = "i"
		v.Shots[0].Plan.AssetID = ""
		v.Shots[0].Plan.ImagePrompt = "generate first frame"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	baseProvider := f.s.Provider
	f.s.Provider = func(ctx context.Context, model Model) (Provider, error) {
		want := routes[0].ProviderID
		if model.Kind == "image" {
			want = routes[1].ProviderID
		}
		if model.ProviderID != want {
			t.Fatalf("stage %s reused another stage's route: got %s want %s", model.Kind, model.ProviderID, want)
		}
		return baseProvider(ctx, model)
	}
	for range 7 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Shots[0].Status != "succeeded" || p.Shots[0].ChargedMicro != 700_000 || f.submits.Load() != 2 {
		t.Fatalf("image/video stages did not finish independently: %+v submits=%d", p.Shots[0], f.submits.Load())
	}
	usage, err := f.s.Repo.Store.ListUsage(ctx, store.UsageFilter{TeamID: f.team})
	if err != nil || len(usage) != 2 {
		t.Fatalf("stage usage count=%d err=%v", len(usage), err)
	}
	for _, record := range usage {
		want := routes[0].ProviderID
		if record.Model == "media:i" {
			want = routes[1].ProviderID
		}
		if record.ProviderID != want {
			t.Fatalf("stage settlement used another route: %+v", record)
		}
	}
}

func TestMediaLegacyAcceptedTaskKeepsOriginalProviderWithoutRouteBinding(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.video, _ = os.ReadFile("testdata/blue.mp4")
	p := f.project(t)
	originalID := p.Models[0].ProviderID
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Shots[0].Status != "video_running" || p.Shots[0].ProviderID != "" {
		t.Fatalf("fixture must represent an accepted legacy task: %+v", p.Shots[0])
	}
	routes := routingProviders(t, f)
	if _, err := f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error { v.Models[0].Routes = routes; return nil }); err != nil {
		t.Fatal(err)
	}
	baseProvider := f.s.Provider
	f.s.Provider = func(ctx context.Context, model Model) (Provider, error) {
		if model.ProviderID != originalID {
			t.Fatalf("legacy accepted task was rescheduled: got %s want %s", model.ProviderID, originalID)
		}
		return baseProvider(ctx, model)
	}
	f.s.Tick(ctx)
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Shots[0].Status != "succeeded" || f.submits.Load() != 1 {
		t.Fatalf("legacy task recovery: %+v submits=%d", p.Shots[0], f.submits.Load())
	}
	usage, err := f.s.Repo.Store.ListUsage(ctx, store.UsageFilter{TeamID: f.team})
	if err != nil || len(usage) != 1 || usage[0].ProviderID != originalID {
		t.Fatalf("legacy usage attribution changed: %+v err=%v", usage, err)
	}
}
