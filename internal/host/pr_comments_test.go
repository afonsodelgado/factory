package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

func TestCommentsRequireCompletePaginationAndPreserveHandledIdentities(t *testing.T) {
	calls, resolved, truncated, edited := 0, false, false, false
	gh := &GitHub{Run: func(_ string, args ...string) ([]byte, error) {
		calls++
		command := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(command, "repo view"):
			return []byte(`{"nameWithOwner":"owner/repo"}`), nil
		case strings.HasPrefix(command, "pr view"):
			return []byte(`{"headRefOid":"h","baseRefOid":"b"}`), nil
		case strings.HasPrefix(command, "api graphql"):
			if !strings.Contains(command, "--paginate --slurp") {
				t.Fatal("threads not paginated")
			}
			body := `[ {"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"thread","isResolved":RESOLVED,"viewerCanResolve":false,"comments":{"nodes":[{"id":"root"}]}}],"pageInfo":{"hasNextPage":true}}}}}}, {"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":TRUNCATED}}}}}} ]`
			body = strings.ReplaceAll(body, "RESOLVED", map[bool]string{true: "true", false: "false"}[resolved])
			body = strings.ReplaceAll(body, "TRUNCATED", map[bool]string{true: "true", false: "false"}[truncated])
			return []byte(body), nil
		default:
			if !strings.HasSuffix(command, "--paginate --slurp") {
				t.Fatal("comments not paginated")
			}
			switch {
			case strings.Contains(command, "pulls/1/comments"):
				return []byte(`[[{"id":1,"node_id":"root","body":"Fix export","user":{"login":"human"}}],[{"id":2,"node_id":"reply","in_reply_to_id":1,"body":"Still broken","user":{"login":"another-human"}}]]`), nil
			case strings.Contains(command, "pulls/1/reviews"):
				return []byte(`[[{"node_id":"review","body":"Check concurrency","user":{"login":"bot"}}]]`), nil
			case strings.Contains(command, "issues/1/comments"):
				body := "General feedback"
				if edited {
					body = "Edited feedback"
				}
				data, _ := json.Marshal([][]map[string]any{{}, {{"node_id": "issue", "body": body, "user": map[string]string{"login": "reviewer"}}}})
				return data, nil
			}
		}
		t.Fatalf("unexpected API call %s", command)
		return nil, nil
	}}
	s := state.State{SchemaVersion: 2, PRs: []state.PR{{Number: 1, HeadSHA: "h", BaseSHA: "b"}}}
	if result, err := gh.Comments("", s, time.Now()); err != nil || result.Evidence.Status != "skipped" || calls != 0 {
		t.Fatal(result, err, calls)
	}
	s.WatchComments = true
	result, err := gh.Comments("", s, time.Now())
	if err != nil || result.Evidence.Status != "pending" || len(result.Comments) != 4 || len(result.Unresolved) != 1 {
		t.Fatal(result, err)
	}
	s.Evidence.CommentDispositions = map[string]string{}
	for _, c := range result.Comments {
		s.Evidence.CommentDispositions[c.Key] = "handled"
	}
	result, err = gh.Comments("", s, time.Now())
	if err != nil || result.Evidence.Status != "pending" {
		t.Fatal("unresolvable human thread passed", result, err)
	}
	for _, c := range result.Comments {
		if c.Disposition != "handled" {
			t.Fatal("lost reply disposition", c)
		}
	}
	resolved = true
	result, err = gh.Comments("", s, time.Now())
	if err != nil || result.Evidence.Status != "pass" {
		t.Fatal(result, err)
	}
	edited = true
	result, err = gh.Comments("", s, time.Now())
	if err != nil || result.Evidence.Status != "pending" {
		t.Fatal("edited feedback was treated as handled", result, err)
	}
	truncated = true
	if _, err := gh.Comments("", s, time.Now()); err == nil {
		t.Fatal("truncated threads passed")
	}
	s.Evidence.CommentsStopped = true
	before := calls
	if _, err := gh.Comments("", s, time.Now()); err != nil || calls != before {
		t.Fatal("stopped watching fetched content", err)
	}
}
