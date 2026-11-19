package handlers

// SetExportCapForTest lowers the export's cap for a test, and returns the
// function that puts it back.
func SetExportCapForTest(n int) func() {
	old := exportReceiptCap
	exportReceiptCap = n
	return func() { exportReceiptCap = old }
}
