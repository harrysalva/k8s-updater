// Package diff computes the difference between a pre-upgrade Report and a
// post-upgrade Report so the UI can surface what was resolved vs newly broken.
package diff

import (
	"upgrade-guardian/internal/checker"
)

// Result is the delta between two Reports keyed by checker name.
type Result struct {
	Pre       *checker.Report   `json:"pre"`
	Post      *checker.Report   `json:"post"`
	Resolved  []checker.Finding `json:"resolved"`   // existed in pre, gone in post
	New       []checker.Finding `json:"new"`        // absent in pre, present in post
	Unchanged []checker.Finding `json:"unchanged"`  // present in both
	Summary   Summary           `json:"summary"`
}

// Summary contains aggregate counters for quick display.
type Summary struct {
	ResolvedTotal     int `json:"resolved_total"`
	NewTotal          int `json:"new_total"`
	NewBlockers       int `json:"new_blockers"`
	UnchangedBlockers int `json:"unchanged_blockers"`
	Improved          bool `json:"improved"` // resolved > new, no new blockers
}

// Compute returns a Result built by comparing pre vs post finding sets.
// Findings are matched by (checker_name, title, resource) since the engine
// does not assign stable IDs.
func Compute(pre, post *checker.Report) *Result {
	preIndex := indexFindings(pre)
	postIndex := indexFindings(post)

	r := &Result{Pre: pre, Post: post}

	for k, f := range preIndex {
		if _, still := postIndex[k]; still {
			r.Unchanged = append(r.Unchanged, f)
		} else {
			r.Resolved = append(r.Resolved, f)
		}
	}
	for k, f := range postIndex {
		if _, existed := preIndex[k]; !existed {
			r.New = append(r.New, f)
		}
	}

	r.Summary.ResolvedTotal = len(r.Resolved)
	r.Summary.NewTotal = len(r.New)
	for _, f := range r.New {
		if f.Blocker {
			r.Summary.NewBlockers++
		}
	}
	for _, f := range r.Unchanged {
		if f.Blocker {
			r.Summary.UnchangedBlockers++
		}
	}
	r.Summary.Improved = r.Summary.ResolvedTotal > r.Summary.NewTotal && r.Summary.NewBlockers == 0
	return r
}

// indexFindings flattens a Report's findings into a map keyed by a stable string.
func indexFindings(rep *checker.Report) map[string]checker.Finding {
	out := map[string]checker.Finding{}
	if rep == nil {
		return out
	}
	for _, res := range rep.Results {
		for _, f := range res.Findings {
			out[findingKey(f)] = f
		}
	}
	return out
}

func findingKey(f checker.Finding) string {
	key := f.CheckerName + "|" + f.Title
	if f.Resource != nil {
		key += "|" + f.Resource.Kind + "/" + f.Resource.Namespace + "/" + f.Resource.Name
	}
	return key
}
