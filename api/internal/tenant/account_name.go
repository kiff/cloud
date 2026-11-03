package tenant

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MetadataKeyAccountName is the name an admin gave the account.
const MetadataKeyAccountName = "account_name"

// MaxAccountNameLen is the longest account name, in characters.
const MaxAccountNameLen = 80

// ParseAccountName trims a name and accepts it if it has 1 to 80
// characters and no control characters.
func ParseAccountName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxAccountNameLen {
		return "", false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return value, true
}

// NameFor is what people call the account: the name an admin gave it,
// else the name it was created with. An account made at someone's first
// sign-in was created with that person's sign-in id as its name, which
// means nothing to anyone. Then NameFor returns no name, and that id as
// first, so the caller can name the account by that person's email.
func NameFor(t Tenant) (name, first string) {
	if v, ok := t.Metadata[MetadataKeyAccountName].(string); ok {
		if n, valid := ParseAccountName(v); valid {
			return n, ""
		}
	}
	if namedBySubject(t) {
		return "", t.DisplayName
	}
	return t.DisplayName, ""
}

// namedBySubject reports whether t still has the name EnsureForSubject
// gave it: the subject it was made for, whose sha-256 its slug carries.
func namedBySubject(t Tenant) bool {
	suffix, ok := strings.CutPrefix(t.Slug, "user-")
	if !ok || suffix == "" || t.DisplayName == "" {
		return false
	}
	digest := sha256.Sum256([]byte(t.DisplayName))
	return strings.HasPrefix(hex.EncodeToString(digest[:]), suffix)
}
