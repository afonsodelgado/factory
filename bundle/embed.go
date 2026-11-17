package bundle

import "embed"

//go:embed hooks.json tmux.conf skills agents codex opencode review pwa
var Files embed.FS
