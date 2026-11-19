package pages

// computerAppReleased says whether /support shows §6, adding a computer with
// your phone. It's off until a computer's app has a public download, the
// same switch /tafs/download uses: the day a computer's release is published
// and pages.DesktopApps gives it a URL (package desktop), §6 appears in every
// language with no other change.
func computerAppReleased() bool { return anyDesktopDownload(DesktopApps()) }

// anyDesktopDownload reports whether any of apps has a file to download.
func anyDesktopDownload(apps []DesktopApp) bool {
	for _, a := range apps {
		if a.URL != "" {
			return true
		}
	}
	return false
}
