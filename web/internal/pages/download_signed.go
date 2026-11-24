package pages

// WindowsSigned is whether Windows' published installer is signed
// (KIFF_WINDOWS_SIGNED=on). Off, Windows is coming soon on the download page,
// "once its installer is signed", with no download, whatever release is
// published: an unsigned installer is never offered there. It's off until a
// signed release is published, so a missing switch holds the download back,
// never offers it.
var WindowsSigned = false
