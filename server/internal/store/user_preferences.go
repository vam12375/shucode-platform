package store

import (
	"context"
	"time"
)

func cloneAgentMediaPreferences(record AgentMediaPreferencesRecord) *AgentMediaPreferencesRecord {
	copy := record
	copy.Preferences = append([]byte(nil), record.Preferences...)
	return &copy
}

func (s *memoryStore) GetAgentMediaPreferences(_ context.Context, userID string) (*AgentMediaPreferencesRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.agentMediaPreferences[userID]
	if !ok {
		return nil, nil
	}
	return cloneAgentMediaPreferences(record), nil
}

func (s *memoryStore) SaveAgentMediaPreferences(_ context.Context, userID string, expectedVersion *int64, preferences []byte, at time.Time) (*AgentMediaPreferencesRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.agentMediaPreferences[userID]
	if !exists {
		if expectedVersion != nil && *expectedVersion != 0 {
			return nil, ErrPreferenceVersionConflict
		}
		current = AgentMediaPreferencesRecord{UserID: userID}
	} else if expectedVersion == nil || current.Version != *expectedVersion {
		return nil, ErrPreferenceVersionConflict
	}
	current.Version++
	current.Preferences = append([]byte(nil), preferences...)
	current.UpdatedAt = at
	s.agentMediaPreferences[userID] = current
	return cloneAgentMediaPreferences(current), nil
}

func (s *PostgresStore) GetAgentMediaPreferences(ctx context.Context, userID string) (*AgentMediaPreferencesRecord, error) {
	var record AgentMediaPreferencesRecord
	err := s.pool.QueryRow(ctx, `
		SELECT user_id::text, preferences::text, version, updated_at
		FROM user_agent_media_preferences WHERE user_id = $1::uuid`, userID).
		Scan(&record.UserID, &record.Preferences, &record.Version, &record.UpdatedAt)
	if queryErrNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *PostgresStore) SaveAgentMediaPreferences(ctx context.Context, userID string, expectedVersion *int64, preferences []byte, at time.Time) (*AgentMediaPreferencesRecord, error) {
	var record AgentMediaPreferencesRecord
	if expectedVersion == nil || *expectedVersion == 0 {
		err := s.pool.QueryRow(ctx, `
			INSERT INTO user_agent_media_preferences (user_id, preferences, version, updated_at)
			VALUES ($1::uuid, $2::jsonb, 1, $3::timestamptz)
			ON CONFLICT (user_id) DO NOTHING
			RETURNING user_id::text, preferences::text, version, updated_at`, userID, preferences, at).
			Scan(&record.UserID, &record.Preferences, &record.Version, &record.UpdatedAt)
		if queryErrNoRows(err) || uniqueViolation(err) {
			return nil, ErrPreferenceVersionConflict
		}
		if err != nil {
			return nil, err
		}
		return &record, nil
	}
	err := s.pool.QueryRow(ctx, `
		UPDATE user_agent_media_preferences
		SET preferences = $3::jsonb, version = version + 1, updated_at = $4::timestamptz
		WHERE user_id = $1::uuid AND version = $2
		RETURNING user_id::text, preferences::text, version, updated_at`, userID, *expectedVersion, preferences, at).
		Scan(&record.UserID, &record.Preferences, &record.Version, &record.UpdatedAt)
	if queryErrNoRows(err) {
		return nil, ErrPreferenceVersionConflict
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
