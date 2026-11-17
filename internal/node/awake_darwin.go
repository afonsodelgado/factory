package node

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func NewAwake(enabled bool) *Awake {
	return &Awake{Enabled: enabled, OnPower: onExternalPower, Hold: caffeinate}
}

func onExternalPower(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "pmset", "-g", "ps")
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	source, _, _ := bytes.Cut(out, []byte("\n"))
	return bytes.Contains(source, []byte("'AC Power'")), nil
}

func caffeinate() (Assertion, error) {
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return Assertion{}, err
	}
	ended := make(chan struct{})
	go func() {
		cmd.Wait()
		close(ended)
	}()
	release := func() {
		cmd.Process.Kill()
		<-ended
	}
	return Assertion{Release: release, Ended: ended}, nil
}
