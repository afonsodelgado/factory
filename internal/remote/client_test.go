package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	sent=$(printf '%s' "$req" | sed -n 's/.*"revision":\([0-9]*\).*/\1/p')
	have=$(cat "$FACTORY_TEST_PUSH" 2>/dev/null || echo 0)
	if [ "$sent" -gt "$have" ]; then
		echo "$sent" >"$FACTORY_TEST_PUSH"
		have=$sent
	fi
	echo '{"protocol":1,"server":"s1","status":"completed","data":'"$have"'}'
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

func TestPushSettingsReachAServerThatCountsAheadOfThisLaptop(t *testing.T) {
	bin, work := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(keepsRevisionSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	serverRevision := filepath.Join(work, "revision")
	t.Setenv("FACTORY_TEST_PUSH", serverRevision)
	if err := os.WriteFile(serverRevision, []byte("5\n"), 0o600); err != nil {
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
	if got, _ := os.ReadFile(serverRevision); strings.TrimSpace(string(got)) != "6" {
		t.Fatalf("the server counts %q; want the rotated setting numbered past its own 5", got)
	}
	if PushPending(short, "homelab", push) {
		t.Fatal("the laptop still lists the setting as pending after the server took it")
	}
}
