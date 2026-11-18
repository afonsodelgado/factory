package dialog

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/node"
	"github.com/afonsodelgado/factory/internal/ui/theme"
)

const (
	nodeX      = 3
	nodeStateX = 17
	versionX   = 36
	countX     = 45
	defaultX   = 66
	fieldX     = 15
	pickOSX    = 16
	pickNodeX  = 24
	askWait    = node.ProbeWait
	checkWait  = node.ReadyWait + 2*node.ProbeWait
)

type NodeRow struct {
	ID        string
	Name      string
	OS        string
	Version   string
	Factories int
	NeedsYou  int
	Contact   time.Time
	Since     time.Time
}

func (r NodeRow) Short() string { return shortName(r.Name) }

func shortName(name string) string {
	short, _, _ := strings.Cut(name, ".")
	return short
}

type RepoURL struct {
	Name string
	URL  string
}

type NodesContext struct {
	Machine   string
	Self      string
	Offer     string
	Tailnet   string
	Version   string
	Factories int
	NeedsYou  int
	Phone     PhoneContext
	Home      string
	Owner     string
	Target    string
	Leaving   []node.RosterMember
	Others    []NodeRow
	Repos     []RepoURL
}

type NodeAnswer struct {
	Snapshot node.Snapshot
	Report   node.Report
}

type NodeActions interface {
	PhoneActions
	Ask(ctx context.Context, name string, repos []string) (NodeAnswer, error)
	Seen(ctx context.Context, name string) (node.Peer, error)
	Machines(ctx context.Context) (node.Inventory, error)
	Candidates(ctx context.Context) ([]node.Candidate, error)
	Inspect(ctx context.Context, name string) (node.CheckReport, error)
	Add(ctx context.Context, op, name string, replaceKey bool) (node.AddResult, error)
	Remove(op, id string) (node.Roster, error)
	MoveHome(op, id string) (node.Roster, error)
	SetTarget(name string) error
	Clone(ctx context.Context, name, op, url string) error
	MoveAll(ctx context.Context, op, target string) (node.BatchProgress, error)
	Batch(ctx context.Context, op string) (node.BatchProgress, error)
	CancelAll(ctx context.Context, op string) (node.BatchProgress, error)
}

type nodesScreen int

const (
	nodesListScreen nodesScreen = iota
	selfDetailsScreen
	peerDetailsScreen
	pickScreen
	typeScreen
	checkScreen
	moveAllScreen
)

type peerState struct {
	asking   bool
	answered bool
	answer   NodeAnswer
	err      error
	seen     *node.Peer
	seenErr  error
	cloning  bool
	cloneErr error
}

type nodeAsked struct {
	id     string
	answer NodeAnswer
	err    error
}

type nodeSeen struct {
	id   string
	peer node.Peer
	err  error
}

type machinesListed struct {
	run int
	inv node.Inventory
	err error
}

type candidatesListed struct {
	run  int
	rows []node.Candidate
	err  error
}

type nodeInspected struct {
	run    int
	report node.CheckReport
	err    error
}

type nodeAdded struct {
	run         int
	result      node.AddResult
	err         error
	makeDefault bool
}

type nodeCloned struct {
	id  string
	err error
}

type batchArrived struct {
	batch node.BatchProgress
	err   error
}

type batchTick struct{}

type Nodes struct {
	Context NodesContext
	Now     func() time.Time
	actions NodeActions
	size
	screen   nodesScreen
	cursor   int
	scroll   int
	removing bool
	problem  string
	added    *node.AddResult

	checkRun int
	checks   []CheckRow
	peers    map[string]*peerState

	run        int
	cancel     context.CancelFunc
	inventory  *node.Inventory
	candidates []node.Candidate
	listed     bool
	listErr    error
	pick       int
	typed      string
	fromTyped  bool
	checking   string
	report     *node.CheckReport
	reportErr  error
	operation  string
	adding     bool

	confirmingMove bool
	batch          node.BatchProgress

	homing    *homeChoice
	movedFrom string
}

func NewNodes(c NodesContext, actions NodeActions) *Nodes {
	return &Nodes{Context: c, actions: actions, peers: map[string]*peerState{}}
}

func (n *Nodes) now() time.Time {
	if n.Now == nil {
		return time.Now()
	}
	return n.Now()
}

func (n *Nodes) Init() tea.Cmd { return n.askAll() }

func (n *Nodes) Result() (any, bool) { return nil, false }

func (n *Nodes) peer(id string) *peerState {
	p, ok := n.peers[id]
	if !ok {
		p = &peerState{}
		n.peers[id] = p
	}
	return p
}

func (n *Nodes) repoURLs() []string {
	var urls []string
	for _, r := range n.Context.Repos {
		urls = append(urls, r.URL)
	}
	return urls
}

func (n *Nodes) askAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, row := range n.Context.Others {
		cmds = append(cmds, n.ask(row))
	}
	return tea.Batch(cmds...)
}

func (n *Nodes) ask(row NodeRow) tea.Cmd {
	p := n.peer(row.ID)
	if p.asking {
		return nil
	}
	p.asking = true
	actions, repos := n.actions, n.repoURLs()
	return func() tea.Msg {
		answer, err := actions.Ask(context.Background(), row.Name, repos)
		return nodeAsked{id: row.ID, answer: answer, err: err}
	}
}

func (n *Nodes) see(row NodeRow) tea.Cmd {
	actions := n.actions
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), askWait)
		defer cancel()
		peer, err := actions.Seen(ctx, row.Name)
		return nodeSeen{id: row.ID, peer: peer, err: err}
	}
}

func (n *Nodes) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	n.resize(msg)
	switch msg := msg.(type) {
	case phoneChecks:
		if msg.run == n.checkRun && n.screen == selfDetailsScreen {
			n.checks = msg.rows
		}
	case nodeAsked:
		p := n.peer(msg.id)
		p.asking, p.err = false, msg.err
		if msg.err == nil {
			p.answered, p.answer = true, msg.answer
		}
	case nodeSeen:
		p := n.peer(msg.id)
		p.seen, p.seenErr = &msg.peer, msg.err
	case nodeCloned:
		p := n.peer(msg.id)
		p.cloning, p.cloneErr = false, msg.err
		if msg.err == nil {
			return n, n.reask(msg.id)
		}
	case machinesListed:
		if msg.run == n.run && msg.err == nil && !n.listed {
			n.inventory = &msg.inv
		}
	case candidatesListed:
		if msg.run == n.run {
			n.candidates, n.listErr, n.listed = msg.rows, msg.err, true
			n.pick = n.firstSelectable()
		}
	case nodeInspected:
		if msg.run == n.run && n.screen == checkScreen {
			n.report, n.reportErr = &msg.report, msg.err
		}
	case nodeAdded:
		return n, n.addedNode(msg)
	case batchArrived:
		return n, n.batchArrived(msg)
	case batchTick:
		return n, n.pollBatch()
	case tea.PasteMsg:
		if n.screen == typeScreen {
			n.typed += strings.ReplaceAll(DecodeKeys(msg.Content), "\n", "")
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			n.stop()
			return n, tea.Quit
		}
		n.problem = ""
		return n, n.key(msg)
	}
	return n, nil
}

func (n *Nodes) reask(id string) tea.Cmd {
	for _, row := range n.Context.Others {
		if row.ID == id {
			return n.ask(row)
		}
	}
	return nil
}

func (n *Nodes) stop() {
	if n.cancel != nil {
		n.cancel()
		n.cancel = nil
	}
	n.run++
}

func (n *Nodes) start() (context.Context, int) {
	n.stop()
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	return ctx, n.run
}

