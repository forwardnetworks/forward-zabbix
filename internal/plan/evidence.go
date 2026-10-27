package plan

import (
	"sort"
	"strings"
)

// Verdicts of a change judged against what was expected to change.
const (
	VerdictAsExpected    = "as expected"
	VerdictDiffers       = "differs from expectation"
	VerdictNoExpectation = "no expectation given"
)

// Judgement compares the devices whose configuration changed against the ones that were meant to change.
type Judgement struct {
	Changed    []string `json:"changed"`
	Expected   []string `json:"expected"`
	Unexpected []string `json:"unexpected"` // changed, but not expected to
	Missing    []string `json:"missing"`    // expected to change, but did not
	Verdict    string   `json:"verdict"`
}

// Judge compares by device name, ignoring case as Forward does. With nothing expected there is nothing to judge
// against, so the verdict says that instead of calling every change unexpected.
func Judge(changed, expected []string) Judgement {
	j := Judgement{Changed: sorted(changed), Expected: sorted(expected), Unexpected: []string{}, Missing: []string{}}
	if len(expected) == 0 {
		j.Verdict = VerdictNoExpectation
		return j
	}
	want, got := map[string]bool{}, map[string]bool{}
	for _, e := range j.Expected {
		want[fold(e)] = true
	}
	for _, c := range j.Changed {
		got[fold(c)] = true
		if !want[fold(c)] {
			j.Unexpected = append(j.Unexpected, c)
		}
	}
	for _, e := range j.Expected {
		if !got[fold(e)] {
			j.Missing = append(j.Missing, e)
		}
	}
	j.Verdict = VerdictAsExpected
	if len(j.Unexpected) > 0 || len(j.Missing) > 0 {
		j.Verdict = VerdictDiffers
	}
	return j
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
