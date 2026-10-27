// Package topology turns Forward's link list into the "which device does this one depend on" tree that alert
// suppression needs. It is pure: no Forward, no Zabbix.
//
// A device's parent is its immediate dominator from the vantage, the device that sits on every path from the
// vantage to it, nearest to it. Using every neighbour instead would be wrong: Zabbix suppresses a trigger when ANY
// of its dependencies is in problem, so a device with two uplinks must not depend on either one alone.
package topology

import (
	"fmt"
	"sort"
)

// Link is one end-to-end connection between two devices. Forward reports each link from both ends and may report
// parallel links; neither matters here.
type Link struct{ Device, Peer string }

// Tree is the result for one set of vantages.
type Tree struct {
	Vantages []string
	// Parent maps a managed device to the managed device it depends on. A managed device with no managed
	// dominator (the vantage itself, or everything above it is unmanaged) has no entry.
	Parent map[string]string
	// Skipped lists, for a device, the unmanaged devices between it and its parent (nearest first). Only devices
	// where something was skipped have an entry.
	Skipped map[string][]string
	// Unreachable lists managed devices that no link path connects to the vantage.
	Unreachable []string
}

// Build computes the tree. managed is the set of devices that have something to depend on (a Zabbix host); other
// devices in the links still count as part of the paths. Each vantage must appear in the links. With more than one
// vantage (a Zabbix server reaching the network two ways), a device behind both depends on neither: they are joined
// through a virtual root, which nothing can depend on.
//
// Cost is O(n*(n+m)) in devices n and links m, which is fine for a few thousand devices.
func Build(links []Link, vantages []string, managed []string) (Tree, error) {
	adj := map[string]map[string]bool{}
	for _, l := range links {
		if l.Device == "" || l.Peer == "" || l.Device == l.Peer {
			continue
		}
		for _, p := range [][2]string{{l.Device, l.Peer}, {l.Peer, l.Device}} {
			if adj[p[0]] == nil {
				adj[p[0]] = map[string]bool{}
			}
			adj[p[0]][p[1]] = true
		}
	}
	if len(vantages) == 0 {
		return Tree{}, fmt.Errorf("no vantage device given")
	}
	for _, v := range vantages {
		if adj[v] == nil {
			return Tree{}, fmt.Errorf("vantage device %q has no links in Forward's topology", v)
		}
	}
	vantage := vantages[0]
	if len(vantages) > 1 {
		vantage = virtualRoot
		adj[vantage] = map[string]bool{}
		for _, v := range vantages {
			adj[vantage][v] = true
			adj[v][vantage] = true
		}
	}
	isManaged := map[string]bool{}
	for _, m := range managed {
		isManaged[m] = true
	}

	reach := bfs(adj, vantage, "")
	// dominated[x] = devices that lose their path to the vantage when x is removed
	dominated := map[string]map[string]bool{vantage: {}}
	for n := range reach {
		if n != vantage {
			dominated[vantage][n] = true
		}
	}
	for x := range reach {
		if x == vantage {
			continue
		}
		after := bfs(adj, vantage, x)
		d := map[string]bool{}
		for n := range reach {
			if n != x && !after[n] {
				d[n] = true
			}
		}
		dominated[x] = d
	}

	t := Tree{Vantages: vantages, Parent: map[string]string{}, Skipped: map[string][]string{}}
	for _, name := range managed {
		if name == vantage || !reach[vantage] {
			continue
		}
		if !reach[name] {
			t.Unreachable = append(t.Unreachable, name)
			continue
		}
		// dominators of name, nearest first: they form a chain, so the nearest dominates the fewest devices
		var doms []string
		for x, d := range dominated {
			if d[name] {
				doms = append(doms, x)
			}
		}
		sort.Slice(doms, func(i, j int) bool {
			if len(dominated[doms[i]]) != len(dominated[doms[j]]) {
				return len(dominated[doms[i]]) < len(dominated[doms[j]])
			}
			return doms[i] < doms[j]
		})
		var skipped []string
		for _, x := range doms {
			if x == virtualRoot {
				continue // joins the vantages; nothing can depend on it
			}
			if isManaged[x] {
				t.Parent[name] = x
				break
			}
			skipped = append(skipped, x)
		}
		if _, ok := t.Parent[name]; !ok {
			skipped = nil // no managed ancestor at all: nothing was "skipped to reach" a parent
		}
		if len(skipped) > 0 {
			t.Skipped[name] = skipped
		}
	}
	sort.Strings(t.Unreachable)
	return t, nil
}

const virtualRoot = "\x00vantages"

// bfs returns the devices reachable from start without passing through banned.
func bfs(adj map[string]map[string]bool, start, banned string) map[string]bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for p := range adj[n] {
			if p != banned && !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return seen
}
