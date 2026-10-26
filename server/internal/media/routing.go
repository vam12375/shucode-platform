package media

import (
	"context"
	"fmt"
	"sync"

	"github.com/vam12375/shucode-platform/server/internal/routepool"
)

var mediaRouteSchedulerInit sync.Mutex

// Service 的恢复快照会复制结构体；只保存调度器指针，避免复制其内部互斥锁。
// 首次访问在独立锁内初始化，保证并发 Tick 不会创建多条轮转序列或产生竞态。
func (s *Service) routeScheduler() *routepool.Scheduler {
	mediaRouteSchedulerInit.Lock()
	defer mediaRouteSchedulerInit.Unlock()
	if s.RouteScheduler == nil {
		s.RouteScheduler = routepool.New()
	}
	return s.RouteScheduler
}

// 冻结目录只能说明候选渠道，不能说明某次付费任务的归属；一旦选中渠道，
// 先保存阶段绑定，再提交上游，恢复、轮询与结算始终使用同一供应商。
func (s *Service) routedModel(ctx context.Context, p *Project, index int, shot Shot, model Model, kind string) (Model, error) {
	if shot.ProviderID != "" {
		model.ProviderID = shot.ProviderID
		return model, nil
	}
	// 历史已受理任务没有渠道绑定，只能按原单供应商恢复，不能重新调度。
	if len(model.Routes) == 0 || shot.ReservationID != "" || (shot.Status != "pending" && shot.Status != "image_ready") {
		return model, nil
	}
	candidates := make([]routepool.Candidate, 0, len(model.Routes))
	for _, route := range model.Routes {
		if !route.Enabled {
			continue
		}
		provider, err := s.Repo.Store.GetUpstreamProvider(ctx, route.ProviderID)
		if err != nil {
			return Model{}, err
		}
		if provider == nil || !provider.Enabled {
			continue
		}
		candidates = append(candidates, routepool.Candidate{ID: route.ProviderID, Priority: route.Priority, Weight: route.Weight})
	}
	if len(candidates) == 0 {
		return Model{}, fmt.Errorf("media_route_pool_unavailable")
	}
	order := s.routeScheduler().Order(model.ID, candidates)
	providerID := candidates[order[0]].ID
	if _, err := s.workerUpdate(ctx, p, func(v *Project) {
		v.Shots[index].ProviderID = providerID
		v.Shots[index].Stage = kind
	}); err != nil {
		return Model{}, err
	}
	model.ProviderID = providerID
	return model, nil
}
