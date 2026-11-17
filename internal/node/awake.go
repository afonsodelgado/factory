package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/afonsodelgado/factory/internal/factory"
	"github.com/afonsodelgado/factory/internal/host"
)

const awakeEvery = 15 * time.Second

type Assertion struct {
	Release func()
	Ended   <-chan struct{}
}

type AwakeStatus struct {
	Detail  string `json:"detail"`
	Problem bool   `json:"problem,omitempty"`
}

type Awake struct {
	Enabled     bool
	Unsupported string
	OnPower     func(context.Context) (bool, error)
	Hold        func() (Assertion, error)

	mu     sync.Mutex
	held   *Assertion
	status AwakeStatus
}

func ActiveFactories(factories []host.LocalFactory) int {
	active := 0
	for _, f := range factories {
		if !f.Paused && f.Status != factory.Done.String() {
			active++
		}
	}
	return active
}

func (a *Awake) Run(ctx context.Context, active func() int) {
	tick := time.NewTicker(awakeEvery)
	defer tick.Stop()
	for {
		a.Set(ctx, active())
		select {
		case <-ctx.Done():
			a.release()
			return
		case <-tick.C:
		}
	}
}

func (a *Awake) Set(ctx context.Context, active int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	status, holding := a.decide(ctx, active)
	if !holding {
		a.releaseLocked()
	}
	a.status = status
}

func (a *Awake) decide(ctx context.Context, active int) (AwakeStatus, bool) {
	switch {
	case a.Unsupported != "":
		return AwakeStatus{Detail: a.Unsupported}, false
	case !a.Enabled:
		return AwakeStatus{Detail: "off, because node.keep_awake is false"}, false
	case active == 0:
		return AwakeStatus{Detail: "not holding: no active factories"}, false
	}
	onPower, err := a.OnPower(ctx)
	switch {
	case err != nil:
		return AwakeStatus{Detail: "not holding: could not read the power source: " + err.Error(), Problem: true}, false
	case !onPower:
		return AwakeStatus{Detail: "not holding: on battery"}, false
	}
	if err := a.holdLocked(); err != nil {
		return AwakeStatus{Detail: "not holding: " + err.Error(), Problem: true}, false
	}
	running := "active factories run"
	if active == 1 {
		running = "active factory runs"
	}
	return AwakeStatus{Detail: fmt.Sprintf("holding while %d %s on external power", active, running)}, true
}

func (a *Awake) holdLocked() error {
	if a.held != nil {
		select {
		case <-a.held.Ended:
			a.held = nil
		default:
			return nil
		}
	}
	held, err := a.Hold()
	if err != nil {
		return err
	}
	a.held = &held
	return nil
}

func (a *Awake) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.releaseLocked()
}

func (a *Awake) releaseLocked() {
	if a.held != nil {
		a.held.Release()
		a.held = nil
	}
}

func (a *Awake) Status() AwakeStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}
