package tmux

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Client struct {
	Socket     string
	ConfigFile string
}

type Pane struct {
	ID         string
	Session    string
	WindowID   string
	WindowName string
	Command    string
	Path       string
	PID        int
	Dead       bool
	InMode     bool
	Width      int
	Height     int
	Slug       string
	Repo       string
	Title      string
	UI         string
}

const sep = "\x1f"

var paneFields = []string{
	"#{pane_id}", "#{session_name}", "#{window_id}", "#{window_name}", "#{pane_current_command}",
	"#{pane_current_path}", "#{pane_pid}", "#{pane_dead}", "#{pane_in_mode}", "#{pane_width}", "#{pane_height}",
	"#{@factory_slug}", "#{@factory_repo}", "#{@factory_title}", "#{@factory_ui}",
}

func (c Client) args(args ...string) []string {
	var base []string
	switch {
	case strings.Contains(c.Socket, "/"):
		base = append(base, "-S", c.Socket)
	case c.Socket != "":
		base = append(base, "-L", c.Socket)
	}
	if c.ConfigFile != "" {
		base = append(base, "-f", c.ConfigFile)
	}
	return append(base, args...)
}

func (c Client) Command(args ...string) *exec.Cmd {
	return exec.Command("tmux", c.args(args...)...)
}

