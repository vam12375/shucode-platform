package apiapp

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

var errModelRouteProtocolConflict = store.ErrUpstreamRouteProtocolConflict

func validateModelRouteProtocol(
	ctx context.Context,
	deps *Deps,
	modelID string,
	protocol store.UpstreamProtocol,
	excludeRouteID string,
) error {
	routes, err := deps.Store.ListUpstreamRoutes(ctx, modelID, true)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if !route.Enabled || route.ID == excludeRouteID {
			continue
		}
		provider, err := deps.Store.GetUpstreamProvider(ctx, route.ProviderID)
		if err != nil {
			return err
		}
		if provider == nil {
			return errors.New("渠道路由引用了不存在的供应商")
		}
		if provider.Protocol != protocol {
			return errModelRouteProtocolConflict
		}
	}
	return nil
}

func validateProviderRouteProtocolMutation(
	ctx context.Context,
	deps *Deps,
	providerID string,
	protocol store.UpstreamProtocol,
) error {
	models, err := deps.Store.ListModels(ctx, true)
	if err != nil {
		return err
	}
	for _, model := range models {
		routes, err := deps.Store.ListUpstreamRoutes(ctx, model.ModelID, true)
		if err != nil {
			return err
		}
		containsProvider := false
		protocols := make([]store.UpstreamProtocol, 0, len(routes))
		for _, route := range routes {
			if !route.Enabled {
				continue
			}
			upstream, err := deps.Store.GetUpstreamProvider(ctx, route.ProviderID)
			if err != nil {
				return err
			}
			if upstream == nil {
				return errors.New("渠道路由引用了不存在的供应商")
			}
			candidateProtocol := upstream.Protocol
			if route.ProviderID == providerID {
				containsProvider = true
				candidateProtocol = protocol
			}
			protocols = append(protocols, candidateProtocol)
		}
		if containsProvider && len(protocols) > 1 {
			for _, candidateProtocol := range protocols[1:] {
				if candidateProtocol != protocols[0] {
					return errModelRouteProtocolConflict
				}
			}
		}
	}
	return nil
}

func findUpstreamRoute(ctx context.Context, deps *Deps, routeID string) (*store.UpstreamRouteRecord, error) {
	models, err := deps.Store.ListModels(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		routes, err := deps.Store.ListUpstreamRoutes(ctx, model.ModelID, true)
		if err != nil {
			return nil, err
		}
		for i := range routes {
			if routes[i].ID == routeID {
				return &routes[i], nil
			}
		}
	}
	return nil, nil
}

func writeRouteProtocolConflict(c *gin.Context, err error) bool {
	if !errors.Is(err, errModelRouteProtocolConflict) {
		return false
	}
	c.JSON(409, fail(409, errModelRouteProtocolConflict.Error()))
	return true
}
