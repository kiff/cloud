package mandate

import (
	"context"
	"sort"
	"time"
)

func (s *MemoryStore) ReleaseEffects(_ context.Context, e NoEffectEvidence) ([]EffectRelease, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRecord != nil {
		return nil, s.failRecord
	}
	var out []EffectRelease
	for _, d := range s.draws {
		if d.TenantID != e.TenantID || d.ProposalID != e.ProposalID {
			continue
		}
		r, ok, err := eligibleRelease(d, e)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if old, exists := s.releases[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)]; exists {
			r = old
		}
		out = append(out, r)
	}
	// Validate the whole operation before any write, including overlapping Cards.
	if s.releases == nil {
		s.releases = map[string]EffectRelease{}
	}
	for _, r := range out {
		s.releases[key(r.TenantID, r.MandateID, r.ProposalID, r.QuantityKey)] = r
	}
	return out, nil
}

func (s *MemoryStore) EffectReleases(_ context.Context, tenant, id string) ([]EffectRelease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.failDraws != nil {
		return nil, s.failDraws
	}
	var out []EffectRelease
	for _, r := range s.releases {
		if r.TenantID == tenant && r.MandateID == id {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func (s *MemoryStore) LimitBalance(_ context.Context, tenant string, m Mandate, a Aggregate, now time.Time, exclude string) (LimitBalance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.limitBalanceLocked(tenant, m, a, now, exclude)
}

func (s *MemoryStore) limitBalanceLocked(tenant string, m Mandate, a Aggregate, now time.Time, exclude string) (LimitBalance, error) {
	if s.failUsage != nil {
		return LimitBalance{}, s.failUsage
	}
	since, err := a.Window.Start(now, m.Loc())
	if err != nil {
		return LimitBalance{}, err
	}
	b := LimitBalance{}
	for _, d := range s.draws {
		if d.TenantID != tenant || d.MandateID != m.ID || d.QuantityKey != a.Quantity.Key() || d.DrawnAt.Before(since) || d.DrawnAt.After(now) || (exclude != "" && exclude == d.ProposalID) {
			continue
		}
		var release *EffectRelease
		if r, ok := s.releases[key(tenant, m.ID, d.ProposalID, d.QuantityKey)]; ok {
			release = &r
		}
		if err := addBalance(&b, d, release, m, a, now, since); err != nil {
			return LimitBalance{}, err
		}
		if sc, ok := s.surcharges[key(tenant, m.ID, d.ProposalID, d.QuantityKey)]; ok && !sc.At.After(now) {
			b.Gross += sc.Amount
			b.Net = b.Gross - b.Released
		}
	}
	return b, nil
}

func (s *MemoryStore) SurchargeEffects(_ context.Context, e VerifiedEvidence) ([]EffectSurcharge, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRecord != nil {
		return nil, s.failRecord
	}
	var out []EffectSurcharge
	for _, d := range s.draws {
		if d.TenantID != e.TenantID || d.ProposalID != e.ProposalID {
			continue
		}
		sc, ok, err := eligibleSurcharge(d, e)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if old, exists := s.surcharges[key(d.TenantID, d.MandateID, d.ProposalID, d.QuantityKey)]; exists {
			sc = old
		}
		out = append(out, sc)
	}
	if s.surcharges == nil {
		s.surcharges = map[string]EffectSurcharge{}
	}
	for _, sc := range out {
		s.surcharges[key(sc.TenantID, sc.MandateID, sc.ProposalID, sc.QuantityKey)] = sc
	}
	return out, nil
}
