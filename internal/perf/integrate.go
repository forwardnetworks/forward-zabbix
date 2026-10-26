package perf

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Sample is one stored Zabbix value.
type Sample struct {
	T int64 // unix seconds
	V float64
}

// Series are one interface's stored Zabbix history. Zabbix's stock SNMP template stores traffic as bits per second
// and errors/discards as per-second rates (a "change per second" of the raw counter), not the counters themselves.
type Series struct {
	Name                   string
	InBps, OutBps          []Sample
	InErrors, InDiscards   []Sample // per second
	OutErrors, OutDiscards []Sample // per second
	// Cumulative says the six traffic series are already counters (octets, packets) as polled, not rates.
	Cumulative bool
	SpeedBps   float64
	IfType     uint32
}

// Device is the host-level history for one device.
type Device struct {
	Name     string
	VendorOS int32
	Uptime   []Sample // seconds
	CPU      []Sample // percent 0..100; empty if the host has no CPU item
	Memory   []Sample // percent 0..100; empty if the host has no memory item
	Ifaces   []Series
}

// cumulative turns a rate series into a running total: total(t_j) = total(t_j-1) + rate_j * (t_j - t_j-1) * scale.
// A Zabbix rate stored at t_j is the change over the interval ending at t_j, so this reproduces the counter's
// movement; Forward only uses differences, so the starting value is irrelevant. Integer-typed Zabbix items
// truncate fractional rates, so a very small error rate can read as zero: the one loss in this reconstruction.
func cumulative(rates []Sample, scale float64) []Sample {
	out := make([]Sample, len(rates))
	var total float64
	for i, r := range rates {
		if i > 0 {
			total += math.Max(r.V, 0) * float64(r.T-rates[i-1].T) * scale
		}
		out[i] = Sample{r.T, total}
	}
	return out
}

// at returns the latest sample at or before t.
func at(s []Sample, t int64) (float64, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].T > t })
	if i == 0 {
		return 0, false
	}
	return s[i-1].V, true
}

func u64(v float64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(math.Round(v))
}

// clusterGap is how far apart (seconds) two interface samples can be and still belong to one polling cycle: the
// items of one SNMP cycle are stamped a few seconds apart.
const clusterGap = 20

// ticksOf groups all interface-traffic sample times into polling cycles and returns each cycle's latest time. The
// counters only move once per cycle, so a tick must too: Forward divides the counter difference between two samples
// by the time between them. With no interface history it falls back to the uptime timestamps.
func ticksOf(d Device) []int64 {
	var times []int64
	for _, s := range d.Ifaces {
		for _, x := range s.InBps {
			times = append(times, x.T)
		}
		for _, x := range s.OutBps {
			times = append(times, x.T)
		}
	}
	if len(times) == 0 {
		for _, x := range d.Uptime {
			times = append(times, x.T)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	var ticks []int64
	for i, t := range times {
		if i+1 < len(times) && times[i+1]-t <= clusterGap {
			continue // a later sample of the same cycle follows; that one ends the cycle
		}
		ticks = append(ticks, t)
	}
	return ticks
}

// Build returns one DevicePerf per polling cycle, oldest first; each interface's counters are the running totals as
// of that tick. The first tick is the baseline: Forward computes no rates for it.
func Build(d Device) []DevicePerf {
	ticks := ticksOf(d)
	type cum struct {
		name                    string
		in, out, ie, id, oe, od []Sample
		speed                   uint32
		ifType                  uint32
	}
	var cs []cum
	for _, s := range d.Ifaces {
		c := func(r []Sample, scale float64) []Sample {
			if s.Cumulative {
				return r
			}
			return cumulative(r, scale)
		}
		cs = append(cs, cum{s.Name, c(s.InBps, 1.0/8), c(s.OutBps, 1.0/8),
			c(s.InErrors, 1), c(s.InDiscards, 1), c(s.OutErrors, 1), c(s.OutDiscards, 1),
			uint32(s.SpeedBps / 1e6), s.IfType})
	}
	out := make([]DevicePerf, 0, len(ticks))
	for _, t := range ticks {
		up, _ := at(d.Uptime, t)
		dp := DevicePerf{DeviceName: d.Name, VendorOS: d.VendorOS, Time: time.Unix(t, 0), UptimeSeconds: uint32(math.Max(up, 0)),
			Interfaces: map[string]Counters{}}
		if v, ok := at(d.CPU, t); ok {
			dp.CPU = float32(v / 100)
		}
		if v, ok := at(d.Memory, t); ok {
			dp.Memory = float32(v / 100)
		}
		for _, c := range cs {
			in, ok := at(c.in, t)
			if !ok {
				continue // no traffic history for this interface yet
			}
			out, _ := at(c.out, t)
			ie, _ := at(c.ie, t)
			id, _ := at(c.id, t)
			oe, _ := at(c.oe, t)
			od, _ := at(c.od, t)
			dp.Interfaces[c.name] = Counters{InOctets: u64(in), OutOctets: u64(out), InErrors: u64(ie), InDiscards: u64(id),
				OutErrors: u64(oe), OutDiscards: u64(od), SpeedMbps: c.speed, InterfaceType: c.ifType}
		}
		out = append(out, dp)
	}
	return out
}

// StorageRow is one hrStorageTable row's history: allocation unit (bytes), size and used (in units).
type StorageRow struct{ Unit, Size, Used []Sample }

var (
	ramRow    = regexp.MustCompile(`(?i)^(ram|physical memory|memory)$`)
	cacheRows = regexp.MustCompile(`(?i)\((buffers|cache|cached)\)|^(buffers|cached)$`)
)

// Memory returns the fraction of RAM in use over time (0..1) from hrStorage rows keyed by hrStorageDescr. Linux-based
// network operating systems report page cache and buffers as "used"; those rows ("RAM (Cache)", "RAM (Buffers)") are
// subtracted so the figure matches what the device itself calls memory use. With no RAM row it returns nil.
func Memory(rows map[string]StorageRow) []Sample {
	var ram StorageRow
	var found bool
	for d, r := range rows {
		if ramRow.MatchString(strings.TrimSpace(d)) {
			ram, found = r, true
		}
	}
	if !found {
		return nil
	}
	var out []Sample
	for _, u := range ram.Used {
		unit, ok1 := at(ram.Unit, u.T)
		size, ok2 := at(ram.Size, u.T)
		if !ok1 || !ok2 || size <= 0 {
			continue
		}
		used := u.V * unit
		for d, r := range rows {
			if !cacheRows.MatchString(strings.TrimSpace(d)) {
				continue
			}
			cu, _ := at(r.Unit, u.T)
			if v, ok := at(r.Used, u.T); ok {
				used -= v * cu
			}
		}
		f := used / (size * unit)
		out = append(out, Sample{u.T, math.Min(math.Max(f, 0), 1) * 100}) // percent, as Build expects
	}
	return out
}

// AverageSeries averages several series (for example one per CPU core) at each timestamp that any has a sample,
// using each series' latest value at or before that time.
func AverageSeries(series [][]Sample) []Sample {
	var times []int64
	seen := map[int64]bool{}
	for _, s := range series {
		for _, x := range s {
			if !seen[x.T] {
				seen[x.T] = true
				times = append(times, x.T)
			}
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	var out []Sample
	for _, t := range times {
		var sum float64
		var n int
		for _, s := range series {
			if v, ok := at(s, t); ok {
				sum += v
				n++
			}
		}
		if n > 0 {
			out = append(out, Sample{t, sum / float64(n)})
		}
	}
	return out
}
