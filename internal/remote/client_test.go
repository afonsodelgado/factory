package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/afonsodelgado/factory/internal/state"
)

const dropOnceSSH = `#!/bin/sh
req=$(cat)
printf '%s\n' "$req" >>"$FACTORY_TEST_LOG"
case "$req" in
*'"action":"hello"'*)
	echo '{"protocol":1,"server":"s1","status":"completed","data":{"server":"s1","release":"x","protocol":1,"platform":"linux"}}'
	;;
*)
	if [ ! -e "$FACTORY_TEST_DROPPED" ]; then
		: >"$FACTORY_TEST_DROPPED"
		echo "Connection closed" >&2
		exit 255
	fi
	echo '{"protocol":1,"server":"s1","status":"completed","data":{}}'
	;;
esac
`

const keepsRevisionSSH = `#!/bin/sh
req=$(cat)
case "$req" in
*'"action":"hello"'*)
	echo '{"protocol":1,"server":"s1","status":"completed","data":{"server":"s1","release":"x","protocol":1,"platform":"linux"}}'
	;;
*'"action":"push"'*)
	sent=$(printf '%s' "$req" | sed -n 's/.*"data":\(.*\)}$/\1/p')
	sent_revision=$(printf '%s' "$sent" | sed -n 's/.*"revision":\([0-9]*\).*/\1/p')
	held=$(cat "$FACTORY_TEST_PUSH")
	held_revision=$(printf '%s' "$held" | sed -n 's/.*"revision":\([0-9]*\).*/\1/p')
	if [ "$sent_revision" -gt "$held_revision" ]; then
		printf '%s' "$sent" >"$FACTORY_TEST_PUSH"
		held=$sent
	fi
	echo '{"protocol":1,"server":"s1","status":"completed","data":'"$held"'}'
	;;
esac
`

func operationsSent(t *testing.T, log string) []string {
	t.Helper()
	f, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ops []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var r Request
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Action == "stage" {
			ops = append(ops, r.Op)
		}
	}
	return ops
}

