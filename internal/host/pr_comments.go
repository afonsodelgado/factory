package host

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/afonsodelgado/factory/internal/state"
)

type PRComment struct {
	Key         string `json:"key"`
	ID          string `json:"id"`
	PR          int    `json:"pr"`
	Kind        string `json:"kind"`
	Thread      string `json:"thread,omitempty"`
	CanResolve  bool   `json:"can_resolve"`
	Body        string `json:"body"`
	Author      string `json:"author"`
	URL         string `json:"url"`
	Disposition string `json:"disposition,omitempty"`
}

type CommentSnapshot struct {
	Evidence   state.Verification `json:"evidence"`
	Comments   []PRComment        `json:"comments"`
	Unresolved []string           `json:"unresolved"`
}

type reviewThread struct {
	ID         string `json:"id"`
	Resolved   bool   `json:"isResolved"`
	CanResolve bool   `json:"viewerCanResolve"`
	Comments   struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	} `json:"comments"`
}

func (g *GitHub) Comments(dir string, s state.State, now time.Time) (CommentSnapshot, error) {
	result := CommentSnapshot{Evidence: state.Verification{Status: "pass", Heads: s.Stack, RecipeDigest: s.Recipe.Digest, At: state.Time{Time: now}}}
	if !s.WatchComments || s.Evidence.CommentsStopped {
		result.Evidence.Status = "skipped"
		return result, nil
	}
	var repo struct {
		Name string `json:"nameWithOwner"`
	}
	data, err := g.Run(dir, "repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return CommentSnapshot{}, err
	}
	if err := json.Unmarshal(data, &repo); err != nil {
		return CommentSnapshot{}, err
	}
	parts := strings.Split(repo.Name, "/")
	if len(parts) != 2 || len(s.PRs) == 0 {
		return CommentSnapshot{}, errors.New("comments require a published repository")
	}
	for _, pr := range s.PRs {
		if err := g.commentHeads(dir, pr); err != nil {
			return CommentSnapshot{}, err
		}
		query := `query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){reviewThreads(first:100,after:$endCursor){nodes{id isResolved viewerCanResolve comments(first:1){nodes{id}}} pageInfo{hasNextPage endCursor}}}}}`
		data, err := g.Run(dir, "api", "graphql", "--paginate", "--slurp", "-f", "query="+query, "-f", "owner="+parts[0], "-f", "repo="+parts[1], "-F", fmt.Sprint("number=", pr.Number))
		if err != nil {
			return CommentSnapshot{}, err
		}
		var pages []struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Repository struct {
					PR struct {
						Threads struct {
							Nodes    []reviewThread `json:"nodes"`
							PageInfo struct {
								HasNext bool `json:"hasNextPage"`
							} `json:"pageInfo"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &pages); err != nil {
			return CommentSnapshot{}, err
		}
		if len(pages) == 0 || pages[len(pages)-1].Data.Repository.PR.Threads.PageInfo.HasNext {
			return CommentSnapshot{}, errors.New("incomplete review thread pagination")
		}
		threads := map[string]reviewThread{}
		for _, page := range pages {
			if len(page.Errors) > 0 || page.Data.Repository.PR.Threads.Nodes == nil {
				return CommentSnapshot{}, errors.New("incomplete review threads")
			}
			for _, thread := range page.Data.Repository.PR.Threads.Nodes {
				if thread.ID == "" || len(thread.Comments.Nodes) != 1 {
					return CommentSnapshot{}, errors.New("incomplete review thread identity")
				}
				threads[thread.Comments.Nodes[0].ID] = thread
				if !thread.Resolved {
					result.Unresolved = append(result.Unresolved, thread.ID)
				}
			}
		}
		prefix := fmt.Sprintf("repos/%s/", repo.Name)
		for _, source := range []struct{ kind, path string }{
			{"thread", fmt.Sprintf("pulls/%d/comments", pr.Number)},
			{"review", fmt.Sprintf("pulls/%d/reviews", pr.Number)},
			{"comment", fmt.Sprintf("issues/%d/comments", pr.Number)},
		} {
			var pages [][]struct {
				ID      int64  `json:"id"`
				NodeID  string `json:"node_id"`
				ReplyTo int64  `json:"in_reply_to_id"`
				Body    string `json:"body"`
				URL     string `json:"html_url"`
				User    struct {
					Login string `json:"login"`
				} `json:"user"`
			}
			if err := g.api(dir, prefix+source.path+"?per_page=100", &pages, true); err != nil {
				return CommentSnapshot{}, err
			}
			if len(pages) == 0 {
				return CommentSnapshot{}, errors.New("incomplete comment pagination")
			}
			rootIDs := map[int64]string{}
			for _, page := range pages {
				for _, c := range page {
					rootIDs[c.ID] = c.NodeID
				}
			}
			for _, page := range pages {
				if page == nil {
					return CommentSnapshot{}, errors.New("missing comments page")
				}
				for _, c := range page {
					if c.NodeID == "" {
						return CommentSnapshot{}, errors.New("comment identity missing")
					}
					if strings.TrimSpace(c.Body) == "" {
						continue
					}
					comment := PRComment{ID: c.NodeID, PR: pr.Number, Kind: source.kind, Body: c.Body, Author: c.User.Login, URL: c.URL}
					if source.kind == "thread" {
						root := c.NodeID
						if c.ReplyTo != 0 {
							root = rootIDs[c.ReplyTo]
						}
						thread, ok := threads[root]
						if !ok {
							return CommentSnapshot{}, errors.New("review thread changed during refresh")
						}
						if thread.Resolved {
							continue
						}
						comment.Thread, comment.CanResolve = thread.ID, thread.CanResolve
					}
					comment.Key = fmt.Sprintf("%s:%x", c.NodeID, sha256.Sum256([]byte(c.Body)))
					comment.Disposition = s.Evidence.CommentDispositions[comment.Key]
					result.Comments = append(result.Comments, comment)
					if comment.Disposition != "handled" && comment.Disposition != "declined" {
						result.Evidence.Status = "pending"
					}
				}
			}
		}
		if err := g.commentHeads(dir, pr); err != nil {
			return CommentSnapshot{}, err
		}
	}
	if len(result.Unresolved) > 0 {
		result.Evidence.Status = "pending"
	}
	return result, nil
}

func (g *GitHub) commentHeads(dir string, pr state.PR) error {
	data, err := g.Run(dir, "pr", "view", fmt.Sprint(pr.Number), "--json", "headRefOid,baseRefOid")
	if err != nil {
		return err
	}
	var heads struct {
		Head string `json:"headRefOid"`
		Base string `json:"baseRefOid"`
	}
	if err := json.Unmarshal(data, &heads); err != nil {
		return err
	}
	if heads.Head == "" || heads.Base == "" || heads.Head != pr.HeadSHA || heads.Base != pr.BaseSHA {
		return errors.New("comment refresh heads changed")
	}
	return nil
}
