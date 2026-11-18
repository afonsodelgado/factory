package install

const ClaudeInstall = "curl -fsSL https://claude.ai/install.sh | bash"

func ClaudeCode() Check {
	return tool("claude", "2.1.300", "claude", []string{"--version"}, ClaudeInstall)
}

func Tmux() Check {
	return tool("tmux", "3.3", "tmux", []string{"-V"}, installCommand("tmux"))
}

func Toolchain() []Check {
	return []Check{
		ClaudeCode(),
		tool("python3", "3.8", "python3", []string{"--version"}, installCommand("python3")),
		tool("git", "2.31", "git", []string{"--version"}, installCommand("git")),
		Tmux(),
	}
}

func GitHub() Check                         { return ghCheck() }
func GhStack() Check                        { return ghStack() }
func Revmux(o Options) Check                { return revmux(o) }
func PluginCheck(o Options, p Plugin) Check { return plugin(o, p) }
func ClaudeLogin(o Options) Check           { return claudeLogin(o) }
func BrowserQA(o Options) Check             { return browserQA(o, false) }
func ToolChecks(o Options) []Check          { return toolChecks(o) }

func BundleChecks(o Options) []Check {
	return []Check{bundleCheck(o), hooksCheck(o)}
}

func HasCommand(name string) bool { return hasCommand(name) }
