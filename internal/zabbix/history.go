package zabbix

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/perf"
)

// Item key prefixes of Zabbix's stock "Network Generic Device by SNMP" template (and the common CPU and memory keys
// of vendor templates).
var historyKeys = []string{"net.if.in[", "net.if.out[", "net.if.in.errors[", "net.if.in.discards[",
	"net.if.out.errors[", "net.if.out.discards[", "net.if.speed[", "net.if.type[", "system.net.uptime[",
	"system.cpu.util", "vm.memory.util"}

type historyItem struct {
	ItemID    string                        `json:"itemid"`
	Key       string                        `json:"key_"`
	ValueType string                        `json:"value_type"`
	LastValue string                        `json:"lastvalue"`
	Tags      []struct{ Tag, Value string } `json:"tags"`
}

func (h historyItem) iface() string {
	for _, t := range h.Tags {
		if t.Tag == "interface" {
			return t.Value
		}
	}
	return ""
}

// DeviceHistory reads the stored SNMP metrics of one Zabbix host between from and to. It reads only.
func (c *Client) DeviceHistory(ctx context.Context, host string, from, to time.Time) (perf.Device, error) {
	var found []struct {
		HostID string `json:"hostid"`
	}
	if err := c.Call(ctx, "host.get", map[string]any{"output": []string{"hostid"}, "filter": map[string]any{"host": []string{host}}}, &found); err != nil {
		return perf.Device{}, err
	}
	if len(found) == 0 {
		return perf.Device{}, fmt.Errorf("no Zabbix host named %q", host)
	}
	var items []historyItem
	if err := c.Call(ctx, "item.get", map[string]any{"output": []string{"itemid", "key_", "value_type", "lastvalue"},
		"hostids": found[0].HostID, "selectTags": []string{"tag", "value"}, "searchByAny": true, "startSearch": true,
		"search": map[string]any{"key_": historyKeys}}, &items); err != nil {
		return perf.Device{}, err
	}
	// Group by value type so each history.get asks for the right table (0 float, 3 unsigned).
	byType := map[string][]string{}
	for _, it := range items {
		byType[it.ValueType] = append(byType[it.ValueType], it.ItemID)
	}
	series := map[string][]perf.Sample{}
	for vt, ids := range byType {
		var rows []struct {
			ItemID string `json:"itemid"`
			Clock  string `json:"clock"`
			Value  string `json:"value"`
		}
		if err := c.Call(ctx, "history.get", map[string]any{"history": vt, "itemids": ids, "time_from": from.Unix(),
			"time_till": to.Unix(), "output": "extend", "sortfield": "clock", "sortorder": "ASC"}, &rows); err != nil {
			return perf.Device{}, err
		}
		for _, r := range rows {
			t, err1 := strconv.ParseInt(r.Clock, 10, 64)
			v, err2 := strconv.ParseFloat(r.Value, 64)
			if err1 == nil && err2 == nil {
				series[r.ItemID] = append(series[r.ItemID], perf.Sample{T: t, V: v})
			}
		}
	}
	d := perf.Device{Name: host}
	byIface := map[string]*perf.Series{}
	get := func(name string) *perf.Series {
		if byIface[name] == nil {
			byIface[name] = &perf.Series{Name: name}
		}
		return byIface[name]
	}
	for _, it := range items {
		s := series[it.ItemID]
		last, _ := strconv.ParseFloat(it.LastValue, 64)
		switch name := it.iface(); {
		case strings.HasPrefix(it.Key, "net.if.in["):
			get(name).InBps = s
		case strings.HasPrefix(it.Key, "net.if.out["):
			get(name).OutBps = s
		case strings.HasPrefix(it.Key, "net.if.in.errors["):
			get(name).InErrors = s
		case strings.HasPrefix(it.Key, "net.if.in.discards["):
			get(name).InDiscards = s
		case strings.HasPrefix(it.Key, "net.if.out.errors["):
			get(name).OutErrors = s
		case strings.HasPrefix(it.Key, "net.if.out.discards["):
			get(name).OutDiscards = s
		case strings.HasPrefix(it.Key, "net.if.speed["):
			get(name).SpeedBps = last // a rarely-changing item: Zabbix may store no sample in a short window
		case strings.HasPrefix(it.Key, "net.if.type["):
			get(name).IfType = uint32(last)
		case strings.HasPrefix(it.Key, "system.net.uptime["):
			d.Uptime = s
		case strings.HasPrefix(it.Key, "system.cpu.util") && len(d.CPU) == 0:
			d.CPU = s
		case strings.HasPrefix(it.Key, "vm.memory.util") && len(d.Memory) == 0:
			d.Memory = s
		}
	}
	names := make([]string, 0, len(byIface))
	for n := range byIface {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		d.Ifaces = append(d.Ifaces, *byIface[n])
	}
	return d, nil
}
