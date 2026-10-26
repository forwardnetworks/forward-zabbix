package perf

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Expected bytes worked out by hand from PerfProtos.proto, not from the encoder:
//
//	PerfDataPB.timestamp = 1 (LEN):  0A 02 | Timestamp.seconds = 1 (field 1, varint): 08 01
//	PerfDataPB.device_perf = 2 (LEN): 12 09 | DevicePerfPB:
//	  device_name = 1 "a":   0A 01 61
//	  vendor_os   = 2 (1):   10 01
//	  timestamp   = 3 (LEN): 1A 02 08 02
func TestEncodeMatchesTheWireFormatWorkedOutFromTheProto(t *testing.T) {
	p := Payload{Time: time.Unix(1, 0), Devices: []DevicePerf{{DeviceName: "a", VendorOS: 1, Time: time.Unix(2, 0)}}}
	want := []byte{0x0A, 0x02, 0x08, 0x01, 0x12, 0x09, 0x0A, 0x01, 'a', 0x10, 0x01, 0x1A, 0x02, 0x08, 0x02}
	if got := p.Encode(); !bytes.Equal(got, want) {
		t.Fatalf("got % X\nwant % X", got, want)
	}
}

// uptime_seconds = 5 (varint 300 = AC 02), cpu_usage = 6 (fixed32 0.5 = 00 00 00 3F), memory_usage = 7 (0.25 = 00 00 80 3E),
// and one map entry (field 4): key "e1" (field 1), value InterfaceCountersPB{in_octets=1:300, speed_mbps=7:1000}.
func TestEncodeScalarsFloatsAndMapEntries(t *testing.T) {
	d := DevicePerf{DeviceName: "x", UptimeSeconds: 300, CPU: 0.5, Memory: 0.25,
		Interfaces: map[string]Counters{"e1": {InOctets: 300, SpeedMbps: 1000}}}
	got := d.encode()
	// interface entry: key 0A 02 'e' '1'; value 12 06 | 08 AC 02 | 38 E8 07  (speed_mbps 1000 = E8 07)
	for name, frag := range map[string][]byte{
		"uptime": {0x28, 0xAC, 0x02},
		"cpu":    {0x35, 0x00, 0x00, 0x00, 0x3F},
		"memory": {0x3D, 0x00, 0x00, 0x80, 0x3E},
		"map":    {0x22, 0x0C, 0x0A, 0x02, 'e', '1', 0x12, 0x06, 0x08, 0xAC, 0x02, 0x38, 0xE8, 0x07},
	} {
		if !bytes.Contains(got, frag) {
			t.Errorf("%s: % X not found in % X", name, frag, got)
		}
	}
}

func TestZeroValuesAreOmittedLikeProto3(t *testing.T) {
	if got := (Payload{Time: time.Unix(1, 0)}).Encode(); !bytes.Equal(got, []byte{0x0A, 0x02, 0x08, 0x01}) {
		t.Fatalf("% X", got)
	}
}

func TestVendorOSTableCoversTheCommonPlatforms(t *testing.T) {
	for name, want := range map[string]int32{"ARISTA_EOS": 1, "CISCO_IOS": 2, "CISCO_NXOS": 3, "PAN_OS": 19, "CISCO_IOS_XE": 20} {
		if VendorOS[name] != want {
			t.Errorf("%s = %d, want %d", name, VendorOS[name], want)
		}
	}
}

// The independent check: decode our bytes with protoc against Forward's real PerfProtos.proto. A wrong field number,
// type or nesting fails here. Skipped when protoc or a Forward source checkout (FORWARD_SOURCE, default ~/src/fwd)
// is not available, so CI without them proves only the hand-derived bytes above.
func TestDecodesWithProtocAgainstForwardsRealProto(t *testing.T) {
	protoc, err := exec.LookPath("protoc")
	if err != nil {
		t.Skip("protoc not installed")
	}
	src := os.Getenv("FORWARD_SOURCE")
	if src == "" {
		home, _ := os.UserHomeDir()
		src = filepath.Join(home, "src", "fwd")
	}
	proto := filepath.Join(src, "client-structs/src/main/proto/com/forwardnetworks/client/web/PerfProtos.proto")
	if _, err := os.Stat(proto); err != nil {
		t.Skip("no Forward source checkout at " + src)
	}
	p := Payload{Time: time.Unix(1793046570, 500), Devices: []DevicePerf{
		{DeviceName: "core1", VendorOS: VendorOS["ARISTA_EOS"], Time: time.Unix(1793046569, 0), UptimeSeconds: 11621, CPU: 0.5, Memory: 0.25,
			Interfaces: map[string]Counters{"Ethernet1": {InOctets: 123456789, InDiscards: 1, InErrors: 2, OutOctets: 987654321, OutDiscards: 3, OutErrors: 4, SpeedMbps: 1000, InterfaceType: 6},
				"Ethernet2": {InOctets: 5}}},
	}}
	cmd := exec.Command(protoc, "--proto_path="+filepath.Join(src, "client-structs/src/main/proto"),
		"--proto_path="+filepath.Join(src, "base/src/main/proto"), "--decode=com.forwardnetworks.client.web.PerfDataPB", proto)
	cmd.Stdin = bytes.NewReader(p.Encode())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("protoc could not decode the payload: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{"seconds: 1793046570", `device_name: "core1"`, "vendor_os: VENDOR_OS_ARISTA_EOS",
		"uptime_seconds: 11621", "cpu_usage: 0.5", "memory_usage: 0.25", `key: "Ethernet1"`, "in_octets: 123456789",
		"out_octets: 987654321", "in_discards: 1", "out_errors: 4", "speed_mbps: 1000", "interface_type: 6", `key: "Ethernet2"`} {
		if !strings.Contains(text, want) {
			t.Errorf("decoded output lacks %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "error_details") {
		t.Error("an error_details was emitted")
	}
}

func TestLookupVendorOSAcceptsNQEsCiscoShortNames(t *testing.T) {
	for name, want := range map[string]int32{"ARISTA_EOS": 1, "IOS": 2, "IOS_XE": 20, "IOS_XR": 22, "NXOS": 3} {
		if got, ok := LookupVendorOS(name); !ok || got != want {
			t.Errorf("%s -> %d %v, want %d", name, got, ok, want)
		}
	}
	if _, ok := LookupVendorOS("NOT_A_REAL_OS"); ok {
		t.Error("an unknown OS must not map")
	}
}