func (n *Nodes) key(msg tea.KeyPressMsg) tea.Cmd {
	if n.adding {
		return nil
	}
	key := msg.String()
	if n.homing != nil {
		n.homeKey(key)
		return nil
	}
	switch n.screen {
	case selfDetailsScreen:
		return n.selfDetailsKey(key)
	case peerDetailsScreen:
		return n.peerDetailsKey(key)
	case pickScreen:
		return n.pickKey(key)
	case typeScreen:
		return n.typeKey(msg)
	case checkScreen:
		return n.checkKey(key)
	case moveAllScreen:
		return n.moveAllKey(key)
	}
	return n.listKey(key)
}

func (n *Nodes) selected() (NodeRow, bool) {
	if n.cursor == 0 || n.cursor > len(n.Context.Others) {
		return NodeRow{}, false
	}
	return n.Context.Others[n.cursor-1], true
}

func (n *Nodes) listKey(key string) tea.Cmd {
	if n.removing {
		n.removing = false
		if key == "y" {
			n.remove()
		}
		return nil
	}
	row, onPeer := n.selected()
	switch key {
	case "esc":
		return tea.Quit
	case "up", "k":
		n.cursor = max(n.cursor-1, 0)
	case "down", "j":
		n.cursor = min(n.cursor+1, len(n.Context.Others))
	case "enter":
		return n.openDetails()
	case "a":
		if !n.Context.Phone.Tailscale {
			return nil
		}
		return n.openPick()
	case "d":
		n.makeDefault(row.Name)
	case "h":
		n.askHome()
	case "x":
		n.askRemoval(onPeer)
	case "r":
		return n.askAll()
	}
	return nil
}

func (n *Nodes) askRemoval(onPeer bool) {
	if !onPeer {
		n.problem = n.Context.Machine + " is this machine. Removing it from the list does not stop it; to stop this node, run factory node uninstall here."
		return
	}
	n.removing = true
}

func (n *Nodes) remove() {
	row, _ := n.selected()
	roster, err := n.actions.Remove(uuid.NewString(), row.ID)
	if err != nil {
		n.problem = err.Error()
		return
	}
	n.Context.Leaving = roster.Leaving
	n.Context.Others = slices.Delete(n.Context.Others, n.cursor-1, n.cursor)
	if n.Context.Target == row.Name {
		n.Context.Target = ""
	}
	n.cursor = min(n.cursor, len(n.Context.Others))
	n.screen = nodesListScreen
}

func (n *Nodes) makeDefault(name string) bool {
	if err := n.actions.SetTarget(name); err != nil {
		n.problem = err.Error()
		return false
	}
	n.Context.Target = name
	return true
}

func (n *Nodes) target() (NodeRow, bool) {
	for _, row := range n.Context.Others {
		if row.Name == n.Context.Target {
			return row, true
		}
	}
	return NodeRow{}, false
}

func (n *Nodes) openDetails() tea.Cmd {
	n.scroll = 0
	row, onPeer := n.selected()
	if !onPeer {
		n.screen, n.checks = selfDetailsScreen, nil
		n.checkRun++
		return runChecks(n.actions, n.checkRun)
	}
	n.screen = peerDetailsScreen
	return tea.Batch(n.ask(row), n.see(row))
}

func (n *Nodes) selfDetailsKey(key string) tea.Cmd {
	switch key {
	case "esc":
		n.screen = nodesListScreen
		n.checkRun++
	case "r":
		n.checks = nil
		n.checkRun++
		return runChecks(n.actions, n.checkRun)
	case "h":
		n.askHome()
	default:
		n.scrollKey(key)
	}
	return nil
}

func (n *Nodes) scrollKey(key string) {
	switch key {
	case "j", "down":
		n.scroll = min(n.scroll+1, n.maxScroll())
	case "k", "up":
		n.scroll = max(n.scroll-1, 0)
	}
}

func (n *Nodes) peerDetailsKey(key string) tea.Cmd {
	row, _ := n.selected()
	if n.confirmingMove {
		n.confirmingMove = false
		if key == "y" {
			return n.moveAll(row)
		}
		return nil
	}
	switch key {
	case "m":
		n.confirmingMove = n.takesFactories(row)
	case "esc":
		n.screen = nodesListScreen
	case "r":
		return tea.Batch(n.ask(row), n.see(row))
	case "c":
		return n.clone(row)
	case "d":
		n.makeDefault(row.Name)
	case "h":
		n.askHome()
	case "x":
		n.screen, n.removing = nodesListScreen, true
	default:
		n.scrollKey(key)
	}
	return nil
}

func (n *Nodes) uncloned(row NodeRow) []RepoURL {
	p := n.peer(row.ID)
	if !p.answered {
		return nil
	}
	var missing []RepoURL
	for _, c := range p.answer.Report.Checks {
		for _, r := range n.Context.Repos {
			if c.Name == "repos" && strings.Contains(c.Detail, r.Name+" not cloned") {
				missing = append(missing, r)
			}
		}
	}
	return missing
}

func (n *Nodes) clone(row NodeRow) tea.Cmd {
	missing := n.uncloned(row)
	p := n.peer(row.ID)
	if len(missing) == 0 || p.cloning {
		return nil
	}
	p.cloning, p.cloneErr = true, nil
	actions := n.actions
	return func() tea.Msg {
		for _, r := range missing {
			if err := actions.Clone(context.Background(), row.Name, uuid.NewString(), r.URL); err != nil {
				return nodeCloned{id: row.ID, err: err}
			}
		}
		return nodeCloned{id: row.ID}
	}
}

func (n *Nodes) openPick() tea.Cmd {
	ctx, run := n.start()
	n.screen, n.listed, n.candidates, n.listErr, n.inventory, n.pick, n.scroll = pickScreen, false, nil, nil, nil, -1, 0
	actions := n.actions
	return tea.Batch(
		func() tea.Msg {
			listCtx, cancel := context.WithTimeout(ctx, askWait)
			defer cancel()
			inv, err := actions.Machines(listCtx)
			return machinesListed{run: run, inv: inv, err: err}
		},
		func() tea.Msg {
			rows, err := actions.Candidates(ctx)
			return candidatesListed{run: run, rows: rows, err: err}
		},
	)
}

func (n *Nodes) selectable() []int {
	var rows []int
	for i, c := range n.candidates {
		if c.Selectable {
			rows = append(rows, i)
		}
	}
	return rows
}

func (n *Nodes) firstSelectable() int {
	if rows := n.selectable(); len(rows) > 0 {
		return rows[0]
	}
	return -1
}

func (n *Nodes) movePick(delta int) {
	rows := n.selectable()
	if len(rows) == 0 {
		return
	}
	i := slices.Index(rows, n.pick) + delta
	n.pick = rows[max(0, min(i, len(rows)-1))]
}

func (n *Nodes) picked() (node.Candidate, bool) {
	if n.pick < 0 || n.pick >= len(n.candidates) {
		return node.Candidate{}, false
	}
	return n.candidates[n.pick], true
}

func (n *Nodes) pickKey(key string) tea.Cmd {
	switch key {
	case "esc":
		n.stop()
		n.screen = nodesListScreen
	case "up", "k":
		n.movePick(-1)
	case "down", "j":
		n.movePick(1)
	case "t":
		n.screen = typeScreen
	case "enter":
		if c, ok := n.picked(); ok {
			n.fromTyped = false
			return n.inspect(c.Name)
		}
	}
	return nil
}

