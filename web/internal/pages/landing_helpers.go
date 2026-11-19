package pages

// fwFile maps a snippet language to the filename shown in the terminal
// card's title bar, so each stack's card reads like the file you'd edit.
func fwFile(lang string) string {
	switch lang {
	case "ts":
		return "agent.ts"
	case "bash":
		return "shell"
	default:
		return "agent.py"
	}
}
