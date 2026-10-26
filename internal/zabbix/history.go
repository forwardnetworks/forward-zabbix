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
	"system.cpu.util", "vm.memory.util", "fwd.if.", "fwd.cpu.", "fwd.mem.", "fwd.uptime"}

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
	var cores [][]perf.Sample
	storage := map[string]perf.StorageRow{}
	for _, it := range items { // the template in zabbix/forward-performance-template.yaml: raw counters, preferred
		s := series[it.ItemID]
		last, _ := strconv.ParseFloat(it.LastValue, 64)
		switch {
		case strings.HasPrefix(it.Key, "fwd.uptime"):
			d.Uptime = s
		case strings.HasPrefix(it.Key, "fwd.cpu.core["):
			cores = append(cores, s)
		case strings.HasPrefix(it.Key, "fwd.mem."):
			kind, descr := keyKindAndParam(it.Key)
			row := storage[descr]
			switch kind {
			case "unit":
				row.Unit = s
			case "size":
				row.Size = s
			case "used":
				row.Used = s
			}
			storage[descr] = row
		case strings.HasPrefix(it.Key, "fwd.if.") && !strings.HasPrefix(it.Key, "fwd.if.walk") && !strings.HasPrefix(it.Key, "fwd.if.discovery"):
			ser := get(it.iface())
			ser.Cumulative = true
			switch kind, _ := keyKindAndParam(it.Key); kind {
			case "in.octets":
				ser.InBps = s
			case "out.octets":
				ser.OutBps = s
			case "in.errors":
				ser.InErrors = s
			case "out.errors":
				ser.OutErrors = s
			case "in.discards":
				ser.InDiscards = s
			case "out.discards":
				ser.OutDiscards = s
			case "speed":
				ser.SpeedBps = last * 1e6 // ifHighSpeed is Mbit/s
			case "type":
				ser.IfType = uint32(last)
			}
		}
	}
	if len(cores) > 0 {
		d.CPU = perf.AverageSeries(cores)
	}
	if len(storage) > 0 {
		d.Memory = perf.Memory(storage)
	}
	for _, it := range items {
		if strings.HasPrefix(it.Key, "fwd.") {
			continue
		}
		s := series[it.ItemID]
		last, _ := strconv.ParseFloat(it.LastValue, 64)
		switch name := it.iface(); {
		case get(name).Cumulative && strings.HasPrefix(it.Key, "net.if."):
			// the template's raw counters are already in use for this interface
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
		case strings.HasPrefix(it.Key, "system.net.uptime[") && len(d.Uptime) == 0:
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

// keyKindAndParam splits "fwd.if.in.octets[eth1]" into the part after the family prefix ("in.octets") and the
// bracketed parameter ("eth1"). The family is fwd.if., fwd.cpu. or fwd.mem.
func keyKindAndParam(key string) (kind, param string) {
	open := strings.Index(key, "[")
	head := key
	if open >= 0 {
		head, param = key[:open], strings.TrimSuffix(key[open+1:], "]")
		param = strings.Trim(param, `"`)
	}
	for _, fam := range []string{"fwd.if.", "fwd.cpu.", "fwd.mem."} {
		if strings.HasPrefix(head, fam) {
			return strings.TrimPrefix(head, fam), param
		}
	}
	return head, param
}