func (n *Nodes) typeKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "tab":
		n.screen = pickScreen
	case "enter":
		if typed := strings.TrimSpace(n.typed); typed != "" {
			n.fromTyped = true
			return n.inspect(typed)
		}
	case "backspace":
		n.typed = dropLast(n.typed)
	case "ctrl+u":
		n.typed = ""
	default:
		n.typed += DecodeKeys(msg.Text)
	}
	return nil
}

func (n *Nodes) inspect(name string) tea.Cmd {
	ctx, run := n.start()
	n.screen, n.checking, n.report, n.reportErr, n.operation, n.scroll = checkScreen, name, nil, nil, uuid.NewString(), 0
	actions := n.actions
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, checkWait)
		defer cancel()
		report, err := actions.Inspect(ctx, name)
		return nodeInspected{run: run, report: report, err: err}
	}
}

func (n *Nodes) refused() bool {
	return n.reportErr != nil || (n.report != nil && n.report.Refusal != "")
}

func (n *Nodes) addable() bool { return n.report != nil && !n.refused() }

func (n *Nodes) checkKey(key string) tea.Cmd {
	checked := n.report != nil || n.reportErr != nil
	switch {
	case key == "esc":
		n.stop()
		n.screen = n.origin()
	case !checked:
	case key == "tab" && n.reportErr != nil:
		n.screen = typeScreen
	case key == "r":
		return n.inspect(n.checking)
	case key == "enter" && n.addable():
		return n.add(false, false)
	case key == "p" && n.addable() && n.report.PushConflict:
		return n.add(false, true)
	case key == "d" && n.addable() && n.report.Problems == 0:
		return n.add(true, false)
	default:
		n.scrollKey(key)
	}
	return nil
}

func (n *Nodes) origin() nodesScreen {
	if n.fromTyped {
		return typeScreen
	}
	return pickScreen
}

func (n *Nodes) add(makeDefault, replaceKey bool) tea.Cmd {
	ctx, run := n.start()
	n.adding = true
	actions, op, name := n.actions, n.operation, n.checking
	return func() tea.Msg {
		result, err := actions.Add(ctx, op, name, replaceKey)
		return nodeAdded{run: run, result: result, err: err, makeDefault: makeDefault}
	}
}

func (n *Nodes) addedNode(msg nodeAdded) tea.Cmd {
	if msg.run != n.run {
		return nil
	}
	n.adding = false
	if msg.err != nil {
		if len(msg.result.Report.Checks) > 0 {
			n.report = &msg.result.Report
		}
		n.problem = msg.err.Error()
		return nil
	}
	m := msg.result.Member
	row := NodeRow{ID: m.ID, Name: m.Name, OS: m.OS, Since: n.now()}
	if !slices.ContainsFunc(n.Context.Others, func(r NodeRow) bool { return r.ID == m.ID }) {
		n.Context.Others = append(n.Context.Others, row)
	}
	n.cursor = slices.IndexFunc(n.Context.Others, func(r NodeRow) bool { return r.ID == m.ID }) + 1
	n.added = &msg.result
	n.screen = nodesListScreen
	if msg.makeDefault {
		n.makeDefault(m.Name)
	}
	return n.ask(row)
}

func (n *Nodes) View() tea.View {
	c := theme.NewCanvas(n.width, n.height)
	switch n.screen {
	case moveAllScreen:
		n.drawMoveAll(c)
	case selfDetailsScreen, peerDetailsScreen, checkScreen:
		n.drawScrolled(c)
	case pickScreen:
		n.drawPick(c)
	case typeScreen:
		n.drawType(c)
	default:
		n.drawList(c)
	}
	return view(c)
}

func (n *Nodes) domain() string {
	if n.Context.Tailnet != "" {
		return n.Context.Tailnet
	}
	return "your tailnet"
}

type nodeLook struct {
	state, version, factories string
	stateInk                  theme.Ink
	lost                      bool
	answered                  bool
	needs                     int
	asOf                      time.Time
}

func (n *Nodes) look(row NodeRow) nodeLook {
	p := n.peer(row.ID)
	switch {
	case p.asking:
		return nodeLook{state: "◌ asking…", stateInk: theme.FgInk(theme.Cyan), version: "–", factories: "–"}
	case p.answered && p.err == nil:
		snap := p.answer.Snapshot
		l := nodeLook{state: "● running", stateInk: theme.FgInk(theme.Green), version: snap.Version, factories: fmt.Sprint(len(snap.Factories)), answered: true}
		for _, f := range snap.Factories {
			if !f.NeedsSince.IsZero() {
				l.needs++
			}
		}
		if problems := p.answer.Report.Problems; problems > 0 {
			l.state, l.stateInk = fmt.Sprintf("! %d to fix", problems), theme.FgInk(theme.Yellow)
		}
		return l
	}
	l := nodeLook{state: "↮ unreachable " + theme.Age(n.lostFor(row)), stateInk: theme.FgInk(theme.Red), version: cmp.Or(row.Version, "–"), factories: "–", lost: true, asOf: row.Contact}
	if !row.Contact.IsZero() {
		l.factories, l.needs = fmt.Sprint(row.Factories), row.NeedsYou
	}
	return l
}

func (n *Nodes) lostFor(row NodeRow) time.Duration {
	since := row.Contact
	if since.IsZero() {
		since = row.Since
	}
	if since.IsZero() {
		return 0
	}
	return n.now().Sub(since)
}

func (n *Nodes) reachable() (reachable, asking int) {
	reachable = 1
	for _, row := range n.Context.Others {
		switch l := n.look(row); {
		case l.answered:
			reachable++
		case !l.lost:
			asking++
		}
	}
	return reachable, asking
}

func (n *Nodes) drawList(c *theme.Canvas) {
	c.Clip(0, c.H-1)
	defer c.Unclip()
	c.Put(1, 0, "Nodes", theme.Bright.B())
	title := n.Context.Tailnet
	if len(n.Context.Others) > 0 {
		reachable, asking := n.reachable()
		title = fmt.Sprintf("%s · %d of %d reachable", n.domain(), reachable, len(n.Context.Others)+1)
		if asking > 0 {
			title = fmt.Sprintf("%s · asking %d %s…", n.domain(), asking, nounFor(asking, "node"))
		}
	}
	if !n.Context.Phone.Tailscale {
		title = "this machine only"
	}
	c.Right(c.W-2, 0, title, theme.Muted)
	newOnesX := min(defaultX, c.W-1-theme.Width("● default"))
	for x, heading := range map[int]string{nodeX: "node", nodeStateX: "state", versionX: "version", countX: "factories", newOnesX: "new ones"} {
		c.Put(x, 2, heading, theme.Muted)
	}
	n.drawSelfRow(c, 3, newOnesX)
	for i, row := range n.Context.Others {
		n.drawPeerRow(c, 4+i, i+1, row, newOnesX)
	}
	switch {
	case !n.Context.Phone.Tailscale:
		n.drawWithoutTailscale(c)
		return
	case len(n.Context.Others) == 0:
		n.drawAlone(c)
		return
	}
	n.drawMany(c, 5+len(n.Context.Others))
}

func nounFor(count int, noun string) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}

func (n *Nodes) drawCursor(c *theme.Canvas, y, i int) {
	if i == n.cursor {
		c.Fill(0, y, c.W, 1, theme.Selection)
		c.Put(1, y, "❯", theme.Focused.B())
	}
}

