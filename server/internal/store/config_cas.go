package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
)

func configJSONValue(raw []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err := decoder.Decode(&value)
	return value, err
}
func configJSONEqual(left, right any) bool {
	switch value := left.(type) {
	case json.Number:
		other, ok := right.(json.Number)
		if !ok {
			return false
		}
		a, aOK := new(big.Rat).SetString(string(value))
		b, bOK := new(big.Rat).SetString(string(other))
		return aOK && bOK && a.Cmp(b) == 0
	case map[string]any:
		other, ok := right.(map[string]any)
		if !ok || len(value) != len(other) {
			return false
		}
		for key, item := range value {
			otherItem, exists := other[key]
			if !exists || !configJSONEqual(item, otherItem) {
				return false
			}
		}
		return true
	case []any:
		other, ok := right.([]any)
		if !ok || len(value) != len(other) {
			return false
		}
		for i, item := range value {
			if !configJSONEqual(item, other[i]) {
				return false
			}
		}
		return true
	default:
		return left == right
	}
}
func (s *memoryStore) CompareAndSwapConfigValue(_ context.Context, key string, expected, value []byte) (bool, error) {
	if !json.Valid(value) || (expected != nil && !json.Valid(expected)) {
		return false, fmt.Errorf("配置必须是合法 JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.config[key]
	if expected == nil {
		if exists {
			return false, nil
		}
	} else {
		if !exists {
			return false, nil
		}
		a, err := configJSONValue(current)
		if err != nil {
			return false, err
		}
		b, err := configJSONValue(expected)
		if err != nil {
			return false, err
		}
		if !configJSONEqual(a, b) {
			return false, nil
		}
	}
	s.config[key] = append([]byte(nil), value...)
	return true, nil
}
func (s *PostgresStore) CompareAndSwapConfigValue(ctx context.Context, key string, expected, value []byte) (bool, error) {
	if !json.Valid(value) || (expected != nil && !json.Valid(expected)) {
		return false, fmt.Errorf("配置必须是合法 JSON")
	}
	if expected == nil {
		tag, err := s.pool.Exec(ctx, `INSERT INTO platform_config(key,value,updated_at) VALUES($1,$2::jsonb,now()) ON CONFLICT(key) DO NOTHING`, key, value)
		if err != nil {
			return false, err
		}
		return tag.RowsAffected() > 0, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE platform_config SET value=$3::jsonb,updated_at=now() WHERE key=$1 AND value=$2::jsonb`, key, expected, value)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
