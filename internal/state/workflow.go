package state

type StackBranch struct {
	Branch string `json:"branch"`
	Parent string `json:"parent"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	Title  string `json:"title"`
}

type Workflow struct {
	Runs          []WorkflowRun `json:"runs"`
	ActiveRun     string        `json:"active_run"`
	Pending       *Effect       `json:"pending,omitempty"`
	ReturnLoop    string        `json:"return_loop"`
	ReturnTo      string        `json:"return_to"`
	Correction    *Correction   `json:"correction,omitempty"`
	Gate          string        `json:"gate"`
	CommentsAsked bool          `json:"comments_asked"`
}

type WorkflowRun struct {
	ID         string        `json:"id"`
	Stage      string        `json:"stage"`
	Role       string        `json:"role"`
	Round      int           `json:"round"`
	Loop       string        `json:"loop"`
	InputHeads []StackBranch `json:"input_heads"`
	ReturnTo   string        `json:"return_to"`
	Prompt     string        `json:"prompt"`
	Result     string        `json:"result"`
	Outcome    string        `json:"outcome"`
	Retries    int           `json:"retries"`
}

type Effect struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	RunID string `json:"run_id"`
	Stage string `json:"stage"`
	Role  string `json:"role"`
}

type Correction struct {
	Loop     string `json:"loop"`
	Stage    string `json:"stage"`
	ReturnTo string `json:"return_to"`
}

type Loops struct {
	Active  string         `json:"active"`
	Used    map[string]int `json:"used"`
	Request int            `json:"request,omitempty"`
}

type Command struct {
	Command   string `json:"command"`
	Directory string `json:"directory"`
	Source    string `json:"source"`
}

type Recipe struct {
	Digest     string             `json:"digest"`
	Commands   map[string]Command `json:"commands"`
	QA         map[string]Command `json:"qa"`
	Unresolved []string           `json:"unresolved"`
}

type Finding struct {
	ID          string `json:"id"`
	Stage       string `json:"stage"`
	Severity    string `json:"severity"`
	Layer       string `json:"layer"`
	Disposition string `json:"disposition"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"`
}

type CheckEvidence struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	Directory  string `json:"directory"`
	Head       string `json:"head"`
	ExitStatus int    `json:"exit_status"`
	Path       string `json:"path"`
}

type Verification struct {
	Status       string          `json:"status"`
	Reason       string          `json:"reason"`
	Heads        []StackBranch   `json:"heads"`
	RecipeDigest string          `json:"recipe_digest"`
	At           Time            `json:"at"`
	Checks       []CheckEvidence `json:"checks"`
	Notes        []string        `json:"notes"`
}

type PRVerification struct {
	Number         int    `json:"number"`
	Head           string `json:"head"`
	Base           string `json:"base"`
	Open           bool   `json:"open"`
	Draft          bool   `json:"draft"`
	CI             string `json:"ci"`
	Reason         string `json:"reason"`
	Policy         string `json:"policy"`
	ApprovalNeeded bool   `json:"approval_needed"`
	At             Time   `json:"at"`
}

type Evidence struct {
	Local               Verification      `json:"local"`
	Review              Verification      `json:"review"`
	QA                  Verification      `json:"qa"`
	PRs                 []PRVerification  `json:"prs"`
	Comments            Verification      `json:"comments"`
	CommentsStopped     bool              `json:"comments_stopped"`
	CommentDispositions map[string]string `json:"comment_dispositions"`
	Pending             bool              `json:"pending"`
}

type Confirmation struct {
	ItemID  string        `json:"item_id"`
	Heads   []StackBranch `json:"heads"`
	At      Time          `json:"at"`
	Receipt string        `json:"receipt"`
}
