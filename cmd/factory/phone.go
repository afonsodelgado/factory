package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/afonsodelgado/factory/internal/phone"
	"github.com/afonsodelgado/factory/internal/state"
)

const serveReady = 10 * time.Second

const phoneUsage = "usage: factory phone start | stop | status | enable | disable | configure [--port n] [--hostname name] | pair | devices | revoke <device id> | test"

func runPhone(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, phoneUsage)
		return 2
	}
	cfg, err := state.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory phone:", err)
		return 1
	}
	setup := phoneSetup(cfg)
	switch args[0] {
	case "start":
		err = setup.Start()
	case "stop":
		err = setup.Stop()
	case "status":
		err = printPhoneStatus(os.Stdout, setup)
	case "enable":
		err = setup.Enable()
	case "disable":
		err = setup.Disable()
	case "configure":
		err = configurePhone(setup, args[1:])
	case "pair":
		err = printPairing(os.Stdout, setup)
	case "devices":
		err = printDevices(os.Stdout, setup)
	case "test":
		err = printTestPush(os.Stdout, setup)
	case "revoke":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, phoneUsage)
			return 2
		}
		err = setup.Revoke(args[1])
	default:
		fmt.Fprintln(os.Stderr, phoneUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "factory phone:", err)
		return 1
	}
	return 0
}

func printTestPush(w io.Writer, setup phone.Setup) error {
	results, err := setup.Test()
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "No phone has turned on notifications yet.")
		return nil
	}
	names := map[string]string{}
	if devices, err := setup.Devices(); err == nil {
		for _, d := range devices {
			names[d.ID] = d.Name
		}
	}
	for _, r := range results {
		outcome := "accepted by the push service; check that the phone shows it"
		if !r.Accepted {
			outcome = r.Problem
		}
		fmt.Fprintf(w, "%s: %s\n", cmp.Or(names[r.Device], r.Device), outcome)
	}
	return nil
}

func phoneSetup(cfg *state.Config) phone.Setup {
	return phone.Setup{Config: cfg, Start: func() error { return startServe(cfg) }}
}

func configurePhone(setup phone.Setup, args []string) error {
	flags := flag.NewFlagSet("phone configure", flag.ContinueOnError)
	port := flags.Int("port", 0, "loopback port that tailscale serve forwards to")
	hostname := flags.String("hostname", "", "this machine's full Tailscale name, such as devbox.tail1234.ts.net")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *port == 0 && *hostname == "" {
		return errors.New("give --port, --hostname or both")
	}
	return setup.Configure(*port, *hostname)
}

func printPhoneStatus(w io.Writer, setup phone.Setup) error {
	status, err := setup.Status()
	if err != nil {
		return err
	}
	s := status.Settings
	fmt.Fprintf(w, "enabled   %v\nurl       %s\nport      127.0.0.1:%d\n", s.Enabled, s.Origin(), s.ListenPort())
	switch {
	case status.Serving != nil && status.Serving.Owner != nil:
		fmt.Fprintf(w, "serving   yes, version %s; %s\n", status.Serving.Version, ownerPhrase(status.Serving.Owner.Mode))
	case status.Serving != nil:
		fmt.Fprintf(w, "serving   yes, version %s; no runtime yet\n", status.Serving.Version)
	default:
		fmt.Fprintln(w, "serving   no")
	}
	fmt.Fprintf(w, "devices   %d\n", len(status.Devices))
	return nil
}

func ownerPhrase(mode string) string {
	switch mode {
	case phone.ModeHub:
		return "the hub runs the factories"
	case phone.ModeLocal:
		return "factory serve runs the factories while the hub is closed"
	}
	return "an incompatible factory holds the runtime; restart it with this version"
}

func printPairing(w io.Writer, setup phone.Setup) error {
	pairing, err := setup.Pair()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Open this on the phone before %s:\n%s\n", pairing.Expires.Local().Format("15:04:05"), pairing.URL)
	return nil
}

func printDevices(w io.Writer, setup phone.Setup) error {
	devices, err := setup.Devices()
	if err != nil {
		return err
	}
	for _, d := range devices {
		fmt.Fprintf(w, "%s  %s  paired %s\n", d.ID, d.Name, d.PairedAt.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

func startServe(cfg *state.Config) error {
	if phone.Running(cfg.StateDir) {
		return nil
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(phone.Open(cfg.StateDir).Dir, "serve.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(bin, "serve")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	deadline := time.Now().Add(serveReady)
	for {
		if controlAnswers(cfg) {
			return nil
		}
		select {
		case <-exited:
			tail, _ := os.ReadFile(logPath)
			lines := strings.Split(strings.TrimSpace(string(tail)), "\n")
			return fmt.Errorf("factory serve stopped at once: %s", lines[len(lines)-1])
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return errors.New("factory serve did not answer within " + serveReady.String() + "; read " + logPath)
		}
	}
}

func controlAnswers(cfg *state.Config) bool {
	status, err := phoneSetup(cfg).Status()
	return err == nil && status.Serving != nil
}