func (n *Nodes) drawSelfRow(c *theme.Canvas, y, newOnesX int) {
	n.drawCursor(c, y, 0)
	c.PutMax(nodeX, y, n.Context.Machine, theme.Bright, nodeStateX-nodeX-1)
	state, stateInk := n.state()
	c.Put(nodeStateX, y, state, stateInk)
	c.PutMax(versionX, y, n.version(), theme.Text, countX-versionX-1)
	n.drawFactories(c, countX, y, "0")
	_, elsewhere := n.target()
	n.drawDefaultMark(c, y, newOnesX, !elsewhere)
}

func (n *Nodes) drawDefaultMark(c *theme.Canvas, y, x int, isDefault bool) {
	if isDefault {
		c.Put(x, y, "● default", theme.FgInk(theme.Green))
		return
	}
	c.Put(x, y, "○", theme.Muted)
}

func (n *Nodes) drawPeerRow(c *theme.Canvas, y, i int, row NodeRow, newOnesX int) {
	n.drawCursor(c, y, i)
	l := n.look(row)
	c.PutMax(nodeX, y, row.Short(), theme.Bright, nodeStateX-nodeX-1)
	c.Put(nodeStateX, y, l.state, l.stateInk)
	valueInk := func(value string) theme.Ink {
		switch {
		case l.lost:
			return theme.Quiet
		case value == "–":
			return theme.Muted
		}
		return theme.Text
	}
	c.PutMax(versionX, y, l.version, valueInk(l.version), countX-versionX-1)
	end := c.Put(countX, y, l.factories, valueInk(l.factories))
	if l.needs > 0 {
		end = c.Put(end, y, fmt.Sprintf(" · %d needs you", l.needs), theme.FgInk(theme.Orange))
	}
	if l.lost && !l.asOf.IsZero() {
		c.PutMax(end, y, ", as of "+clock(l.asOf), theme.Muted, newOnesX-end-1)
	}
	n.drawDefaultMark(c, y, newOnesX, row.Name == n.Context.Target)
}

func clock(t time.Time) string { return t.Local().Format("15:04") }

func (n *Nodes) drawAlone(c *theme.Canvas) {
	if len(n.Context.Leaving) > 0 {
		y := n.drawLeaving(c, 5)
		y = n.drawPhoneApp(c, y+1, false)
		n.drawProblem(c, y+1)
		n.hint(c, "⏎ details   a add a node   esc close")
		return
	}
	c.Put(1, 5, "No other nodes yet. Every factory runs on this machine.", theme.Text)
	y := paragraph(c, 7, "A node is any machine of yours that ran factory setup or factory node install, such as a home server that stays on. Its factories keep running while this laptop sleeps or travels, and this hub and your phone list them next to these.", theme.Text)
	c.Spans(1, y+1, []theme.Span{{Text: "a", Ink: theme.Key}, {Text: "  add one from the machines on your tailnet", Ink: theme.Text}})
	c.Spans(1, y+2, []theme.Span{{Text: "or, on the other machine:  ", Ink: theme.Muted}, {Text: "factory setup", Ink: theme.Bright}, {Text: "  installs what it needs and joins " + n.Context.Machine, Ink: theme.Muted}})
	y = n.drawPhoneApp(c, y+4, false)
	n.drawProblem(c, y+1)
	n.hint(c, "⏎ details   a add a node   esc close")
}

func (n *Nodes) drawWithoutTailscale(c *theme.Canvas) {
	missing := node.NeedsTailscale(node.NodesFeature, n.Context.Machine)
	y := paragraph(c, 5, "Every factory runs on this machine. "+strings.ToUpper(missing[:1])+missing[1:]+".", theme.Text)
	y = paragraph(c, y+1, "A node is any machine of yours that runs factory, such as a home server that stays on. Its factories keep running while this laptop sleeps, and this hub and your phone list them next to these.", theme.Text)
	y = addTailscale(c, y+1, "installs and logs in Tailscale and serves this node on your tailnet. Every factory, setting and repo here stays as it is.")
	c.Spans(1, y, []theme.Span{{Text: "then, on the other machine:  ", Ink: theme.Muted}, {Text: node.AddTailscale, Ink: theme.Bright}, {Text: "  joins " + n.Context.Machine, Ink: theme.Muted}})
	c.Put(1, y+2, "Phone app", theme.Bright.B())
	c.Put(valueX, y+2, "off: the phone app needs Tailscale too", theme.Muted)
	n.drawProblem(c, y+4)
	n.hint(c, "⏎ details   esc close")
}

func (n *Nodes) targetName() string {
	if row, ok := n.target(); ok {
		return row.Short()
	}
	return n.Context.Machine
}

func (n *Nodes) drawLeaving(c *theme.Canvas, y int) int {
	for _, m := range n.Context.Leaving {
		y = paragraph(c, y, shortName(m.Name)+": removal pending; it learns it was removed when it next answers.", theme.FgInk(theme.Yellow)) + 1
	}
	return y
}

func (n *Nodes) drawMany(c *theme.Canvas, y int) {
	y = n.drawLeaving(c, y)
	y = paragraph(c, y, "New factories run on "+n.targetName()+". Pick another node in New factory for a one-off.", theme.Text) + 1
	y = n.drawHomeNews(c, y)
	if n.homing != nil {
		c.Clip(0, c.H-10)
		n.drawPhoneApp(c, y, true)
		c.Unclip()
		n.drawHomeConfirm(c)
		return
	}
	if n.added != nil {
		y = n.drawAdded(c, y)
	}
	_, asking := n.reachable()
	if asking > 0 && n.added == nil {
		c.Put(1, y, "Each node answers for itself; a slow one never holds up the others.", theme.Muted)
		y += 2
	}
	if n.removing {
		c.Clip(0, c.H-7)
		n.drawPhoneApp(c, y, true)
		c.Unclip()
		n.drawRemoval(c)
		return
	}
	for _, row := range n.Context.Others {
		if n.look(row).lost {
			y = paragraph(c, y, "! "+row.Short()+": "+n.noAnswer(row)+". The hub asks again every 15s; ⏎ on its row shows what is known.", theme.FgInk(theme.Yellow)) + 1
		}
	}
	y = n.drawPhoneApp(c, y, true)
	n.drawProblem(c, y+1)
	n.hint(c, "⏎ details   a add   d make default   h home   x remove   r check again   esc close")
}

func (n *Nodes) noAnswer(row NodeRow) string {
	if row.Contact.IsZero() {
		return "no answer yet"
	}
	return "no answer since " + clock(row.Contact)
}

func (n *Nodes) lostNames(except string) []string {
	var names []string
	for _, row := range n.Context.Others {
		if row.ID != except && n.look(row).lost {
			names = append(names, row.Short())
		}
	}
	return names
}

func (n *Nodes) drawAdded(c *theme.Canvas, y int) int {
	m := n.added.Member
	short := shortName(m.Name)
	text := "✓ " + short + " added. This hub and your phone list it now"
	if lost := n.lostNames(m.ID); len(lost) > 0 {
		text += "; " + joinNames(lost) + " " + hears(len(lost)) + " about it when it is back"
	}
	y = paragraph(c, y, text+".", theme.FgInk(theme.Green)) + 1
	var failing []string
	for _, check := range n.added.Report.Checks {
		if check.Mark == node.MarkFail {
			failing = append(failing, check.Name)
		}
	}
	if conflict := n.added.Push.Conflict; conflict != "" {
		y = paragraph(c, y, "! "+withStop(conflict), theme.FgInk(theme.Yellow)) + 1
	}
	if len(failing) == 0 {
		return y
	}
	return paragraph(c, y, "! "+short+": "+joinNames(failing)+" need fixing there, so factories can't start or move there yet. ⏎ shows the fixes.", theme.FgInk(theme.Yellow)) + 1
}

