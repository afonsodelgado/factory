package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/install"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/state"
)

const (
	moveUsage      = "usage: factory move <factory> --to <node> [--force [--yes]] | --all --to <node>"
	movePoll       = time.Second
	doctorMoveWait = 30 * time.Second
)

type moveFlags struct {
	to              string
	all, force, yes bool
	args            []string
}

func (f moveFlags) valid() bool {
	switch {
	case f.all:
		return len(f.args) == 0 && f.to != "" && !f.force && !f.yes
	case f.force:
		return len(f.args) == 1 && f.to != ""
	}
	return len(f.args) == 1 && f.to != "" && !f.yes
}

func runMove(args []string) int {
	flags := flag.NewFlagSet("move", flag.ContinueOnError)
	var m moveFlags
	flags.StringVar(&m.to, "to", "", "the node that takes the factory over")
	flags.BoolVar(&m.all, "all", false, "move every movable factory of this node")
	flags.BoolVar(&m.force, "force", false, "take the factory over from its last checkpoint without its unreachable holder")
	flags.BoolVar(&m.yes, "yes", false, "accept the forced takeover warning without asking")
	if err := flags.Parse(reorder(flags, args)); err != nil {
		return 2
	}
	if m.args = flags.Args(); !m.valid() {
		fmt.Fprintln(os.Stderr, moveUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	var c *node.Client
	if err == nil {
		c, err = localNode(cfg)
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = m.run(ctx, c, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory move:", err)
		return 1
	}
	return 0
}

func (m moveFlags) run(ctx context.Context, c *node.Client, in *os.File, out io.Writer) error {
	switch {
	case m.all:
		return moveAll(ctx, c, m.to, out)
	case m.force:
		return takeOver(ctx, c, m.args[0], m.to, m.yes, in, out)
	}
	var p node.Progress
	if err := c.Do(ctx, http.MethodPost, "/api/transfers", node.MoveRequest{Operation: uuid.NewString(), Factory: m.args[0], Target: m.to}, &p); err != nil {
		return err
	}
	return follow(ctx, c, p, out)
}

func follow(ctx context.Context, c *node.Client, p node.Progress, out io.Writer) error {
	last := ""
	for {
		if line := describeMove(p); line != last {
			fmt.Fprintln(out, line)
			last = line
		}
		if done, err := finished(p, out); done {
			return err
		}
		select {
		case <-ctx.Done():
			return interruptMove(c, p, out)
		case <-time.After(movePoll):
		}
		if err := c.Do(ctx, http.MethodGet, "/api/transfers/"+p.Operation, nil, &p); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

func interruptMove(c *node.Client, p node.Progress, out io.Writer) error {
	if p.Phase == node.PhaseReleased || p.Phase == node.PhaseActive {
		fmt.Fprintf(out, "%s is already released; the node finishes the move without this command. factory doctor shows how it ends.\n", p.Factory)
		return nil
	}
	if err := c.Do(context.Background(), http.MethodPost, "/api/transfers/"+p.Operation+"/cancel", nil, &p); err != nil {
		return err
	}
	return fmt.Errorf("the move was cancelled; %s still runs here", p.Factory)
}

func describeMove(p node.Progress) string {
	if p.Factory == "" {
		return "starting the move"
	}
	switch p.State {
	case node.StateCheckpointWaiting:
		return p.Waiting
	case node.StateStaged:
		return fmt.Sprintf("%s has the checkpoint; stopping %s here", p.TargetName, p.Factory)
	case node.StateReleased:
		return fmt.Sprintf("%s is stopped here; %s is starting it", p.Factory, p.TargetName)
	case node.StateActive:
		return fmt.Sprintf("%s runs on %s now. Its files stay here until factory delete %s.", p.Factory, p.TargetName, p.Factory)
	case node.StateMoving:
		if p.Phase == node.PhaseCheckpoint {
			return fmt.Sprintf("sending the checkpoint of %s to %s", p.Factory, p.TargetName)
		}
		return fmt.Sprintf("checking that %s can run %s", p.TargetName, p.Factory)
	}
	return p.State
}

func finished(p node.Progress, out io.Writer) (bool, error) {
	switch p.State {
	case node.StateActive:
		return true, nil
	case node.StatePreflightFailed:
		printChecks(out, slices.DeleteFunc(slices.Clone(p.Checks), func(c node.Check) bool { return c.Mark == node.MarkOK }))
		return true, fmt.Errorf("%s; %s still runs here", p.Refusal, p.Factory)
	case node.StateRefused, node.StateCancelled:
		return true, fmt.Errorf("%s; %s still runs here", p.Refusal, p.Factory)
	case node.StateRecoveryNeeded, node.StateInterrupted:
		return true, errors.New(strings.Join(p.Recovery, "\n"))
	}
	return false, nil
}

func takeOver(ctx context.Context, c *node.Client, id, to string, yes bool, in *os.File, out io.Writer) error {
	request := node.TakeoverRequest{Operation: uuid.NewString(), Factory: id, Target: to}
	var unconfirmed struct{ Warning node.Unconfirmed }
	err := c.Do(ctx, http.MethodPost, "/api/transfers/takeover", request, &unconfirmed)
	var refusal node.StatusError
	if !errors.As(err, &refusal) || refusal.Status != http.StatusPreconditionRequired {
		return cmp.Or(err, errors.New("the node started a takeover without showing its warning"))
	}
	fmt.Fprintln(out, unconfirmed.Warning.Warning)
	switch {
	case yes:
	case !term.IsTerminal(in.Fd()):
		return errors.New("a forced takeover needs your confirmation; run it in a terminal, or add --yes once you have read the warning above")
	case !confirm(out, bufio.NewReader(in), fmt.Sprintf("Take %s over on %s anyway? [y/N] ", id, to)):
		return errors.New("nothing was changed")
	}
	request.Confirm = true
	var p node.Progress
	if err := c.Do(ctx, http.MethodPost, "/api/transfers/takeover", request, &p); err != nil {
		return err
	}
	if self, err := selfName(ctx, c); err != nil || !slices.Contains(self, to) {
		fmt.Fprintf(out, "%s is taking %s over from its checkpoint; the hub and the phone show when it runs there.\n", to, id)
		return err
	}
	return follow(ctx, c, p, out)
}

func selfName(ctx context.Context, c *node.Client) ([]string, error) {
	var roster node.Roster
	if err := c.Do(ctx, http.MethodGet, "/api/nodes", nil, &roster); err != nil {
		return nil, err
	}
	for _, m := range roster.Members {
		if m.Self {
			return []string{m.ID, m.Name, shortName(m.Name)}, nil
		}
	}
	return nil, nil
}

func moveAll(ctx context.Context, c *node.Client, to string, out io.Writer) error {
	var b node.BatchProgress
	if err := c.Do(ctx, http.MethodPost, "/api/transfers/all", node.MoveAllRequest{Operation: uuid.NewString(), Target: to}, &b); err != nil {
		return err
	}
	reported := map[string]bool{}
	cancelled := false
	for {
		for _, item := range b.Items {
			if !reported[item.Key] && itemSettled(item, b.Running) {
				reported[item.Key] = true
				fmt.Fprintf(out, "  %-20s %s\n", item.Factory, itemLine(item, b.TargetName))
			}
		}
		if !b.Running {
			return batchSummary(b, out)
		}
		select {
		case <-ctx.Done():
			if !cancelled {
				cancelled = true
				fmt.Fprintln(out, "cancelling the moves not released yet; a released one finishes")
				c.Do(context.Background(), http.MethodPost, "/api/transfers/all/"+b.Operation+"/cancel", nil, &b)
			}
		case <-time.After(movePoll):
		}
		if err := c.Do(context.Background(), http.MethodGet, "/api/transfers/all/"+b.Operation, nil, &b); err != nil {
			return err
		}
	}
}

func itemSettled(item node.ItemOutcome, running bool) bool {
	inFlight := []string{node.StateQueued, node.StateMoving, node.StateCheckpointWaiting, node.StateStaged, node.StateReleased}
	return !running || !slices.Contains(inFlight, item.State)
}

func itemLine(item node.ItemOutcome, target string) string {
	switch item.State {
	case node.StateActive:
		return "moved to " + target
	case node.StateCancelled:
		return "not moved: cancelled"
	case node.StateRecoveryNeeded, node.StateInterrupted:
		return "stopped mid-move: " + item.Reason + "; factory doctor shows how to recover it"
	}
	return "not moved: " + item.Reason
}

func batchSummary(b node.BatchProgress, out io.Writer) error {
	moved := len(slices.DeleteFunc(slices.Clone(b.Items), func(i node.ItemOutcome) bool { return i.State != node.StateActive }))
	fmt.Fprintf(out, "%s moved to %s, %d not moved.\n", count(moved, "factory"), b.TargetName, len(b.Items)-moved)
	if moved < len(b.Items) {
		return errors.New("some factories stayed here; the reasons are above")
	}
	return nil
}

func moveChecks(cfg *state.Config) []install.Check {
	self, err := os.ReadFile(filepath.Join(cfg.StateDir, "server-id"))
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), doctorMoveWait)
	defer cancel()
	moves := movesOf(ctx, cfg)
	registry, err := state.LoadRegistry(cfg.RegistryPath())
	if err != nil {
		return []install.Check{{Name: "moves", Mark: install.Warn, Detail: "cannot read the registry: " + err.Error()}}
	}
	var listed []node.Listed
	for _, e := range registry.Factories {
		if e.Incarnation != "" && e.MovedTo == "" {
			listed = append(listed, node.Listed{Factory: e.Slug, Incarnation: e.Incarnation})
		}
	}
	var checks []install.Check
	for _, c := range node.Diagnose(ctx, cfg.StateDir, string(self), moves, listed) {
		checks = append(checks, installCheck(c))
	}
	return checks
}

func movesOf(ctx context.Context, cfg *state.Config) []node.Progress {
	var moves []node.Progress
	if c, err := localNode(cfg); err == nil && c.Do(ctx, http.MethodGet, "/api/transfers", nil, &moves) == nil {
		return moves
	}
	for _, t := range node.Journals(cfg.StateDir) {
		moves = append(moves, node.ProgressOf(t, false))
	}
	return moves
}
