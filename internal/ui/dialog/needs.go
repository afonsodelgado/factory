package dialog

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

type NodeReadiness struct {
	Unchecked   bool
	PeerVersion string
	HubVersion  string
	RepoMissing string
	Needs       node.RepoReport
}

type probe struct {
	done   bool
	report NodeReadiness
	err    error
}

type probedMsg struct {
	key    string
	report NodeReadiness
	err    error
}

func (m *NewFactoryModel) keyFor(name string) string {
	return name + " " + m.selectedRepo().Path
}

func (m *NewFactoryModel) probeKey() string {
	return m.keyFor(m.node())
}

func (m *NewFactoryModel) currentProbe() (probe, bool) {
	p, started := m.probes[m.probeKey()]
	return p, started
}

func (m *NewFactoryModel) probed(msg probedMsg) tea.Cmd {
	m.probes[msg.key] = probe{done: true, report: msg.report, err: msg.err}
	if m.waiting && msg.key == m.probeKey() {
		m.waiting = false
		return m.start()
	}
	return nil
}

func (m *NewFactoryModel) checking() bool {
	p, started := m.currentProbe()
	return started && !p.done
}

func (m *NewFactoryModel) missing() []node.Check {
	p, _ := m.currentProbe()
	if !p.done || p.err != nil {
		return nil
	}
	return p.report.Needs.Missing()
}

func (m *NewFactoryModel) heldBack() bool {
	return len(m.missing()) > 0 && !m.startAnyway
}

func (m *NewFactoryModel) placeChanged() tea.Cmd {
	m.startAnyway, m.refused, m.waiting = false, false, false
	return m.probeAll()
}

func (m *NewFactoryModel) recheck() tea.Cmd {
	delete(m.probes, m.probeKey())
	m.refused = false
	return m.probeAll()
}

func (m *NewFactoryModel) startAnywayKey() tea.Cmd {
	if len(m.missing()) == 0 {
		return nil
	}
	m.startAnyway, m.refused = true, false
	if m.step == 1 {
		return m.next()
	}
	return m.start()
}

func (m *NewFactoryModel) holdStart() bool {
	switch {
	case m.checking():
		m.waiting = true
	case m.heldBack():
		m.refused = true
	default:
		return false
	}
	return true
}

func (m *NewFactoryModel) drawStartLines(c *theme.Canvas, y, w int) int {
	short, repo, missing := m.placeName(), m.selectedRepo().Name, m.missing()
	switch {
	case m.refused && m.heldBack():
		c.PutMax(1, y+1, fmt.Sprintf("✗ %s is missing %s %s needs, so %s did not start.", short, plural(len(missing), "thing"), repo, m.slug()), theme.FgInk(theme.Red), w)
		return drawChecks(c, y+3, checkRows(missing))
	case m.startAnyway && len(missing) > 0:
		yellow := theme.FgInk(theme.Yellow)
		c.Spans(1, y+1, []theme.Span{{Text: "! ", Ink: yellow.B()}, {Text: fmt.Sprintf("Starts on %s with %s missing: you picked start anyway.", short, plural(len(missing), "thing")), Ink: yellow}})
		return y + 2
	case m.waiting:
		c.PutMax(1, y+1, "◌ checking what "+repo+" needs on "+short+"…", theme.FgInk(theme.Cyan), w)
		return y + 2
	}
	return y
}

func (m *NewFactoryModel) startLinesHeight(w int) int {
	return m.drawStartLines(theme.NewCanvas(m.width, m.height), 0, w)
}

func drawReadiness(c *theme.Canvas, y, w int, short, repo string, r NodeReadiness) int {
	missing := r.Needs.Missing()
	present := presentNeeds(r.Needs.Checks)
	switch {
	case r.Unchecked:
		yellow := theme.FgInk(theme.Yellow)
		y = wrapped(c, 3, y, w-3, "! "+short+" runs factory "+r.PeerVersion+", which cannot report what "+repo+" needs there.", yellow)
		return wrapped(c, 5, y, w-5, "⏎ starts without the check. factory update brings "+short+" to "+r.HubVersion+".", yellow)
	case len(missing) > 0:
		c.PutMax(3, y, fmt.Sprintf("✗ %s is missing %s %s needs. Nothing is installed for you.", short, plural(len(missing), "thing"), repo), theme.FgInk(theme.Red), w-3)
		rows := checkRows(missing)
		if present != "" {
			rows = append(rows, CheckRow{Name: "also there", Mark: node.MarkOK, Detail: present})
		}
		return drawChecks(c, y+2, rows)
	case present != "":
		return wrapped(c, 3, y, w-3, "↔ "+short+" has everything "+repo+" needs: "+present+".", theme.FgInk(theme.Cyan))
	}
	return wrapped(c, 3, y, w-3, "↔ "+short+" has everything "+repo+" needs.", theme.FgInk(theme.Cyan))
}

func presentNeeds(checks []node.Check) string {
	var names []string
	for _, c := range checks {
		if c.Mark != node.MarkFail {
			names = append(names, strings.TrimSpace(c.Name+" "+c.Detail))
		}
	}
	return joinNames(names)
}

func wrapped(c *theme.Canvas, x, y, w int, text string, ink theme.Ink) int {
	for _, l := range theme.Wrap(text, w) {
		c.Put(x, y, l, ink)
		y++
	}
	return y
}

func (n *Nodes) drawNeeds(c *theme.Canvas, y int, row NodeRow, answer NodeAnswer) int {
	short := row.Short()
	if !answer.Snapshot.Supports(node.CanNeeds) {
		return drawChecks(c, y, []CheckRow{{Name: "repo needs", Mark: node.MarkSkip, Detail: short + " runs factory " + answer.Snapshot.Version + ", which cannot report what repos need"}})
	}
	var problems []string
	noted := false
	for _, report := range answer.Needs.Repos {
		if len(report.Checks) == 0 {
			continue
		}
		repo := node.RepoName(report.URL)
		heading := []theme.Span{{Text: "What " + repo + " needs on " + short, Ink: theme.Bright.B()}}
		if !noted {
			heading = append(heading, theme.Span{Text: "   names only; nothing is copied or installed", Ink: theme.Muted})
			noted = true
		}
		c.Spans(1, y, heading)
		missing := report.Missing()
		rows := checkRows(missing)
		if len(missing) == 0 {
			rows = []CheckRow{{Name: "everything", Mark: node.MarkOK, Detail: presentNeeds(report.Checks)}}
		} else {
			problems = append(problems, needsProblem(repo, short, len(missing)))
		}
		y = drawChecks(c, y+1, rows) + 1
	}
	for _, problem := range problems {
		y = paragraph(c, y, problem, theme.Text)
	}
	return y
}

func needsProblem(repo, short string, count int) string {
	them := "them"
	if count == 1 {
		them = "it"
	}
	return fmt.Sprintf("%s for %s. New %s factories on %s wait for %s, unless you pick start anyway in New factory.", plural(count, "problem"), repo, repo, short, them)
}
