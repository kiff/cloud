package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/kiff/cloud/api/internal/words"
)

// CardBudgetWarning uses a stable provider idempotency key on every retry.
// It contains the budget warning and Card link, never tool arguments.
// Its subject and link are in lang, the recipient's language, as text is.
func (r *ResendNotifier) CardBudgetWarning(ctx context.Context, to, eventID, text, cardURL string, lang words.Lang) error {
	h := sha256.Sum256([]byte(to))
	key := "card-warning/" + eventID + "/" + hex.EncodeToString(h[:16])
	w := words.Of(lang)
	return r.sendHTMLWithKey(ctx, to, w.WarnSubject, text+" "+cardURL,
		htmlBodyLink(text, cardURL, w.WarnReview), key)
}
