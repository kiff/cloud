package mandate

import "context"

func (s *MemoryStore) GetWarningConfig(_ context.Context, tenant, id string) (WarningConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if items := s.warningConfigs[key(tenant, id)]; len(items) > 0 {
		return items[len(items)-1], nil
	}
	return WarningConfig{Settings: DefaultWarningSettings()}, nil
}
func (s *MemoryStore) SetWarningConfig(_ context.Context, tenant, id, by string, expected int64, settings WarningSettings) (WarningConfig, error) {
	if err := settings.Validate(); err != nil {
		return WarningConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mandates[key(tenant, id)]; !ok {
		return WarningConfig{}, ErrWarningHistory
	}
	items := s.warningConfigs[key(tenant, id)]
	revision := int64(len(items))
	if revision != expected {
		return WarningConfig{}, ErrWarningConflict
	}
	c := WarningConfig{Revision: revision + 1, At: s.now(), By: by, Settings: settings}
	if s.warningConfigs == nil {
		s.warningConfigs = map[string][]WarningConfig{}
	}
	s.warningConfigs[key(tenant, id)] = append(items, c)
	return c, nil
}
func (s *MemoryStore) CardWarnings(_ context.Context, tenant, id string) ([]CardWarning, error) {
	return []CardWarning{}, nil
}
