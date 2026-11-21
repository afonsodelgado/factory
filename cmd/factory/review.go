package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/host"
	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
	"github.com/google/uuid"
)

const reviewUsage = `usage: factory review reply --thread <id> [--resolve] [--factory id] [text…]
       factory review threads [--page <path>] [--open] [--factory id]
       factory review open [--factory <id>] [--for design] <url>
       factory review stack [--for design]
       factory review link <url>
       factory review capture [--factory id] --source <html in the factory directory | prototype> --screen <id>... [--viewport WxH]... [--entry <module> --route <path>]`

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
	case "capture":
		return runReviewCapture(args[1:])
	}
	fmt.Fprintln(os.Stderr, reviewUsage)
	return 2
}

func runReviewReply(args []string) int {
	flags := flag.NewFlagSet("review reply", flag.ContinueOnError)
	thread := flags.String("thread", "", "the thread id, such as t-3")
	resolve := flags.Bool("resolve", false, "mark the thread resolved")
	id := factoryFlag(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	text := strings.Join(flags.Args(), " ")
	if *thread == "" || (text == "" && !*resolve) {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	if _, err := askReview(host.ReviewRequest{Action: "reply", ID: *id, Thread: *thread, Text: text, Resolve: *resolve}); err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	return 0
}

func runReviewThreads(args []string) int {
	flags := flag.NewFlagSet("review threads", flag.ContinueOnError)
	page := flags.String("page", "", "only the threads on this page, relative to the factory directory")
	open := flags.Bool("open", false, "only the threads that are not resolved")
	id := factoryFlag(flags)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	reply, err := askReview(host.ReviewRequest{Action: "threads", ID: *id, Page: *page})
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
	stage := flags.String("for", "design", "the design stage receives comments")
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
	fmt.Printf("Open %s in your browser to review it; its comments come back here.\n", flags.Arg(0))
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
	fmt.Printf("Open %s in your browser.\n", args[0])
	return 0
}

var livePages = map[string]string{"design": review.Prototype}

func runReviewStack(args []string) int {
	flags := flag.NewFlagSet("review stack", flag.ContinueOnError)
	stage := flags.String("for", "design", "the design stage receives comments")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	page, ok := livePages[*stage]
	if !ok {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	reply, err := askReview(host.ReviewRequest{Action: "stack", Page: page})
	for err == nil && reply.Stack.State == host.StackStarting {
		time.Sleep(stackPoll)
		reply, err = askReview(host.ReviewRequest{Action: "stack-status"})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory review:", err)
		return 1
	}
	if s := reply.Stack; s.State == host.StackFailed {
		fmt.Fprintf(os.Stderr, "factory review: the review stack failed at %s: %s\n", s.Failure, s.Reason)
		return 1
	}
	if link, ok := localReviewLink(reply.Stack.Href); ok {
		fmt.Printf("The review stack runs at %s, on this machine only. This link opens it signed in, once, within 2 minutes; factory review stack prints a new one:\n  %s\nComments made on it come back here.\n", reply.Stack.URL, link)
		return 0
	}
	fmt.Printf("The review stack runs at %s, for your tailnet only. The factory's Pages tab in the phone app opens it signed in; comments made on it come back here.\n", reply.Stack.URL)
	return 0
}

func localReviewLink(path string) (string, bool) {
	cfg, err := state.LoadConfig()
	if err != nil || cfg.TailscaleNode() {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodeWait)
	defer cancel()
	var link node.ReviewLink
	err = node.Unix(cfg.NodeSocket()).Do(ctx, http.MethodPost, "/api/review/links", node.ReviewLink{Path: path}, &link)
	return link.URL, err == nil
}

const stackPoll = time.Second

func runReviewCapture(args []string) int {
	flags := flag.NewFlagSet("review capture", flag.ContinueOnError)
	id := factoryFlag(flags)
	source := flags.String("source", "", "the approved HTML file, relative to the factory directory, or prototype")
	entry := flags.String("entry", "", "the prototype's entry module, relative to the worktree")
	route := flags.String("route", "", "the prototype page's path on the app, such as /prototypes/<id>/variant-a.html")
	var screens, sizes keys
	flags.Var(&screens, "screen", "the id of a screen or state section; repeat for each")
	flags.Var(&sizes, "viewport", "an extra viewport such as 1280x800, besides 1440x900, 1024x768 and 390x844; repeat for each")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	prototype := *source == review.Prototype
	if *source == "" || len(screens) == 0 || flags.NArg() > 0 || prototype != (*entry != "" && *route != "") {
		fmt.Fprintln(os.Stderr, reviewUsage)
		return 2
	}
	viewports := slices.Clone(review.DefaultViewports)
	for _, size := range sizes {
		v, err := review.ParseViewport(size)
		if err != nil {
			fmt.Fprintln(os.Stderr, "factory review capture:", err)
			return 2
		}
		viewports = append(viewports, v)
	}
	request := review.CaptureRequest{Source: *source, Entry: *entry, URL: *route, Screens: screens, Viewports: viewports}
	reply, err := askReview(host.ReviewRequest{Action: "capture", ID: *id, Capture: &request})
	for err == nil && reply.Capture.State == host.CaptureRunning {
		time.Sleep(stackPoll)
		reply, err = askReview(host.ReviewRequest{Action: "capture-status", ID: *id})
	}
	if err == nil && reply.Capture.State == host.CaptureFailed {
		err = errors.New(reply.Capture.Reason)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory review capture:", err)
		return 1
	}
	fmt.Printf("sha256 %s  %s\n", reply.Capture.Result.SHA256, reply.Capture.Result.Frozen)
	for _, file := range reply.Capture.Result.Files {
		fmt.Println(file)
	}
	return 0
}

func factoryFlag(flags *flag.FlagSet) *string {
	return flags.String("factory", "", "the factory, when this is not one of its panes")
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
	if !slices.Contains([]string{"threads", "status", "stack-status", "capture-status"}, r.Action) {
		op = uuid.NewString()
	}
	var answer host.ReviewReply
	return answer, runtime.Request(context.Background(), "review", op, r, &answer)
}
