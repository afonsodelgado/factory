package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

const hostUsage = "usage: factory host attach <view> | factory host stream [--user <user>] [--label <factory>] <machine> <view>"

func runHost(args []string) int {
	if len(args) == 2 && args[0] == "attach" {
		err := attachView(args[1])
		fmt.Fprintln(os.Stderr, "factory host attach:", err)
		return 1
	}
	if len(args) > 0 && args[0] == "stream" {
		return runStream(args[1:])
	}
	if len(args) > 0 && args[0] == "run" {
		fmt.Fprintln(os.Stderr, "factory host: host run is gone; the node runs this machine's factories. Start it with factory node run")
		return 1
	}
	fmt.Fprintln(os.Stderr, hostUsage)
	return 2
}

func attachView(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return errors.New("a view id is a UUID from the node's attach preparation")
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return err
	}
	tm, err := factoryTmux(cfg)
	if err != nil {
		return err
	}
	cmd := tm.Command("attach-session", "-t", "="+host.ViewSession(id))
	path, err := exec.LookPath(cmd.Path)
	if err != nil {
		return err
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "TMUX=") })
	return syscall.Exec(path, cmd.Args, env)
}

func runStream(args []string) int {
	flags := flag.NewFlagSet("host stream", flag.ContinueOnError)
	login := flags.String("user", "", "the user that runs the node on that machine, for the ssh fallback")
	label := flags.String("label", "", "the factory's name, for the ssh fallback")
	if err := flags.Parse(args); err != nil || flags.NArg() != 2 {
		fmt.Fprintln(os.Stderr, hostUsage)
		return 2
	}
	to := node.StreamTarget{Machine: flags.Arg(0), User: *login, Label: *label}
	if err := streamView(context.Background(), node.PeerHTTP(), to, flags.Arg(1), os.Stdout, execSSH); err != nil {
		fmt.Fprintln(os.Stderr, "factory host stream:", err)
		return 1
	}
	return 0
}

func streamView(ctx context.Context, h *http.Client, to node.StreamTarget, view string, out io.Writer, ssh func([]string) error) error {
	in := os.Stdin.Fd()
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)
	conn, err := node.DialStream(ctx, h, to.Machine, view, terminalSize(in), os.Getenv("TERM"))
	if errors.Is(err, node.ErrNoStream) {
		words := sshWords(to, view)
		fmt.Fprint(out, sshExplanation(ctx, h, to, words))
		return ssh(words)
	}
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	if raw, err := term.MakeRaw(in); err == nil {
		defer term.Restore(in, raw)
	}
	select {
	case resized <- syscall.SIGWINCH:
	default:
	}
	go sendResizes(ctx, conn, in, resized)
	go sendInput(ctx, conn, os.Stdin)
	return showOutput(ctx, conn, out)
}

func terminalSize(fd uintptr) node.Resize {
	width, height, err := term.GetSize(fd)
	if err != nil || width <= 0 || height <= 0 {
		return node.Resize{Cols: 80, Rows: 24}
	}
	return node.Resize{Cols: uint16(width), Rows: uint16(height)}
}

func sendResizes(ctx context.Context, conn *websocket.Conn, fd uintptr, resized <-chan os.Signal) {
	for range resized {
		if wsjson.Write(ctx, conn, terminalSize(fd)) != nil {
			return
		}
	}
}

func sendInput(ctx context.Context, conn *websocket.Conn, in io.Reader) {
	buf := make([]byte, node.StreamChunk)
	for {
		n, err := in.Read(buf)
		if n > 0 && conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
			return
		}
		if err != nil {
			conn.Close(websocket.StatusNormalClosure, "the terminal closed")
			return
		}
	}
}

func showOutput(ctx context.Context, conn *websocket.Conn, out io.Writer) error {
	for {
		_, data, err := conn.Read(ctx)
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}
}

func sshWords(to node.StreamTarget, view string) []string {
	destination := to.Machine
	if to.User != "" {
		destination = to.User + "@" + to.Machine
	}
	return []string{"ssh", "-t", destination, "factory", "host", "attach", view}
}

func sshExplanation(ctx context.Context, h *http.Client, to node.StreamTarget, words []string) string {
	short, _, _ := strings.Cut(to.Machine, ".")
	here := "this machine"
	if runtime.GOOS == "darwin" {
		here = "this Mac"
	}
	release := "an older factory"
	var me node.Me
	if node.Remote(to.Machine, h).Do(ctx, http.MethodGet, "/api/me", nil, &me) == nil && me.Version != "" {
		release = "factory " + me.Version
	}
	login, as := to.User, to.User+", the user that runs "+short+"'s node"
	if login == "" {
		login = localUser()
		as = login + ", your user on " + here + ", because " + short + " does not say which user runs its node"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "↔ %s runs %s, which cannot stream sessions to this hub\n\n", short, release)
	fmt.Fprintf(&b, "This pane opens %s over ssh instead, as %s:\n\n", cmp.Or(to.Label, "the session"), as)
	fmt.Fprintf(&b, "    %s\n\n", strings.Join(words, " "))
	fmt.Fprintf(&b, "That needs ssh from %s to %s: Remote Login or sshd on %s, and %s's key in %s's ~/.ssh/authorized_keys. If ssh asks for a password below, it is the password of %s on %s.\n\n", here, short, short, here, login, login, short)
	fmt.Fprintf(&b, "factory update brings %s to %s. ⏎ then streams the session with no ssh.\n\n", short, version)
	return b.String()
}

func localUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

func execSSH(words []string) error {
	path, err := exec.LookPath(words[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, words, os.Environ())
}
