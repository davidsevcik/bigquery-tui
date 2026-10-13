package config

import "strings"

// ResolveEditor picks the editor command: $VISUAL, then $EDITOR, then the OS
// opener (open -t on macOS, xdg-open elsewhere).
func ResolveEditor(getenv func(string) string, goos string) []string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return strings.Fields(v)
		}
	}
	if goos == "darwin" {
		return []string{"open", "-t"}
	}
	return []string{"xdg-open"}
}
