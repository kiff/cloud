package auth

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// DeviceUnusedAfter is how long a device goes unused before the dry run
// counts it as unused: 30 days since its last use (or, never used, since it
// signed in).
const DeviceUnusedAfter = 30 * 24 * time.Hour

// ActiveDeviceLister lists every account's devices that aren't revoked. The
// dry run only reads: nothing here can end a device.
type ActiveDeviceLister interface {
	ListAllActive(ctx context.Context) ([]ApprovalDevice, error)
}

// DeviceCleanupCounts is what the dry run would end on one account, if
// ending were on. It isn't: these are counts only.
type DeviceCleanupCounts struct {
	Active int
	// Unused: no use for DeviceUnusedAfter.
	Unused int
	// LikelySuperseded: an older key of what is likely the same phone.
	LikelySuperseded int
	// Either: unused, likely superseded or both (each device once).
	Either int
}

// lastActive is when the device was last used, or when it signed in if it
// never was.
func (d ApprovalDevice) lastActive() time.Time {
	if d.LastUsedAt != nil {
		return *d.LastUsedAt
	}
	return d.CreatedAt
}

// CountDeviceCleanup counts, per account, the active devices unused for
// unusedAfter and the likely superseded ones. A reinstalled app makes a new
// key, so a new device, under the same name; KIFF keeps no id of the phone
// itself, so "the same phone" is a guess: the same person, platform and
// non-empty name as a newer device that signed in after the older one was
// last used. Two phones with one name both in use don't match (the older one
// was used after the newer signed in); a device with no name is never
// matched.
func CountDeviceCleanup(devices []ApprovalDevice, now time.Time, unusedAfter time.Duration) map[string]DeviceCleanupCounts {
	type phone struct{ tenant, subject, platform, name string }
	newest := map[phone]ApprovalDevice{}
	for _, d := range devices {
		if d.IsRevoked() || d.Name == "" {
			continue
		}
		k := phone{d.Tenant, d.Subject, d.Platform, d.Name}
		if n, ok := newest[k]; !ok || d.CreatedAt.After(n.CreatedAt) || (d.CreatedAt.Equal(n.CreatedAt) && d.ID > n.ID) {
			newest[k] = d
		}
	}
	out := map[string]DeviceCleanupCounts{}
	for _, d := range devices {
		if d.IsRevoked() {
			continue
		}
		c := out[d.Tenant]
		c.Active++
		unused := now.Sub(d.lastActive()) >= unusedAfter
		superseded := false
		if n, ok := newest[phone{d.Tenant, d.Subject, d.Platform, d.Name}]; ok && d.Name != "" && n.ID != d.ID {
			superseded = !d.lastActive().After(n.CreatedAt)
		}
		if unused {
			c.Unused++
		}
		if superseded {
			c.LikelySuperseded++
		}
		if unused || superseded {
			c.Either++
		}
		out[d.Tenant] = c
	}
	return out
}

// DeviceCleanupDryRun logs, per account and in total, how many devices are
// unused or likely superseded. It ends nothing: ending devices is off, and
// there is no code path for it.
func DeviceCleanupDryRun(ctx context.Context, lister ActiveDeviceLister, now time.Time, unusedAfter time.Duration, logger *slog.Logger) (map[string]DeviceCleanupCounts, error) {
	devices, err := lister.ListAllActive(ctx)
	if err != nil {
		return nil, err
	}
	counts := CountDeviceCleanup(devices, now, unusedAfter)
	tenants := make([]string, 0, len(counts))
	for t := range counts {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)
	var total DeviceCleanupCounts
	for _, t := range tenants {
		c := counts[t]
		total.Active += c.Active
		total.Unused += c.Unused
		total.LikelySuperseded += c.LikelySuperseded
		total.Either += c.Either
		if c.Either == 0 {
			continue
		}
		logger.Info("devices dry run: account", slog.String("tenant", t), slog.Int("active", c.Active),
			slog.Int("unused", c.Unused), slog.Int("likely_superseded", c.LikelySuperseded),
			slog.Int("would_end", c.Either), slog.Bool("ending", false))
	}
	logger.Info("devices dry run: total", slog.Int("accounts", len(tenants)), slog.Int("active", total.Active),
		slog.Int("unused", total.Unused), slog.Int("likely_superseded", total.LikelySuperseded),
		slog.Int("would_end", total.Either), slog.Duration("unused_after", unusedAfter), slog.Bool("ending", false))
	return counts, nil
}

// ListAllActive returns every account's active devices.
func (s *MemoryDeviceStore) ListAllActive(_ context.Context) ([]ApprovalDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ApprovalDevice{}
	for _, d := range s.byID {
		if !d.IsRevoked() {
			out = append(out, d)
		}
	}
	return out, nil
}

// ListAllActive returns every account's active devices.
func (s *PostgresDeviceStore) ListAllActive(ctx context.Context) ([]ApprovalDevice, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sqlDeviceColumns+` FROM cloud_meta.approval_devices WHERE revoked_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ApprovalDevice{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
