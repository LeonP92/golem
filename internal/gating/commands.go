package gating

// deniedCommands is the one source projected into native adapter rules and
// checked for each tool event. A command is denied only when it starts a
// command (see commandSegments), so words inside arguments are not matched.
// These rules prevent accidents, not shell escapes.
var deniedCommands = [][]string{
	{"git", "push"}, {"gh", "pr", "create"}, {"gh", "pr", "comment"},
	{"git", "reset", "--hard"}, {"git", "rebase"}, {"npm", "publish"}, {"cargo", "publish"},
	{"twine", "upload"}, {"python", "-m", "twine", "upload"}, {"sudo"},
	{"chmod", "777"},
}

// DeniedCommands returns the deny list for adapters that project it into native rules.
func DeniedCommands() [][]string { return deniedCommands }