func (c Client) Run(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := c.Command(args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func (c Client) Panes() ([]Pane, error) {
	out, err := c.Run("list-panes", "-a", "-F", strings.Join(paneFields, sep))
	if err != nil {
		if c.noServer(err) {
			return nil, nil
		}
		return nil, err
	}
	var panes []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != len(paneFields) {
			continue
		}
		pid, _ := strconv.Atoi(f[6])
		width, _ := strconv.Atoi(f[9])
		height, _ := strconv.Atoi(f[10])
		panes = append(panes, Pane{
			ID: f[0], Session: f[1], WindowID: f[2], WindowName: f[3], Command: f[4], Path: f[5], PID: pid,
			Dead: f[7] == "1", InMode: f[8] == "1", Width: width, Height: height,
			Slug: f[11], Repo: f[12], Title: f[13], UI: f[14],
		})
	}
	return panes, nil
}

func (c Client) noServer(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting to") || strings.Contains(msg, "No such file or directory")
}

func (c Client) Sessions() ([]string, error) {
	out, err := c.Run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if c.noServer(err) {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (c Client) HasSession(name string) bool {
	_, err := c.Run("has-session", "-t", "="+name)
	return err == nil
}

func (c Client) NewSession(name, window string, width, height int, command string) (string, error) {
	args := []string{"new-session", "-d", "-s", name, "-n", window, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "-P", "-F", "#{pane_id}"}
	if command != "" {
		args = append(args, command)
	}
	return c.Run(args...)
}

type Window struct {
	Session string
	Name    string
	Dir     string
	Env     []string
	Command string
}

func (c Client) NewWindow(w Window) (string, error) {
	args := []string{"new-window", "-d", "-t", w.Session + ":", "-n", w.Name, "-P", "-F", "#{pane_id}"}
	if w.Dir != "" {
		args = append(args, "-c", w.Dir)
	}
	for _, env := range w.Env {
		args = append(args, "-e", env)
	}
	if w.Command != "" {
		args = append(args, w.Command)
	}
	return c.Run(args...)
}

type Split struct {
	Target     string
	Horizontal bool
	Before     bool
	Full       bool
	Size       int
	Command    string
}

func (c Client) Split(s Split) (string, error) {
	args := []string{"split-window", "-d", "-t", s.Target, "-P", "-F", "#{pane_id}"}
	if s.Horizontal {
		args = append(args, "-h")
	} else {
		args = append(args, "-v")
	}
	if s.Before {
		args = append(args, "-b")
	}
	if s.Full {
		args = append(args, "-f")
	}
	if s.Size > 0 {
		args = append(args, "-l", strconv.Itoa(s.Size))
	}
	if s.Command != "" {
		args = append(args, s.Command)
	}
	return c.Run(args...)
}

func (c Client) Swap(source, target string) error {
	_, err := c.Run("swap-pane", "-d", "-s", source, "-t", target)
	return err
}

func (c Client) Break(pane, session, window string) error {
	_, err := c.Run("break-pane", "-d", "-s", pane, "-t", session+":", "-n", window)
	return err
}

func (c Client) Join(pane, target string, size string) error {
	args := []string{"join-pane", "-d", "-h", "-s", pane, "-t", target}
	if size != "" {
		args = append(args, "-l", size)
	}
	_, err := c.Run(args...)
	return err
}

func (c Client) RenameWindow(windowID, name string) error {
	_, err := c.Run("rename-window", "-t", windowID, name)
	return err
}

func (c Client) ResizeWindow(windowID string, width, height int) error {
	_, err := c.Run("resize-window", "-t", windowID, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
	return err
}

func (c Client) ResizePane(pane string, width, height int) error {
	args := []string{"resize-pane", "-t", pane}
	if width > 0 {
		args = append(args, "-x", strconv.Itoa(width))
	}
	if height > 0 {
		args = append(args, "-y", strconv.Itoa(height))
	}
	_, err := c.Run(args...)
	return err
}

func (c Client) SetPaneOption(pane, name, value string) error {
	_, err := c.Run("set-option", "-p", "-t", pane, name, value)
	return err
}

func (c Client) SetWindowOption(window, name, value string) error {
	_, err := c.Run("set-option", "-w", "-t", window, name, value)
	return err
}

func (c Client) SetSessionOption(session, name, value string) error {
	_, err := c.Run("set-option", "-t", session, name, value)
	return err
}

func (c Client) Bind(key string, command ...string) error {
	_, err := c.Run(append([]string{"bind-key", "-n", key}, command...)...)
	return err
}

func (c Client) Unbind(key string) error {
	_, err := c.Run("unbind-key", "-n", key)
	return err
}

type Popup struct {
	Title   string
	Width   string
	Height  string
	Command string
	Client  string
}

func (c Client) Popup(p Popup) error {
	args := []string{"display-popup", "-E", "-b", "rounded"}
	if p.Client != "" {
		args = append(args, "-c", p.Client)
	}
	if p.Title != "" {
		args = append(args, "-T", p.Title)
	}
	if p.Width != "" {
		args = append(args, "-w", p.Width)
	}
	if p.Height != "" {
		args = append(args, "-h", p.Height)
	}
	args = append(args, p.Command)
	_, err := c.Run(args...)
	return err
}

func (c Client) Paste(pane, buffer, text string) error {
	if _, err := c.Run("set-buffer", "-b", buffer, "--", text); err != nil {
		return err
	}
	_, err := c.Run("paste-buffer", "-d", "-p", "-b", buffer, "-t", pane)
	return err
}

func (c Client) SendKeys(pane string, keys ...string) error {
	_, err := c.Run(append([]string{"send-keys", "-t", pane}, keys...)...)
	return err
}

func (c Client) SelectPane(pane string) error {
	_, err := c.Run("select-pane", "-t", pane)
	return err
}

func (c Client) DisableInput(pane string) error {
	_, err := c.Run("select-pane", "-d", "-t", pane)
	return err
}

func (c Client) KillPane(pane string) error {
	_, err := c.Run("kill-pane", "-t", pane)
	return err
}

func (c Client) KillWindow(window string) error {
	_, err := c.Run("kill-window", "-t", window)
	return err
}

func (c Client) KillSession(name string) error {
	_, err := c.Run("kill-session", "-t", "="+name)
	return err
}

func (c Client) Display(target, format string) (string, error) {
	return c.Run("display-message", "-p", "-t", target, format)
}

func (c Client) WindowSize(target string) (int, int, error) {
	out, err := c.Display(target, "#{window_width} #{window_height}")
	if err != nil {
		return 0, 0, err
	}
	var width, height int
	_, err = fmt.Sscan(out, &width, &height)
	return width, height, err
}

func (c Client) Clients(session string) ([]string, error) {
	out, err := c.Run("list-clients", "-t", session, "-F", "#{client_name}")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}