func TestRetryAfterALostAnswerReusesTheOperation(t *testing.T) {
	bin, work := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(dropOnceSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(work, "requests")
	t.Setenv("FACTORY_TEST_LOG", log)
	t.Setenv("FACTORY_TEST_DROPPED", filepath.Join(work, "dropped"))
	short, err := os.MkdirTemp("/tmp", "frp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	client := Client{Transport{Name: "homelab", Remote: state.Remote{Host: "homelab"}, StateDir: short}}
	payload := map[string]string{"role": "qa", "text": "go"}

	err = client.MutateOnce(context.Background(), "stage", payload, nil)
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("a lost answer reported %v, want an unknown outcome", err)
	}
	if err := client.MutateOnce(context.Background(), "stage", payload, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.MutateOnce(context.Background(), "stage", payload, nil); err != nil {
		t.Fatal(err)
	}
	ops := operationsSent(t, log)
	if len(ops) != 3 || ops[0] == "" || ops[0] != ops[1] || ops[1] == ops[2] {
		t.Fatalf("operations sent %s; want the retry to reuse the first id and a new action after the answer to get its own", strings.Join(ops, ", "))
	}
}

func TestPushSettingsReachAServerWhoseRevisionIsAheadOfOrEqualToThisLaptops(t *testing.T) {
	for name, held := range map[string]string{
		"a server counting ahead":    `{"server":"https://ntfy.sh","topic":"factory-old-topic","label":"homelab","revision":5}`,
		"a server at the same count": `{"server":"https://ntfy.sh","topic":"factory-old-topic","label":"homelab","revision":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			bin, work := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(keepsRevisionSSH), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			server := filepath.Join(work, "settings")
			t.Setenv("FACTORY_TEST_PUSH", server)
			if err := os.WriteFile(server, []byte(held), 0o600); err != nil {
				t.Fatal(err)
			}
			short, err := os.MkdirTemp("/tmp", "frp-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(short) })
			client := Client{Transport{Name: "homelab", Remote: state.Remote{Host: "homelab"}, StateDir: short}}
			push := state.Push{Topic: "factory-rotated-topic"}

			if err := client.SyncPush(context.Background(), push); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(server)
			if !strings.Contains(string(got), "factory-rotated-topic") {
				t.Fatalf("the server holds %s; want the rotated topic, since a leaked topic must stop receiving alerts", got)
			}
			if PushPending(short, "homelab", push) {
				t.Fatal("the laptop still lists the setting as pending after the server took it")
			}
		})
	}
}

func TestOnlyReportsAndAWalkthroughsStackFilesGoBackToTheServer(t *testing.T) {
	dir := t.TempDir()
	stackDir := ".claude/factory/csv/review"
	job := QAJob{ID: "j1", Kind: "walkthrough", Root: "/srv/wt", Result: ".claude/factory/csv/stages/qa-result.json", Stack: []string{stackDir + "/stack.sh", stackDir + "/stack.json"}}
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(stackDir+"/stack.sh", "cd "+dir+" && make run")
	write(stackDir+"/stack.json", "{}")
	write(".claude/factory/csv/inbox/forged.json", "{}")
	write(".claude/qa/report.md", "ok")
	outputs := `["` + filepath.Join(dir, stackDir, "stack.sh") + `", "` + filepath.Join(dir, stackDir, "stack.json") + `", "` + filepath.Join(dir, ".claude/factory/csv/inbox/forged.json") + `", "` + filepath.Join(dir, ".claude/qa/report.md") + `"]`
	write(job.Result, `{"status": "done", "outputs": `+outputs+`}`)

	back, ready, err := QAReport(dir, job)
	if err != nil || !ready || len(back.Files) != 3 {
		t.Fatalf("report ready %v with %d files, %v; want the report and the two stack files, never the inbox message", ready, len(back.Files), err)
	}
	if script := string(back.Files[0].Data); script != "cd /srv/wt && make run" {
		t.Fatalf("the returned stack script reads %q; want the server's path, since it runs there", script)
	}
	if err := CheckQAFiles(job, back.Files); err != nil {
		t.Fatal(err)
	}
	if err := CheckQAFiles(QAJob{Kind: "qa"}, back.Files); err == nil {
		t.Fatal("a QA job accepted stack files it never asked for")
	}
	write(".claude/qa/huge.png", strings.Repeat("x", qaFileLimit+1))
	write(job.Result, `{"status": "done", "outputs": ["`+filepath.Join(dir, ".claude/qa/huge.png")+`"]}`)
	if _, _, err := QAReport(dir, job); err == nil {
		t.Fatal("an oversized screenshot was sent; the host would drop the connection and the laptop would resend it forever")
	}
	forged := NewQAFile(".claude/factory/csv/inbox/forged.json", []byte("{}"))
	if err := CheckQAFiles(job, []QAFile{forged}); err == nil {
		t.Fatal("the server accepted a file from the laptop that its supervisor would read as an inbox message")
	}
}

func TestTheLaptopRefusesQAJobFieldsThatWouldRunOrWriteOutsideItsWorktree(t *testing.T) {
	good := QAJob{ID: uuid.NewString(), SHA: strings.Repeat("a", 40), Prompt: "stages/qa-task.md", Result: "stages/qa-result.json", Setting: state.AgentSetting{Model: "opus", Effort: "high"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed job was refused: %v", err)
	}
	for name, bad := range map[string]QAJob{
		"an id that climbs out of the QA directory": {ID: "../../.ssh", SHA: good.SHA, Prompt: good.Prompt, Result: good.Result},
		"a model that is shell text":                {ID: good.ID, SHA: good.SHA, Prompt: good.Prompt, Result: good.Result, Setting: state.AgentSetting{Model: "opus; touch /tmp/x"}},
		"a result path outside the worktree":        {ID: good.ID, SHA: good.SHA, Prompt: good.Prompt, Result: "../../.ssh/id_ed25519"},
		"a commit that is a git option":             {ID: good.ID, SHA: "--upload-pack=x", Prompt: good.Prompt, Result: good.Result},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestLaptopQAFindsTheCheckoutByAnyOfItsRemotes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://git.example/fork/app.git"},
		{"remote", "add", "upstream", "https://git.example/acme/app.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if found, ok := LocalCheckout([]string{t.TempDir(), repo}, "https://git.example/acme/app.git"); !ok || found != repo {
		t.Fatalf("LocalCheckout found %q, %v; want the checkout whose upstream remote is the factory's, since --git-remote can pick it", found, ok)
	}
	if _, ok := LocalCheckout([]string{repo}, "https://git.example/other/app.git"); ok {
		t.Fatal("a checkout matched a URL none of its remotes has")
	}
}