func hears(count int) string {
	if count == 1 {
		return "hears"
	}
	return "hear"
}

func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func (n *Nodes) drawRemoval(c *theme.Canvas) {
	row, _ := n.selected()
	short := row.Short()
	var others []string
	for _, o := range n.Context.Others {
		if o.ID != row.ID {
			others = append(others, o.Short())
		}
	}
	listers := "This hub and your phone stop"
	if len(others) > 0 {
		listers = "This hub, your phone and " + joinNames(others) + " stop"
	}
	factories := "Its factories keep running there if it is on."
	if l := n.look(row); l.factories != "–" {
		factories = "Its " + factoryCount(atoi(l.factories)) + " keep running there if it is on."
	}
	text := fmt.Sprintf("Remove %s? %s listing it. %s When %s is back, it learns it was removed and stops sending notifications to your phone.", short, listers, factories, short)
	paragraph(c, c.H-6, text, theme.FgInk(theme.Yellow))
	c.Unclip()
	c.Spans(1, c.H-1, []theme.Span{{Text: "y", Ink: theme.Key}, {Text: " remove   ", Ink: theme.Text}, {Text: "n", Ink: theme.Key}, {Text: " keep", Ink: theme.Text}})
}

func factoryCount(count int) string {
	if count == 1 {
		return "1 factory"
	}
	return fmt.Sprintf("%d factories", count)
}

func atoi(s string) int {
	var v int
	fmt.Sscan(s, &v)
	return v
}

func (n *Nodes) hint(c *theme.Canvas, text string) {
	c.Unclip()
	c.Put(1, c.H-1, text, theme.Text)
}

func (n *Nodes) drawProblem(c *theme.Canvas, y int) {
	if n.problem != "" {
		paragraph(c, y, "✗ "+n.problem, theme.FgInk(theme.Red))
	}
}

func (n *Nodes) state() (string, theme.Ink) {
	if n.Context.Phone.Running {
		return "● this machine", theme.FgInk(theme.Green)
	}
	return "! not running", theme.FgInk(theme.Yellow)
}

func (n *Nodes) answered() bool { return n.Context.Version != "" }

func (n *Nodes) version() string {
	if !n.answered() {
		return "–"
	}
	return n.Context.Version
}

func (n *Nodes) drawFactories(c *theme.Canvas, x, y int, none string) {
	switch {
	case !n.answered():
		c.Put(x, y, "–", theme.Muted)
		return
	case n.Context.Factories == 0:
		c.Put(x, y, none, theme.Muted)
		return
	}
	end := c.Put(x, y, fmt.Sprint(n.Context.Factories), theme.Text)
	if n.Context.NeedsYou > 0 {
		c.Put(end, y, fmt.Sprintf(" · %d needs you", n.Context.NeedsYou), theme.FgInk(theme.Orange))
	}
}

func (n *Nodes) drawPhoneApp(c *theme.Canvas, y int, many bool) int {
	c.Put(1, y, "Phone app", theme.Bright.B())
	url, urlInk := n.Context.Phone.url()
	if n.Context.Home != "" {
		url, urlInk = n.Context.Home, theme.Bright
	}
	end := c.PutMax(valueX, y, url, urlInk, c.W-valueX-1)
	where := "lives on " + n.homeShort()
	if many {
		where += ", reaches every node"
	}
	if n.Context.Phone.Hostname != "" && end+3+theme.Width(where) <= c.W-1 {
		c.Put(end+3, y, where, theme.Muted)
	}
	notify, notifyInk := n.Context.Phone.notify()
	c.Put(3, y+1, "notify", theme.Text)
	c.PutMax(valueX, y+1, notify, notifyInk, c.W-valueX-1)
	if n.Context.Phone.Hostname == "" || many {
		return y + 2
	}
	c.Put(3, y+2, "open", theme.Text)
	lines := theme.Wrap("that URL on your phone with Tailscale on. No code to scan or type.", c.W-valueX-1)
	for i, line := range lines {
		c.Put(valueX, y+2+i, line, theme.Muted)
	}
	return y + 2 + len(lines)
}

