package handlers

import "time"

// SetClock sets a handler's clock, for the approval window tests.
func SetClock(h any, now func() time.Time) {
	switch h := h.(type) {
	case *ApprovalWindowHandler:
		h.now = now
	case *HaltsHandler:
		h.now = now
	case *DeviceVerifyHandler:
		h.now = now
	case *DeviceSignatureHandler:
		h.now = now
	default:
		panic("SetClock: no clock on this handler")
	}
}
