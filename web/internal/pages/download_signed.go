package pages

// WindowsSigned is whether Windows' published installer is signed
// (KIFF_WINDOWS_SIGNED=on). Off, the download page says the beta isn't signed
// yet and what Windows will ask, under the Windows download. It's off until a
// signed release is published: a missing switch shows the warning, never hides
// it.
var WindowsSigned = false
