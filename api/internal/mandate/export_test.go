package mandate

// SetAfterDiscover installs a hook that runs between discovering a
// subject's Cards and locking them, so a test can commit a move in
// exactly that gap.
func SetAfterDiscover(a *Authorizer, fn func()) { a.afterDiscover = fn }

// SetBeforeWrite installs a hook that runs after a decision is made and
// before its draws are recorded.
func SetBeforeWrite(a *Authorizer, fn func()) { a.beforeWrite = fn }

// CardLockID is the advisory-lock id a Card's lock is taken under, so a
// test can find the session holding it in pg_locks.
func CardLockID(tenantID, mandateID string) int64 {
	return sortedCardLockIDs([]string{CardLockKey(tenantID, mandateID)})[0]
}

// SetAfterIssueLock installs a hook that runs once PostgresStore.Issue
// holds the Card's lock.
func SetAfterIssueLock(s *PostgresStore, fn func()) { s.afterIssueLock = fn }

// LegacyFingerprintForTest is the pre-#1147 fingerprint, for tests of
// exceptions recorded then.
var LegacyFingerprintForTest = legacyFingerprint
