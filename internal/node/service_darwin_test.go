package node

import (
	"context"
	"testing"
)

func TestLaunchdStateIgnoresNestedCoalitionState(t *testing.T) {
	printed := "gui/501/dev.factory.node = {\n" +
		"\tactive count = 1\n" +
		"\tstate = running\n" +
		"\tpid = 47944\n" +
		"\tresource coalition = {\n" +
		"\t\tID = 1234\n" +
		"\t\tstate = active\n" +
		"\t}\n" +
		"\tjetsam coalition = {\n" +
		"\t\tstate = active\n" +
		"\t}\n" +
		"}\n"
	svc := Service{Home: t.TempDir(), Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(printed), nil
	}}
	st := svc.State(context.Background())
	if !st.Running || st.PID != "47944" {
		t.Fatalf("State() = %+v; want running with pid 47944", st)
	}
}
