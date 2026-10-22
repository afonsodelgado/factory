package factory

import (
	"sort"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

type Step int

const (
	StepPending Step = iota
	StepDone
	StepCurrent
	StepSkipped
	StepFailed
)

func Track(st *state.State) []Step {
	steps := make([]Step, len(state.StageKeys))
	if st == nil {
		return steps
	}
	for i, key := range state.StageKeys {
		switch st.Stages[key].Status {
		case "done":
			steps[i] = StepDone
		case "skipped":
			steps[i] = StepSkipped
		case "failed":
			steps[i] = StepFailed
		default:
			if key == st.Stage {
				steps[i] = StepCurrent
			}
		}
	}
	return steps
}

func TimeInStage(f Factory, now time.Time) (time.Duration, bool) {
	if f.State == nil {
		created := f.CreatedAt()
		return now.Sub(created), !created.IsZero()
	}
	since := stageStart(f.State)
	if since.IsZero() {
		return 0, false
	}
	return now.Sub(since), true
}

func stageStart(st *state.State) time.Time {
	for i := len(st.History) - 1; i >= 0; i-- {
		h := st.History[i]
		if h.Stage == st.Stage && h.Event == "started" && !h.At.IsZero() {
			return h.At.Time
		}
	}
	var earliest time.Time
	for i := len(st.History) - 1; i >= 0 && st.History[i].Stage == st.Stage; i-- {
		if at := st.History[i].At; !at.IsZero() {
			earliest = at.Time
		}
	}
	return earliest
}

func Activity(f Factory, logged []state.ActivityEntry) []state.ActivityEntry {
	var all []state.ActivityEntry
	if f.State != nil {
		for i := len(f.State.History) - 1; i >= 0; i-- {
			h := f.State.History[i]
			text := h.Event
			if h.Detail != "" {
				text += " · " + h.Detail
			}
			all = append(all, state.ActivityEntry{At: h.At.Time, Source: "state", Stage: h.Stage, Text: text})
		}
	}
	for i := len(logged) - 1; i >= 0; i-- {
		all = append(all, logged[i])
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].At.After(all[j].At)
	})
	return all
}
