package playstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrBadPush is a push body that isn't a Pub/Sub message carrying a Play
// developer notification.
var ErrBadPush = errors.New("playstore: not a Pub/Sub push of a Play notification")

// Subscription notification types (Real-time Developer Notifications).
const (
	SubRecovered            = 1
	SubRenewed              = 2
	SubCanceled             = 3
	SubPurchased            = 4
	SubOnHold               = 5
	SubInGracePeriod        = 6
	SubRestarted            = 7
	SubPaused               = 10
	SubRevoked              = 12
	SubExpired              = 13
	SubPendingPurchaseCancl = 20
)

// One-time product notification types.
const (
	OneTimePurchased = 1
	OneTimeCanceled  = 2
)

// Voided purchase product types.
const (
	VoidedSubscription = 1
	VoidedOneTime      = 2
)

// DeveloperNotification is a Play Real-time Developer Notification: one of
// its notifications is set. Only the purchase token and the product are
// read from it; the purchase's state is always read back from Play.
type DeveloperNotification struct {
	PackageName              string `json:"packageName"`
	SubscriptionNotification *struct {
		NotificationType int    `json:"notificationType"`
		PurchaseToken    string `json:"purchaseToken"`
		SubscriptionID   string `json:"subscriptionId"`
	} `json:"subscriptionNotification"`
	OneTimeProductNotification *struct {
		NotificationType int    `json:"notificationType"`
		PurchaseToken    string `json:"purchaseToken"`
		Sku              string `json:"sku"`
	} `json:"oneTimeProductNotification"`
	VoidedPurchaseNotification *struct {
		PurchaseToken string `json:"purchaseToken"`
		OrderID       string `json:"orderId"`
		ProductType   int    `json:"productType"`
		RefundType    int    `json:"refundType"`
	} `json:"voidedPurchaseNotification"`
	TestNotification *struct{} `json:"testNotification"`
	// MessageID is the Pub/Sub message's id.
	MessageID string `json:"-"`
}

// DecodePush reads a Pub/Sub push body: {"message": {"data": base64 of the
// notification, "messageId": …}, "subscription": …}.
func DecodePush(body []byte) (DeveloperNotification, error) {
	var push struct {
		Message struct {
			Data       string `json:"data"`
			MessageID  string `json:"messageId"`
			MessageID2 string `json:"message_id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &push); err != nil || push.Message.Data == "" {
		return DeveloperNotification{}, ErrBadPush
	}
	raw, err := base64.StdEncoding.DecodeString(push.Message.Data)
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(push.Message.Data); err != nil {
			return DeveloperNotification{}, fmt.Errorf("%w: data", ErrBadPush)
		}
	}
	var n DeveloperNotification
	if err := json.Unmarshal(raw, &n); err != nil {
		return DeveloperNotification{}, fmt.Errorf("%w: notification", ErrBadPush)
	}
	n.MessageID = push.Message.MessageID
	if n.MessageID == "" {
		n.MessageID = push.Message.MessageID2
	}
	return n, nil
}

// Kind names the notification, for logs.
func (n DeveloperNotification) Kind() string {
	switch {
	case n.SubscriptionNotification != nil:
		return fmt.Sprintf("subscription:%d", n.SubscriptionNotification.NotificationType)
	case n.OneTimeProductNotification != nil:
		return fmt.Sprintf("one_time:%d", n.OneTimeProductNotification.NotificationType)
	case n.VoidedPurchaseNotification != nil:
		return fmt.Sprintf("voided:%d", n.VoidedPurchaseNotification.ProductType)
	case n.TestNotification != nil:
		return "test"
	}
	return "unknown"
}
