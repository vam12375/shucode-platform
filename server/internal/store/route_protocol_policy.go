package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrUpstreamRouteProtocolConflict = errors.New("同一模型的启用路由必须使用同一 API 协议")

const upstreamRouteProtocolPolicyLockKey = "upstream-route-protocol-policy-v1"

func lockUpstreamRouteProtocolPolicy(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, upstreamRouteProtocolPolicyLockKey)
	return err
}

func validateUpstreamRouteProtocolTx(
	ctx context.Context,
	tx pgx.Tx,
	modelID string,
	protocol UpstreamProtocol,
	excludeRouteID string,
) error {
	rows, err := tx.Query(ctx, `
		SELECT p.protocol
		FROM upstream_routes r
		JOIN upstream_providers p ON p.id = r.provider_id
		WHERE r.model_id = $1
			AND r.enabled
			AND ($2::text = '' OR r.id::text <> $2)
	`, modelID, excludeRouteID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		if UpstreamProtocol(existing) != protocol {
			return ErrUpstreamRouteProtocolConflict
		}
	}
	return rows.Err()
}

func validateProviderRouteProtocolTx(ctx context.Context, tx pgx.Tx, providerID string, protocol UpstreamProtocol) error {
	var conflict bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM upstream_routes r
			JOIN upstream_providers p ON p.id = r.provider_id
			WHERE r.enabled
			GROUP BY r.model_id
			HAVING bool_or(r.provider_id = $1::uuid)
				AND COUNT(DISTINCT CASE WHEN r.provider_id = $1::uuid THEN $2::text ELSE p.protocol END) > 1
		)
	`, providerID, string(protocol)).Scan(&conflict)
	if err != nil {
		return err
	}
	if conflict {
		return ErrUpstreamRouteProtocolConflict
	}
	return nil
}

func (s *memoryStore) validateRouteProtocolLocked(modelID string, protocol UpstreamProtocol, excludeRouteID string) error {
	for _, route := range s.upstreamRoutes {
		if route.ModelID != modelID || !route.Enabled || route.ID == excludeRouteID {
			continue
		}
		provider, ok := s.providers[route.ProviderID]
		if ok && provider.Protocol != protocol {
			return ErrUpstreamRouteProtocolConflict
		}
	}
	return nil
}

func (s *memoryStore) validateProviderRouteProtocolLocked(providerID string, protocol UpstreamProtocol) error {
	for _, route := range s.upstreamRoutes {
		if route.ProviderID != providerID || !route.Enabled {
			continue
		}
		if err := s.validateRouteProtocolLocked(route.ModelID, protocol, route.ID); err != nil {
			return err
		}
	}
	return nil
}
