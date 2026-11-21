package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/hub"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/afonsodelgado/factory/internal/ui/dialog"
)

func runDialog(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: factory dialog <new|settings|switch|pause|comments|archive|delete|keys|phone|nodes|move> [--socket path]")
		return 2
	}
	flags := flag.NewFlagSet("dialog", flag.ContinueOnError)
	socket := flags.String("socket", state.StateDir()+"/hub.sock", "hub socket")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if err := dialogMain(args[0], *socket); err != nil {
		fmt.Fprintln(os.Stderr, "factory dialog:", err)
		time.Sleep(2 * time.Second)
		return 1
	}
	return 0
}

func dialogMain(kind, socket string) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("the hub is not running: %w", err)
	}
	defer conn.Close()
	pane := "dialog:" + kind
	enc := json.NewEncoder(conn)
	replies := bufio.NewScanner(conn)
	replies.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if err := enc.Encode(hub.Message{T: "hello", Pane: pane}); err != nil {
		return err
	}
	context, err := reply(conn, replies, 3*time.Second)
	if err != nil {
		return err
	}
	if context.T == "error" {
		return fmt.Errorf("%s", context.Error)
	}
	d, err := newDialog(kind, context.Data)
	if err != nil {
		return err
	}
	final, err := tea.NewProgram(d).Run()
	if err != nil {
		return err
	}
	result, ok := final.(dialog.Dialog).Result()
	if !ok {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := enc.Encode(hub.Message{T: "result", Pane: pane, Data: data}); err != nil {
		return err
	}
	done, err := reply(conn, replies, 15*time.Second)
	if err != nil {
		return err
	}
	if done.T == "error" {
		return fmt.Errorf("%s", done.Error)
	}
	return nil
}

func newDialog(kind string, context json.RawMessage) (dialog.Dialog, error) {
	if kind == "move" {
		cfg, err := state.LoadConfig()
		if err != nil {
			return nil, err
		}
		var c dialog.MoveContext
		if err := json.Unmarshal(context, &c); err != nil {
			return nil, err
		}
		return dialog.NewMove(c, newNodeActions(cfg)), nil
	}
	if kind == "phone" || kind == "nodes" {
		cfg, err := state.LoadConfig()
		if err != nil {
			return nil, err
		}
		actions := newNodeActions(cfg)
		status, err := actions.Status()
		if err != nil {
			return nil, err
		}
		if kind == "nodes" {
			var known dialog.NodesContext
			json.Unmarshal(context, &known)
			return dialog.NewNodes(nodesContext(cfg, status, known), actions), nil
		}
		return dialog.NewPhone(status, actions.phoneActions), nil
	}
	if kind == "new" {
		var c dialog.NewContext
		if err := json.Unmarshal(context, &c); err != nil {
			return nil, err
		}
		d := dialog.NewFactory(c)
		d.ProbeNode = probeNode
		return d, nil
	}
	d, ok := dialog.New(kind, context, 0, 0)
	if !ok {
		return nil, fmt.Errorf("no dialog called %q", kind)
	}
	return d, nil
}

func reply(conn net.Conn, scanner *bufio.Scanner, wait time.Duration) (hub.Message, error) {
	conn.SetReadDeadline(time.Now().Add(wait))
	defer conn.SetReadDeadline(time.Time{})
	for scanner.Scan() {
		var m hub.Message
		if json.Unmarshal(scanner.Bytes(), &m) == nil && m.T != "frame" {
			return m, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return hub.Message{}, err
	}
	return hub.Message{}, fmt.Errorf("the hub closed the connection")
}
