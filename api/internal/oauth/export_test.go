package oauth

// NewUserCodeForTest exposes newUserCode to the external tests.
var NewUserCodeForTest = newUserCode

// SetAfterPendingDeviceForTest sets the hook deviceApprove calls once it
// has read the code as pending.
func SetAfterPendingDeviceForTest(s *Server, f func()) { s.afterPendingDevice = f }
