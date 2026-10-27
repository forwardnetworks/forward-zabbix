package topology

import (
	"reflect"
	"strings"
	"testing"
)

func links(pairs ...string) []Link {
	var out []Link
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Link{pairs[i], pairs[i+1]})
	}
	return out
}

// Each case states the requirement in words: a device depends on the one device every path to it must cross.
func TestBuild(t *testing.T) {
	cases := []struct {
		name        string
		links       []Link
		managed     []string
		parent      map[string]string
		skipped     map[string][]string
		unreachable []string
	}{
		{
			name:    "chain: each device depends on the one before it",
			links:   links("a", "b", "b", "c", "c", "d"),
			managed: []string{"a", "b", "c", "d"},
			parent:  map[string]string{"b": "a", "c": "b", "d": "c"},
		},
		{
			name:    "diamond: d has two ways home, so it depends on neither b nor c but on a",
			links:   links("a", "b", "a", "c", "b", "d", "c", "d"),
			managed: []string{"a", "b", "c", "d"},
			parent:  map[string]string{"b": "a", "c": "a", "d": "a"},
		},
		{
			name:    "ring: nobody is behind anybody else",
			links:   links("a", "b", "b", "c", "c", "d", "d", "a"),
			managed: []string{"a", "b", "c", "d"},
			parent:  map[string]string{"b": "a", "c": "a", "d": "a"},
		},
		{
			name:    "dual-homed leaf behind a ring keeps the ring's entry point, not one uplink",
			links:   links("a", "b", "a", "c", "b", "e", "c", "e", "e", "leaf"),
			managed: []string{"a", "b", "c", "e", "leaf"},
			parent:  map[string]string{"b": "a", "c": "a", "e": "a", "leaf": "e"},
		},
		{
			name:    "parallel links and self-loops change nothing",
			links:   links("a", "b", "b", "a", "a", "b", "b", "b", "b", "c"),
			managed: []string{"a", "b", "c"},
			parent:  map[string]string{"b": "a", "c": "b"},
		},
		{
			name:        "a device no link path reaches is reported, not given a parent",
			links:       links("a", "b", "x", "y"),
			managed:     []string{"a", "b", "x"},
			parent:      map[string]string{"b": "a"},
			unreachable: []string{"x"},
		},
		{
			name:    "unmanaged parent: the child depends on the nearest managed device above it",
			links:   links("a", "b", "b", "c"),
			managed: []string{"a", "c"},
			parent:  map[string]string{"c": "a"},
			skipped: map[string][]string{"c": {"b"}},
		},
		{
			name:    "unmanaged vantage: the devices right under it are roots",
			links:   links("a", "b", "b", "c"),
			managed: []string{"b", "c"},
			parent:  map[string]string{"c": "b"},
		},
		{
			name:    "a managed device with nothing managed above it is a root and skips nothing",
			links:   links("a", "b", "b", "c"),
			managed: []string{"c"},
			parent:  map[string]string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Build(c.links, []string{"a"}, c.managed)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Parent, c.parent) {
				t.Errorf("parent = %v, want %v", got.Parent, c.parent)
			}
			wantSkipped := c.skipped
			if wantSkipped == nil {
				wantSkipped = map[string][]string{}
			}
			if !reflect.DeepEqual(got.Skipped, wantSkipped) {
				t.Errorf("skipped = %v, want %v", got.Skipped, wantSkipped)
			}
			if !reflect.DeepEqual(got.Unreachable, c.unreachable) {
				t.Errorf("unreachable = %v, want %v", got.Unreachable, c.unreachable)
			}
		})
	}
}

func TestBuildVantageMustHaveLinks(t *testing.T) {
	_, err := Build(links("a", "b"), []string{"nope"}, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want an error naming the vantage, got %v", err)
	}
}

// A dependency cycle would be rejected by Zabbix, so Build must never produce one.
func TestBuildNeverCycles(t *testing.T) {
	g := links("a", "b", "b", "c", "c", "d", "d", "b", "d", "e", "e", "f", "f", "a", "c", "g")
	all := []string{"a", "b", "c", "d", "e", "f", "g"}
	tr, err := Build(g, []string{"a"}, all)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range all {
		seen := map[string]bool{}
		for p, ok := tr.Parent[n]; ok; p, ok = tr.Parent[p] {
			if seen[p] || p == n {
				t.Fatalf("cycle through %s: %v", n, tr.Parent)
			}
			seen[p] = true
		}
	}
}

// Two ways in: a device behind both vantages must not depend on just one of them.
func TestBuildTwoVantages(t *testing.T) {
	g := links("v1", "x", "v2", "x", "x", "leaf", "v1", "only1")
	tr, err := Build(g, []string{"v1", "v2"}, []string{"v1", "v2", "x", "leaf", "only1"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"leaf": "x", "only1": "v1"}
	if !reflect.DeepEqual(tr.Parent, want) {
		t.Errorf("parent = %v, want %v", tr.Parent, want)
	}
}

func TestBuildNoVantage(t *testing.T) {
	if _, err := Build(links("a", "b"), nil, []string{"a", "b"}); err == nil {
		t.Fatal("want an error when no vantage is given")
	}
}
