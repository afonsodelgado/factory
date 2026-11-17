package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/remote"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/google/uuid"
)

const reviewUsage = `usage: factory review reply --thread <id> [--resolve] [--factory id --remote name] [text…]
       factory review threads [--page <path>] [--open] [--factory id --remote name]
       factory review open [--factory <id>] [--for design|walkthrough] <url>
       factory review stack [--for design|walkthrough]
       factory review link <url>`

func runReview(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	switch args[0] {
	case "reply":
		return runReviewReply(args[1:])
	case "threads":
		return runReviewThreads(args[1:])
	case "open":
		return runReviewOpen(args[1:])
	case "stack":
		return runReviewStack(args[1:])
	case "link":
		return runReviewLink(args[1:])
	}
	fmt.Fprintln(os.Stderr, reviewUsage)
	return 2
}

func runReviewReply(args []string) int {
	flags := flag.NewFlagSet("review reply", flag.ContinueOnError)
	thread := flags.String("thread", "", "the thread id, such as t-3")
	resolve := flags.Bool("resolve", false, "mark the thread resolved")
	owner := ownerFlags(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	text := strings.Join(flags.Args(), " ")
	if *thread == "" || (text == "" && !*resolve) || !owner.valid() {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	if _, err := owner.ask(host.ReviewRequest{Action: "reply", Thread: *thread, Text: text, Resolve: *resolve}); err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	return 0
}

func runReviewThreads(args []string) int {
	flags := flag.NewFlagSet("review threads", flag.ContinueOnError)
	page := flags.String("page", "", "only the threads on this page, relative to the factory directory")
	open := flags.Bool("open", false, "only the threads that are not resolved")
	owner := ownerFlags(flags)
	if err := flags.Parse(args); err != nil || !owner.valid() {
		return 2
	}
	reply, err := owner.ask(host.ReviewRequest{Action: "threads", Page: *page})
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	for _, t := range reply.Threads {
		if *open && !t.Open() {
			continue
		}
		fmt.Printf("%s  %s  %s  %s\n", t.ID, t.Status, t.Page, t.Messages[len(t.Messages)-1].Text)
	}
	return 0
}

func runReviewOpen(args []string) int {
	flags := flag.NewFlagSet("review open", flag.ContinueOnError)
	id := flags.String("factory", "", "the factory the comments belong to; default: the one this directory is in")
	stage := flags.String("for", "walkthrough", "the stage whose agent gets the comments: design (the designer) or walkthrough (the fixer)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	page, ok := livePages[*stage]
	if flags.NArg() != 1 || !ok {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	if _, err := askReview(host.ReviewRequest{Action: "open", ID: *id, URL: flags.Arg(0), Page: page}); err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	fmt.Printf("Open %s in your browser to review it; a connected laptop hub also opens it through a tunnel, and its comments come back here.\n", flags.Arg(0))
	return 0
}

func runReviewLink(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	if _, err := askReview(host.ReviewRequest{Action: "link", URL: args[0]}); err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	fmt.Printf("Open %s in your browser; a connected laptop hub also opens it.\n", args[0])
	return 0
}

var livePages = map[string]string{"design": review.Prototype, "walkthrough": review.App}

func runReviewStack(args []string) int {
	flags := flag.NewFlagSet("review stack", flag.ContinueOnError)
	stage := flags.String("for", "walkthrough", "the stage whose agent gets the comments: design (the designer) or walkthrough (the fixer)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	page, ok := livePages[*stage]
	if !ok {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	if _, err := askReview(host.ReviewRequest{Action: "stack", Page: page}); err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	fmt.Println("Starting the stack in its own window; open its URL from review/stack.json in your browser once it answers. A connected laptop hub opens it through a tunnel.")
	return 0
}

type reviewOwner struct{ id, remote *string }

func ownerFlags(flags *flag.FlagSet) reviewOwner {
	return reviewOwner{
		id:     flags.String("factory", "", "the factory, when this is not one of its panes"),
		remote: flags.String("remote", "", "the remote the factory runs on; needs --factory"),
	}
}

func (o reviewOwner) valid() bool { return *o.remote == "" || *o.id != "" }

func (o reviewOwner) ask(r host.ReviewRequest) (host.ReviewReply, error) {
	r.ID = *o.id
	if *o.remote == "" {
		return askReview(r)
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		return host.ReviewReply{}, err
	}
	client, err := remote.For(cfg, *o.remote)
	if err != nil {
		return host.ReviewReply{}, err
	}
	var reply host.ReviewReply
	if r.Action == "threads" {
		return reply, client.Read(context.Background(), "review", r, &reply)
	}
	return reply, client.MutateOnce(context.Background(), "review", r, &reply)
}

func askReview(r host.ReviewRequest) (host.ReviewReply, error) {
	cfg, err := state.LoadConfig()
	if err != nil {
		return host.ReviewReply{}, err
	}
	runtime, err := localNode(cfg)
	if err != nil {
		return host.ReviewReply{}, err
	}
	r.Token = os.Getenv("FACTORY_PANE_TOKEN")
	r.Cwd, _ = os.Getwd()
	op := ""
	if r.Action != "threads" && r.Action != "status" {
		op = uuid.NewString()
	}
	var answer host.ReviewReply
	return answer, runtime.Request(context.Background(), "review", op, r, &answer)
}
