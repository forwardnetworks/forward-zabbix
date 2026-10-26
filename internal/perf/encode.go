// Package perf builds Forward performance payloads (PerfDataPB) from interface and device metrics.
//
// The wire format is written by hand from Forward's PerfProtos.proto (message PerfDataPB, DevicePerfPB,
// InterfaceCountersPB) at the commit named in vendoros_gen.go. The schema is small and numbered, so there is no
// protobuf dependency; encode_test.go checks the bytes by decoding them with protoc against Forward's real proto
// whenever protoc and a Forward source checkout are available. Forward can change the proto between releases:
// re-check it before relying on an upgrade.
package perf

import (
	"encoding/binary"
	"math"
	"sort"
	"time"
)

// Counters are cumulative interface counters, as an SNMP poll would read them. Forward diffs consecutive samples
// itself (and handles wrap), so only the deltas matter, not the absolute values.
type Counters struct {
	InOctets, InDiscards, InErrors    uint64
	OutOctets, OutDiscards, OutErrors uint64
	SpeedMbps                         uint32
	InterfaceType                     uint32 // IANA ifType
}

// DevicePerf is one device's metrics at one time.
type DevicePerf struct {
	DeviceName    string
	VendorOS      int32 // VendorOsPB number; see VendorOS
	Time          time.Time
	Interfaces    map[string]Counters // keyed by ifName, as Forward's collector keys them
	UptimeSeconds uint32
	CPU           float32 // fraction 0..1
	Memory        float32 // fraction 0..1
}

// Payload is one upload. Forward compares Time with its own clock, so set it to when the data was collected.
type Payload struct {
	Time    time.Time
	Devices []DevicePerf // keep each device's entries in chronological order
}

type buf struct{ b []byte }

func (w *buf) varint(v uint64)     { w.b = binary.AppendUvarint(w.b, v) }
func (w *buf) tag(field, wire int) { w.varint(uint64(field<<3 | wire)) }
func (w *buf) uint(field int, v uint64) {
	if v != 0 {
		w.tag(field, 0)
		w.varint(v)
	}
}
func (w *buf) float(field int, v float32) {
	if v != 0 {
		w.tag(field, 5)
		w.b = binary.LittleEndian.AppendUint32(w.b, math.Float32bits(v))
	}
}
func (w *buf) bytes(field int, v []byte) {
	w.tag(field, 2)
	w.varint(uint64(len(v)))
	w.b = append(w.b, v...)
}
func (w *buf) str(field int, v string) {
	if v != "" {
		w.bytes(field, []byte(v))
	}
}

func timestamp(t time.Time) []byte {
	var w buf
	w.uint(1, uint64(t.Unix())) // int64 seconds
	w.uint(2, uint64(t.Nanosecond()))
	return w.b
}

func (c Counters) encode() []byte {
	var w buf
	w.uint(1, c.InOctets)
	w.uint(2, c.InDiscards)
	w.uint(3, c.InErrors)
	w.uint(4, c.OutOctets)
	w.uint(5, c.OutDiscards)
	w.uint(6, c.OutErrors)
	w.uint(7, uint64(c.SpeedMbps))
	w.uint(8, uint64(c.InterfaceType))
	return w.b
}

func (d DevicePerf) encode() []byte {
	var w buf
	w.str(1, d.DeviceName)
	w.uint(2, uint64(d.VendorOS)) // enum, a varint
	w.bytes(3, timestamp(d.Time))
	names := make([]string, 0, len(d.Interfaces))
	for n := range d.Interfaces {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names { // map<string, InterfaceCountersPB>: one entry message per key
		var e buf
		e.str(1, n)
		e.bytes(2, d.Interfaces[n].encode())
		w.bytes(4, e.b)
	}
	w.uint(5, uint64(d.UptimeSeconds))
	w.float(6, d.CPU)
	w.float(7, d.Memory)
	return w.b
}

// Encode returns the serialized PerfDataPB.
func (p Payload) Encode() []byte {
	var w buf
	w.bytes(1, timestamp(p.Time))
	for _, d := range p.Devices {
		w.bytes(2, d.encode())
	}
	return w.b
}
