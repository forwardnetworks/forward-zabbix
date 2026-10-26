package perf

import "testing"

// A counter that moves at 8000 bit/s (= 1000 octets/s) for 3 minutes, sampled every 60 s, must advance by
// 60000 octets per tick once the first sample (the baseline) is passed. Derived from the definition of the rate.
func TestCumulativeReconstructsCounterMovementFromRates(t *testing.T) {
	rates := []Sample{{0, 0}, {60, 8000}, {120, 8000}, {180, 8000}}
	got := cumulative(rates, 1.0/8)
	want := []float64{0, 60000, 120000, 180000}
	for i := range want {
		if got[i].V != want[i] {
			t.Fatalf("tick %d: %v, want %v", i, got[i].V, want[i])
		}
	}
}

func TestBuildEmitsOneEntryPerUptimeTickWithMonotonicCountersAndFractionalCPU(t *testing.T) {
	d := Device{Name: "r1", VendorOS: 1,
		Uptime: []Sample{{0, 100}, {60, 160}, {120, 220}},
		CPU:    []Sample{{0, 50}, {120, 25}},
		Ifaces: []Series{{Name: "Ethernet1", InBps: []Sample{{0, 0}, {60, 8000}, {120, 16000}},
			OutBps: []Sample{{0, 0}, {60, 0}, {120, 0}}, InErrors: []Sample{{0, 0}, {60, 2}, {120, 0}},
			SpeedBps: 1e9, IfType: 6}}}
	got := Build(d)
	if len(got) != 3 || got[0].UptimeSeconds != 100 || got[2].UptimeSeconds != 220 {
		t.Fatalf("ticks: %+v", got)
	}
	if got[0].CPU != 0.5 || got[2].CPU != 0.25 {
		t.Errorf("cpu = %v, %v; want fractions 0.5 and 0.25", got[0].CPU, got[2].CPU)
	}
	e := func(i int) Counters { return got[i].Interfaces["Ethernet1"] }
	if e(0).InOctets != 0 || e(1).InOctets != 60000 || e(2).InOctets != 180000 { // 60s@1000 B/s, then 60s@2000 B/s
		t.Errorf("in octets: %d %d %d", e(0).InOctets, e(1).InOctets, e(2).InOctets)
	}
	if e(1).InErrors != 120 || e(2).InErrors != 120 { // 2/s for 60 s, then 0/s
		t.Errorf("in errors: %d %d", e(1).InErrors, e(2).InErrors)
	}
	if e(1).SpeedMbps != 1000 || e(1).InterfaceType != 6 {
		t.Errorf("speed %d type %d", e(1).SpeedMbps, e(1).InterfaceType)
	}
}

func TestAnInterfaceWithNoHistoryYetIsOmittedNotReportedAsZeroTraffic(t *testing.T) {
	d := Device{Name: "r1", Uptime: []Sample{{0, 1}, {60, 2}},
		Ifaces: []Series{{Name: "early", InBps: []Sample{{0, 5}, {60, 5}}}, {Name: "late", InBps: []Sample{{60, 5}}}}}
	got := Build(d)
	if len(got) != 2 {
		t.Fatalf("ticks: %d", len(got))
	}
	if _, ok := got[0].Interfaces["late"]; ok {
		t.Fatal("reported an interface before it had any sample")
	}
	if _, ok := got[1].Interfaces["late"]; !ok {
		t.Fatal("dropped an interface that has a sample")
	}
}

func TestNegativeRatesNeverMoveACounterBackwards(t *testing.T) {
	got := cumulative([]Sample{{0, 0}, {60, -5}, {120, 10}}, 1)
	if got[1].V != 0 || got[2].V != 600 {
		t.Fatalf("%+v", got)
	}
}

// Uptime is polled every 30 s but traffic every 180 s. Forward turns the difference between consecutive samples into
// a rate over the time between them, so a tick must exist only when the counters moved: one every 180 s, not six
// of which five repeat the same counters (which would read as zero traffic then a 6x spike).
func TestTicksFollowTheInterfacePollingCycleNotTheFasterUptimeItem(t *testing.T) {
	var uptime, in, out []Sample
	for i := int64(0); i <= 6; i++ {
		uptime = append(uptime, Sample{i * 30, float64(1000 + i*30)})
	}
	for _, at := range []int64{0, 180} {
		in = append(in, Sample{at, 8000})    // 1000 octets/s
		out = append(out, Sample{at + 4, 0}) // the second interface item is polled a few seconds later
	}
	got := Build(Device{Name: "r", Uptime: uptime, Ifaces: []Series{{Name: "e1", InBps: in}, {Name: "e2", InBps: out}}})
	if len(got) != 2 {
		t.Fatalf("got %d ticks, want 2 (one per polling cycle)", len(got))
	}
	if got[1].Time.Unix()-got[0].Time.Unix() < 170 {
		t.Fatalf("ticks %d s apart", got[1].Time.Unix()-got[0].Time.Unix())
	}
	if got[1].Interfaces["e1"].InOctets-got[0].Interfaces["e1"].InOctets != 180000 {
		t.Fatalf("counter moved %d octets over the cycle, want 180000 (1000 B/s for 180 s)",
			got[1].Interfaces["e1"].InOctets-got[0].Interfaces["e1"].InOctets)
	}
	if _, ok := got[1].Interfaces["e2"]; !ok {
		t.Fatal("the interface polled a few seconds later was dropped from its cycle")
	}
}

func TestWithNoTrafficHistoryTicksFallBackToUptime(t *testing.T) {
	got := Build(Device{Name: "r", Uptime: []Sample{{0, 1}, {30, 2}, {60, 3}}})
	if len(got) != 3 {
		t.Fatalf("got %d ticks", len(got))
	}
}