func (n *Nodes) drawScrolled(c *theme.Canvas) {
	n.drawHeading(c)
	bottom := c.H - 1
	switch {
	case n.confirmingMove:
		bottom = c.H - 6
	case n.homing != nil:
		bottom = c.H - 10
	}
	c.Clip(1, bottom)
	n.drawBody(c, 2-n.scroll)
	c.Unclip()
	if n.confirmingMove {
		n.drawMoveAllConfirm(c)
		return
	}
	if n.homing != nil {
		n.drawHomeConfirm(c)
		return
	}
	hints := n.hints()
	if n.maxScroll() > 0 {
		hints = "j/k scroll   " + hints
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (n *Nodes) maxScroll() int {
	c := theme.NewCanvas(n.width, n.height)
	c.Clip(0, 0)
	return max(n.drawBody(c, 2)-(c.H-1), 0)
}

func (n *Nodes) drawHeading(c *theme.Canvas) {
	switch n.screen {
	case selfDetailsScreen:
		c.Put(1, 0, n.Context.Machine, theme.Bright.B())
		state, stateInk := n.state()
		c.Right(c.W-2, 0, state, stateInk)
	case peerDetailsScreen:
		row, _ := n.selected()
		c.Put(1, 0, row.Short(), theme.Bright.B())
		state, stateInk := n.detailsState(row)
		c.Right(c.W-2, 0, state, stateInk)
	case checkScreen:
		title, ink := n.checkTitle()
		c.Put(1, 0, title, ink)
		c.Right(c.W-2, 0, "2 of 2", theme.Muted)
	}
}

func (n *Nodes) drawBody(c *theme.Canvas, y int) int {
	switch n.screen {
	case peerDetailsScreen:
		return n.drawPeerDetails(c, y)
	case checkScreen:
		return n.drawCheck(c, y)
	}
	return n.drawSelfDetails(c, y)
}

func (n *Nodes) hints() string {
	switch n.screen {
	case selfDetailsScreen:
		if n.checks == nil {
			return "esc back"
		}
		return "r check again   h home   esc back"
	case peerDetailsScreen:
		return n.peerHints()
	}
	return n.checkHints()
}

func (n *Nodes) drawSelfDetails(c *theme.Canvas, y int) int {
	url, urlInk := n.Context.Phone.url()
	notify, notifyInk := n.Context.Phone.notify()
	c.Put(3, y, "address", theme.Text)
	c.PutMax(fieldX, y, url, urlInk, c.W-fieldX-1)
	c.Put(3, y+1, "factories", theme.Text)
	n.drawFactories(c, fieldX, y+1, "none yet")
	c.Put(3, y+2, "notifies", theme.Text)
	c.PutMax(fieldX, y+2, notify, notifyInk, c.W-fieldX-1)
	c.Put(1, y+4, "Checks", theme.Bright.B())
	if n.checks == nil {
		c.Put(3, y+5, "◌ checking Tailscale, the service and the push services…", theme.Muted)
		return y + 6
	}
	end := drawChecks(c, y+5, n.checks)
	return paragraph(c, end+1, "Nothing was changed. Each fix runs on "+n.Context.Machine+"; r checks again.", theme.Muted)
}

func (n *Nodes) detailsState(row NodeRow) (string, theme.Ink) {
	p := n.peer(row.ID)
	l := n.look(row)
	switch {
	case l.lost && row.Contact.IsZero():
		return "↮ unreachable", theme.FgInk(theme.Red)
	case l.lost:
		return "↮ unreachable since " + clock(row.Contact), theme.FgInk(theme.Red)
	case !l.answered:
		return l.state, l.stateInk
	}
	return l.state + " · checked " + clock(p.answer.Report.At), l.stateInk
}

func (n *Nodes) field(c *theme.Canvas, y int, label string, spans ...theme.Span) {
	c.Put(3, y, label, theme.Text)
	c.Spans(fieldX, y, spans)
}

func (n *Nodes) drawPeerDetails(c *theme.Canvas, y int) int {
	row, _ := n.selected()
	p := n.peer(row.ID)
	l := n.look(row)
	n.field(c, y, "address", theme.Span{Text: "https://" + row.Name, Ink: theme.Bright})
	machine := cmp.Or(row.OS, "unknown system")
	switch {
	case l.answered:
		machine += " · factory " + l.version
	case row.Version != "":
		machine += " · factory " + row.Version + ", when last seen"
	}
	n.field(c, y+1, "machine", theme.Span{Text: machine, Ink: theme.Text})
	factories := []theme.Span{{Text: "none yet", Ink: theme.Muted}}
	if count := atoi(l.factories); count > 0 {
		factories = []theme.Span{{Text: l.factories, Ink: theme.Text}}
		if l.needs > 0 {
			factories = append(factories, theme.Span{Text: fmt.Sprintf(" · %d needs you", l.needs), Ink: theme.FgInk(theme.Orange)})
		}
		if l.lost {
			factories = append(factories, theme.Span{Text: ", as of " + clock(row.Contact), Ink: theme.Muted})
		}
	}
	n.field(c, y+2, "factories", factories...)
	y += 4
	if l.lost {
		return n.drawLostDetails(c, y, row, p)
	}
	c.Put(1, y, "Ready for factories", theme.Bright.B())
	if !l.answered {
		c.Put(3, y+1, "◌ asking "+row.Short()+"…", theme.FgInk(theme.Cyan))
		return y + 2
	}
	y = drawChecks(c, y+1, n.cloneRows(row, p.answer.Report.Checks))
	if p.cloneErr != nil {
		y = paragraph(c, y+1, "✗ "+p.cloneErr.Error(), theme.FgInk(theme.Red))
	}
	return paragraph(c, y+1, readySummary(row.Short(), p.answer.Report.Problems), theme.Text)
}

func readySummary(short string, problems int) string {
	if problems == 0 {
		return "Every check passes. Factories can start on " + short + " and move there."
	}
	return fmt.Sprintf("%s. Factories won't start or move to %s until %s.", plural(problems, "problem"), short, passes(problems))
}

func (n *Nodes) cloneRows(row NodeRow, checks []node.Check) []CheckRow {
	rows := checkRows(checks)
	missing := n.uncloned(row)
	if len(missing) == 0 {
		return rows
	}
	act := "clones " + repoNames(missing) + " now, with " + row.Short() + "'s git login"
	if n.peer(row.ID).cloning {
		act = "◌ cloning " + repoNames(missing) + " on " + row.Short() + "…"
	}
	for i := range rows {
		if rows[i].Name == "repos" {
			rows[i].Act, rows[i].ActKey = act, "c"
		}
	}
	return rows
}

func repoNames(repos []RepoURL) string {
	var names []string
	for _, r := range repos {
		names = append(names, r.Name)
	}
	return joinNames(names)
}

func (n *Nodes) drawLostDetails(c *theme.Canvas, y int, row NodeRow, p *peerState) int {
	short := row.Short()
	gone := "hasn't answered yet"
	if !row.Contact.IsZero() {
		gone = "hasn't answered for " + theme.Age(n.lostFor(row))
	}
	y = paragraph(c, y, short+" "+gone+". Its factories keep running there if it is on; nothing moves by itself. The hub asks again every 15s and this view updates when it answers.", theme.Text)
	var rows []CheckRow
	switch {
	case p.seen == nil && p.seenErr == nil:
		rows = []CheckRow{{Name: "tailscale", Mark: "wait", Detail: "asking tailscale status on " + n.Context.Machine + "…"}}
	case p.seenErr != nil:
		rows = []CheckRow{{Name: "tailscale", Mark: "skip", Detail: p.seenErr.Error()}}
	case !p.seen.Online:
		seen := ""
		if !p.seen.LastSeen.IsZero() {
			seen = "; Tailscale last saw it at " + clock(p.seen.LastSeen)
		}
		rows = []CheckRow{{Name: "tailscale", Mark: "fail", Detail: short + " is offline" + seen}, {Name: "node", Mark: "skip", Detail: "not asked: " + short + " is offline"}}
	default:
		rows = []CheckRow{{Name: "tailscale", Mark: "ok", Detail: short + " is online"}, {Name: "node", Mark: "fail", Detail: "nothing answers at https://" + row.Name}}
	}
	y = drawChecks(c, y+1, rows)
	c.Spans(1, y+1, []theme.Span{{Text: "On " + short + ", ", Ink: theme.Muted}, {Text: "factory node status", Ink: theme.Bright}, {Text: "  says whether its node is running.", Ink: theme.Muted}})
	return y + 2
}

func (n *Nodes) peerHints() string {
	row, _ := n.selected()
	if n.look(row).lost {
		return "r check now   x remove   esc back"
	}
	hints := "r check again   "
	if missing := n.uncloned(row); len(missing) > 0 {
		hints += "c clone " + repoNames(missing) + "   "
	}
	if n.takesFactories(row) {
		hints += "m move all here   "
	}
	return hints + "d make default   h home   x remove   esc back"
}

func checkRows(checks []node.Check) []CheckRow {
	rows := make([]CheckRow, 0, len(checks))
	for _, c := range checks {
		rows = append(rows, CheckRow{Name: c.Name, Mark: c.Mark, Detail: c.Detail, Fix: c.Fix, On: c.On, Lines: c.Lines})
	}
	return rows
}

func (n *Nodes) checkingShort() string {
	if n.report != nil && n.report.Target.Short != "" {
		return n.report.Target.Short
	}
	return shortName(n.checking)
}

func failing(r node.CheckReport) node.Check {
	for _, c := range slices.Backward(r.Checks) {
		if c.Mark == node.MarkFail {
			return c
		}
	}
	return node.Check{}
}

type refusalLook struct {
	title   string
	explain string
	ink     theme.Ink
	then    string
	retry   bool
}

func (n *Nodes) refusal() refusalLook {
	short := n.checkingShort()
	r := *n.report
	fail := failing(r)
	red := theme.FgInk(theme.Red)
	switch {
	case fail.Name == "funnel":
		return refusalLook{title: short + " can't be added", explain: "✗ factory never adds a node the internet can reach, and a node with Funnel on refuses every request until it is off. Nothing was changed.", ink: red, then: "Turn Funnel off on " + short + ", then r checks again.", retry: true}
	case fail.Name == "owner" && fail.Fix == node.UntagFix:
		return refusalLook{title: short + " can't show it is yours", explain: "Nodes talk to each other as you. A tagged machine can't, so it can't be a node. Log it in as " + cmp.Or(n.Context.Owner, "you") + ", then add it again.", ink: theme.Muted}
	case fail.Name == "owner":
		return refusalLook{title: short + " isn't yours", explain: "factory adds only your own nodes; each node answers its owner and no one else.", ink: theme.Muted}
	case fail.Name == "tailscale":
		return refusalLook{title: short + " is offline", explain: "Nothing was changed. Turn " + short + " on and log it in to Tailscale, then r checks again.", ink: theme.Text, retry: true}
	case r.Target.State == node.StateNoNode:
		return refusalLook{title: "No factory node on " + short, explain: "After the install, " + short + " offers to join " + n.Context.Machine + " itself, so you don't have to come back here.", ink: theme.Muted, retry: true}
	case r.Target.State == node.StateIncompatible:
		return refusalLook{title: short + " runs another factory version", explain: "✗ " + r.Refusal + ". Nothing was changed.", ink: red, retry: true}
	}
	return refusalLook{title: short + " can't be added", explain: "✗ " + withStop(r.Refusal) + " Nothing was changed.", ink: red, retry: true}
}

func withStop(sentence string) string {
	if strings.HasSuffix(sentence, ".") {
		return sentence
	}
	return sentence + "."
}

func (n *Nodes) checkTitle() (string, theme.Ink) {
	short := n.checkingShort()
	red := theme.FgInk(theme.Red).B()
	switch {
	case n.reportErr != nil && n.fromTyped:
		return "No machine called " + n.checking, red
	case n.reportErr != nil:
		return short + " could not be checked", red
	case n.report == nil:
		return "Checking " + short, theme.Bright.B()
	case n.report.Refusal != "":
		return n.refusal().title, red
	case n.report.PushConflict:
		return short + " sends with another key", theme.FgInk(theme.Yellow).B()
	case n.report.Problems > 0:
		return fmt.Sprintf("%s needs %s", short, plural(n.report.Problems, "thing")), theme.Bright.B()
	}
	return short + " is ready", theme.Bright.B()
}

var pendingChecks = []string{"tailscale", "node", "owner", "funnel", "tools", "claude", "git access", "repos", "push"}

func (n *Nodes) drawCheck(c *theme.Canvas, y int) int {
	short := n.checkingShort()
	switch {
	case n.reportErr != nil:
		return paragraph(c, y, withStop(n.reportErr.Error()), theme.Text)
	case n.report == nil:
		rows := []CheckRow{{Name: pendingChecks[0], Mark: "wait", Detail: "asking " + short + "…"}}
		for _, name := range pendingChecks[1:] {
			rows = append(rows, CheckRow{Name: name, Mark: "todo"})
		}
		y = drawChecks(c, y, rows)
		return paragraph(c, y+1, short+" runs these checks itself and sends back the results. Nothing is written on either machine yet.", theme.Muted)
	}
	y = drawChecks(c, y, n.pushRows(checkRows(n.report.Checks)))
	if n.report.Refusal != "" {
		look := n.refusal()
		y = paragraph(c, y+1, look.explain, look.ink)
		if look.then != "" {
			y = paragraph(c, y+1, look.then, theme.Text)
		}
		return y
	}
	y = n.drawAlso(c, y+1, short)
	if n.report.PushConflict {
		y = paragraph(c, y+1, "Without p, "+short+" is added but can't notify your phone: one key serves every node.", theme.Muted)
	}
	switch {
	case n.adding:
		y = paragraph(c, y+1, "◌ adding "+short+"…", theme.FgInk(theme.Cyan))
	case n.report.Problems == 0:
		y = paragraph(c, y+1, "Every check passes. Factories can start on "+short+" and move there.", theme.Text)
	default:
		y = paragraph(c, y+1, fmt.Sprintf("%s. %s is added either way; factories won't start or move there until %s. r checks again once you fix them on %s.", plural(n.report.Problems, "problem"), short, passes(n.report.Problems), short), theme.Text)
	}
	if n.problem != "" {
		y = paragraph(c, y+1, "✗ "+n.problem, theme.FgInk(theme.Red))
	}
	return y
}

func (n *Nodes) home() string {
	if home := shortName(strings.TrimPrefix(n.Context.Home, "https://")); home != "" {
		return home
	}
	return n.Context.Machine
}

func (n *Nodes) drawAlso(c *theme.Canvas, y int, short string) int {
	c.Put(1, y, "Adding "+short+" also", theme.Bright.B())
	lines := []struct {
		mark string
		ink  theme.Ink
		text string
	}{
		{"↔", theme.FgInk(theme.Cyan), " tells your other nodes, so this hub, your phone and every node list it"},
		{"◆", theme.FgInk(theme.Orange), n.pushLine(short)},
		{"▣", theme.FgInk(theme.Blue), " lets your phone app, which lives on " + n.home() + ", reach " + short + " directly"},
	}
	for i, l := range lines {
		c.Spans(3, y+1+i, []theme.Span{{Text: l.mark, Ink: l.ink}, {Text: l.text, Ink: theme.Text}})
	}
	return y + 1 + len(lines)
}

func (n *Nodes) pushLine(short string) string {
	if n.report != nil && n.report.PushConflict {
		return " keeps " + short + "'s own push key unless you press p, so it can't notify your phone"
	}
	return " gives it " + n.Context.Machine + "'s push key and your phone's subscription, so it notifies you"
}

func (n *Nodes) pushRows(rows []CheckRow) []CheckRow {
	for i := range rows {
		if rows[i].Name == "push" && n.report.PushConflict {
			rows[i].Act, rows[i].ActKey = "switches it to "+n.Context.Machine+"'s key; that phone subscribes again", "p"
		}
	}
	return rows
}

func (n *Nodes) checkHints() string {
	short := n.checkingShort()
	switch {
	case n.reportErr != nil && n.fromTyped:
		return "tab back to the name   esc back"
	case n.reportErr != nil:
		return "r check again   esc back"
	case n.report == nil:
		return "esc stop"
	case n.adding:
		return ""
	case n.report.Refusal != "" && n.refusal().retry:
		return "r check again   esc back"
	case n.report.Refusal != "":
		return "esc back"
	case n.report.PushConflict:
		return "⏎ add " + short + "   p switch key, then add   esc back"
	case n.report.Problems == 0:
		return "⏎ add " + short + "   d add it as the default for new factories   esc back"
	}
	return "⏎ add " + short + "   r check again   esc back"
}

func (n *Nodes) drawPick(c *theme.Canvas) {
	c.Put(1, 0, "Add a node", theme.Bright.B())
	rows, scanning := n.pickRows()
	step := "1 of 2"
	if scanning {
		step = fmt.Sprintf("1 of 2 · asking %d %s…", n.asking(rows), nounFor(n.asking(rows), "machine"))
		if rows == nil {
			step = "1 of 2 · asking your machines…"
		}
	}
	c.Right(c.W-2, 0, step, theme.Muted)
	y := paragraph(c, 1, "Your machines on "+n.domain()+". ⏎ checks the one you pick; nothing changes yet.", theme.Muted) + 1
	c.Put(nodeX, y, "machine", theme.Muted)
	c.Put(pickOSX, y, "os", theme.Muted)
	c.Put(pickNodeX, y, "factory node", theme.Muted)
	y++
	for i, row := range rows {
		if i == n.pick && n.listed {
			c.Fill(0, y, c.W, 1, theme.Selection)
			c.Put(1, y, "❯", theme.Focused.B())
		}
		nameInk := theme.Bright
		if !row.Selectable {
			nameInk = theme.Text
		}
		c.PutMax(nodeX, y, row.Short, nameInk, pickOSX-nodeX-1)
		c.PutMax(pickOSX, y, row.OS, theme.Quiet, pickNodeX-pickOSX-1)
		n.drawCandidate(c, y, row)
		y++
	}
	if n.listed && len(rows) == 0 && n.listErr == nil {
		c.Put(nodeX, y, "No other machine of yours is on the tailnet now.", theme.Muted)
		y++
	}
	c.Put(nodeX, y+1, "+ type a name…", theme.Quiet)
	y += 3
	switch {
	case n.listErr != nil:
		paragraph(c, y, "✗ "+n.listErr.Error(), theme.FgInk(theme.Red))
	case scanning:
		paragraph(c, y, "Each machine is asked at https://<name>."+n.domain()+" for 2s at most. Rows fill in as they answer.", theme.Muted)
	default:
		paragraph(c, y, n.notListed(), theme.Muted)
	}
	hints := "↑↓ pick   t type a name   esc back"
	if c, ok := n.picked(); ok && n.listed {
		hints = "⏎ check " + c.Short + "   " + hints
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func (n *Nodes) asking(rows []node.Candidate) int {
	count := 0
	for _, r := range rows {
		if r.State == "" {
			count++
		}
	}
	return count
}

func (n *Nodes) pickRows() ([]node.Candidate, bool) {
	if n.listed {
		return n.candidates, false
	}
	if n.inventory == nil {
		return nil, true
	}
	var rows []node.Candidate
	inv := *n.inventory
	for _, p := range inv.Peers {
		ownMachine := strings.EqualFold(p.Login, inv.Self.Login) && strings.HasSuffix(p.Name, "."+inv.Self.Tailnet)
		switch {
		case n.enrolled(p.Name) || (p.OS != "macOS" && p.OS != "linux"):
		case len(p.Tags) > 0:
			rows = append(rows, node.Candidate{Name: p.Name, Short: p.Short(), OS: p.OS, State: node.StateTagged, Detail: "tagged " + strings.Join(p.Tags, ", ") + ", so it can't show it is yours"})
		case !ownMachine:
		case !p.Online:
			rows = append(rows, node.Candidate{Name: p.Name, Short: p.Short(), OS: p.OS, State: node.StateOffline, LastSeen: p.LastSeen})
		default:
			rows = append(rows, node.Candidate{Name: p.Name, Short: p.Short(), OS: p.OS})
		}
	}
	return rows, true
}

func (n *Nodes) enrolled(name string) bool {
	return slices.ContainsFunc(n.Context.Others, func(r NodeRow) bool { return r.Name == name })
}

func (n *Nodes) drawCandidate(c *theme.Canvas, y int, row node.Candidate) {
	limit := c.W - pickNodeX - 1
	switch row.State {
	case "":
		c.Put(pickNodeX, y, "◌ asking…", theme.FgInk(theme.Cyan))
	case node.StateNode:
		c.PutMax(pickNodeX, y, "● "+row.Detail, theme.FgInk(theme.Green), limit)
	case node.StateOffline:
		offline := "– offline"
		if !row.LastSeen.IsZero() {
			offline += " " + theme.Age(n.now().Sub(row.LastSeen))
		}
		c.Put(pickNodeX, y, offline, theme.Muted)
	case node.StateNoNode:
		end := c.Put(pickNodeX, y, "– no node answers · ", theme.Muted)
		c.PutMax(end, y, "on "+row.On+": "+row.Fix, theme.Quiet, c.W-end-1)
	case node.StateIncompatible:
		end := c.Put(pickNodeX, y, "– another factory version · ", theme.Muted)
		c.PutMax(end, y, "on "+row.On+": "+row.Fix, theme.Quiet, c.W-end-1)
	default:
		c.PutMax(pickNodeX, y, "– "+row.Detail, theme.Muted, limit)
	}
}

func (n *Nodes) notListed() string {
	listed := []string{n.Context.Machine}
	for _, row := range n.Context.Others {
		listed = append(listed, row.Short())
	}
	already := ", already nodes"
	if len(listed) == 1 {
		already = ", already a node"
	}
	text := "Not listed: " + joinNames(listed) + already
	if phones := n.phones(); len(phones) > 0 {
		text += "; " + joinNames(phones) + ", because a phone can't run a node"
	}
	return text + "; machines of other tailnet users."
}

func (n *Nodes) phones() []string {
	if n.inventory == nil {
		return nil
	}
	var names []string
	for _, p := range n.inventory.Peers {
		if (p.OS == "iOS" || p.OS == "android") && strings.EqualFold(p.Login, n.inventory.Self.Login) {
			names = append(names, p.Short())
		}
	}
	return names
}

func (n *Nodes) drawType(c *theme.Canvas) {
	c.Put(1, 0, "Add a node", theme.Bright.B())
	c.Right(c.W-2, 0, "1 of 2", theme.Muted)
	c.Put(1, 2, "Tailnet name or address", theme.Text)
	drawInput(c, 1, 3, c.W-2, "", []string{n.typed + "█"}, true, theme.Focused)
	example := cmp.Or(shortName(strings.TrimSpace(n.typed)), "homelab")
	y := paragraph(c, 6, example+", "+example+"."+n.domain()+" or 100.101.7.12. For a machine the list doesn't show, such as one whose node started a moment ago.", theme.Muted)
	paragraph(c, y+1, "factory asks https://"+example+"."+n.domain()+", through Tailscale Serve on that machine. Adding a node never uses ssh.", theme.Muted)
	hints := "tab back to the list   esc back"
	if typed := strings.TrimSpace(n.typed); typed != "" {
		hints = "⏎ check " + typed + "   " + hints
	}
	c.Put(1, c.H-1, hints, theme.Text)
}

func paragraph(c *theme.Canvas, y int, text string, ink theme.Ink) int {
	for _, l := range theme.Wrap(text, c.W-2) {
		c.Put(1, y, l, ink)
		y++
	}
	return y
}

var markGlyphs = map[string]struct {
	glyph string
	ink   theme.Ink
}{
	"ok":   {"✓", theme.FgInk(theme.Green)},
	"fail": {"✗", theme.FgInk(theme.Red)},
	"skip": {"–", theme.Muted},
	"warn": {"!", theme.FgInk(theme.Yellow)},
	"wait": {"◌", theme.FgInk(theme.Yellow)},
	"todo": {"·", theme.Muted},
}

const (
	checkNameX   = 5
	checkDetailX = 18
	checkFixX    = 23
	valueX       = 14
)

func drawChecks(c *theme.Canvas, y int, rows []CheckRow) int {
	for _, row := range rows {
		mark := markGlyphs[row.Mark]
		detailInk, nameInk := theme.Text, theme.Text
		if row.Mark == "skip" {
			detailInk = theme.Muted
		}
		if row.Mark == "todo" {
			nameInk = theme.Muted
		}
		c.Put(3, y, mark.glyph, mark.ink)
		c.PutMax(checkNameX, y, row.Name, nameInk, checkDetailX-checkNameX-1)
		for _, line := range theme.Wrap(row.Detail, c.W-checkDetailX-1) {
			c.Put(checkDetailX, y, line, detailInk)
			y++
		}
		for _, line := range row.Lines {
			c.PutMax(checkDetailX, y, line, theme.Text, c.W-checkDetailX-1)
			y++
		}
		if row.Fix != "" && row.Mark != "ok" {
			y = drawFix(c, y, row)
		}
		if row.Act != "" {
			c.Put(checkDetailX, y, row.ActKey, theme.Key)
			c.PutMax(checkFixX, y, row.Act, theme.Text, c.W-checkFixX-1)
			y++
		}
	}
	return y
}

func drawFix(c *theme.Canvas, y int, row CheckRow) int {
	c.Put(checkDetailX, y, "fix", theme.Muted)
	x := checkFixX
	if row.On != "" {
		x = c.Put(x, y, "on "+row.On+"  ", theme.Muted)
	}
	c.PutMax(x, y, row.Fix, theme.Bright, c.W-x-1)
	return y + 1
}

func passes(problems int) string {
	if problems == 1 {
		return "it passes"
	}
	return "they pass"
}
