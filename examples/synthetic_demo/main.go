// Command synthetic_demo fills a Zabbix with a made-up Forward inventory, using this project's own code. No Forward
// is needed.
//
//	ZABBIX_URL=http://127.0.0.1:8082 ZABBIX_USER=Admin ZABBIX_PASSWORD=zabbix ZABBIX_SERVER=127.0.0.1 ZABBIX_TRAPPER_PORT=10052 \
//	  go run ./examples/synthetic_demo
//
// It imports the templates, syncs 12 synthetic devices, pushes a collection result for each (one failed), adds a
// hand-made host Forward does not know, then adds the Forward note to the resulting problem. Use a throwaway
// Zabbix: it creates hosts and problems. The screenshots in docs/ were taken this way.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
	"github.com/forwardnetworks/forward-zabbix/internal/plan"
	"github.com/forwardnetworks/forward-zabbix/internal/zabbix"
)

const network = "1001"

var snapshot = struct {
	ID    string
	Epoch int64
}{"48213", 1793046570}

func dev(name, site, vendor, model, os, ip, collected string) plan.Device {
	return plan.Device{Name: name, Site: site, Vendor: vendor, Model: model, OS: os, OSVersion: "1.0", DeviceType: "SWITCH",
		IPs: []string{ip}, Collected: collected}
}

func devices() []plan.Device {
	d := []plan.Device{
		dev("dc1-core-01", "dc1", "ARISTA", "7280R3", "ARISTA_EOS", "10.10.0.1", "completed"),
		dev("dc1-core-02", "dc1", "ARISTA", "7280R3", "ARISTA_EOS", "10.10.0.2", "completed"),
		dev("dc1-fw-01", "dc1", "PALO_ALTO_NETWORKS", "PA-5450", "PAN_OS", "10.10.0.31", "collectionFailed:AUTHENTICATION_FAILED"),
		dev("wan-core-01", "wan", "ARISTA", "7280R3", "ARISTA_EOS", "10.20.0.1", "completed"),
		dev("wan-core-02", "wan", "CISCO", "ASR1001-X", "CISCO_IOS_XE", "10.20.0.2", "completed"),
	}
	for i, c := range []string{"nyc", "bos", "chi", "dal", "sfo", "sea", "den"} {
		d = append(d, dev(fmt.Sprintf("br-%s-rtr-01", c), "br-"+c, "CISCO", "ISR4331", "CISCO_IOS_XE", fmt.Sprintf("10.30.%d.1", i+1), "completed"))
	}
	return d
}

var links = map[string][]plan.Link{
	"dc1-fw-01": {{Iface: "ethernet1/1", Peer: "dc1-core-01", PeerIface: "Ethernet3"}, {Iface: "ethernet1/2", Peer: "dc1-core-02", PeerIface: "Ethernet3"}},
}

func main() {
	ctx := context.Background()
	cfg, err := config.ZabbixFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	z, err := zabbix.Connect(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer z.Close(ctx)

	tpl, err := os.ReadFile("zabbix/forward-templates.yaml")
	if err != nil {
		log.Fatal("run from the repository root: ", err)
	}
	rule := map[string]bool{"createMissing": true, "updateExisting": true}
	rules := map[string]any{"template_groups": rule, "templates": rule, "items": rule, "triggers": rule}
	if err := z.Call(ctx, "configuration.import", map[string]any{"format": "yaml", "source": string(tpl), "rules": rules}, nil); err != nil {
		log.Fatal(err)
	}
	// two hand-made hosts so the coverage gap has something to find
	var g []struct {
		GroupID string `json:"groupid"`
	}
	_ = z.Call(ctx, "hostgroup.get", map[string]any{"output": []string{"groupid"}, "filter": map[string]any{"name": []string{"Linux servers"}}}, &g)
	have, _ := z.ListHosts(ctx)
	exists := map[string]bool{}
	for _, h := range have {
		exists[h.Host] = true
	}
	for name, ip := range map[string]string{"legacy-switch-01": "10.99.0.5", "dc1-core-02-old": "10.10.0.2"} {
		if !exists[name] {
			err := z.Call(ctx, "host.create", map[string]any{"host": name, "groups": []map[string]string{{"groupid": g[0].GroupID}},
				"interfaces": []map[string]any{{"type": 1, "main": 1, "useip": 1, "ip": ip, "dns": "", "port": "10050"}}}, nil)
			if err != nil {
				log.Fatal(err)
			}
		}
	}
	if _, err := z.EnsureNetworkHost(ctx, network); err != nil { // create it now so the server has loaded it before the first push
		log.Fatal(err)
	}
	hosts, _ := z.ListHosts(ctx)
	p := plan.PlanSync(devices(), hosts, network)
	applied, err := z.ApplyPlan(ctx, p)
	if err != nil {
		log.Fatal(err)
	}
	gap := plan.CoverageGap(devices(), func() []plan.ZbxHost { h, _ := z.ListHosts(ctx); return h }())
	fmt.Println("sync:", applied, "| collisions:", p.Collisions)
	fmt.Println("gap: unmonitored", gap.Unmonitored, "| unmodeled", gap.Unmodeled, "| drift", gap.Drift)

	time.Sleep(65 * time.Second) // let the server load the new hosts (config cache)
	var vals []zabbix.Value
	for _, v := range plan.StatusValues(devices(), network, snapshot.ID, snapshot.Epoch) {
		vals = append(vals, zabbix.Value{Host: v.Host, Key: v.Key, Value: v.Value})
	}
	processed, failed, err := z.PushValues(ctx, vals)
	fmt.Println("push:", processed, failed, err)
	time.Sleep(20 * time.Second) // let the trigger fire

	byHost := map[string]plan.Device{}
	for _, d := range devices() {
		byHost[plan.TechnicalName(d.Name)] = d
	}
	problems, err := z.OpenProblems(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, pr := range problems {
		if d, ok := byHost[pr.Host]; ok && !pr.Noted {
			if err := z.AddNote(ctx, pr.EventID, plan.EnrichmentNote(d, links[d.Name], snapshot.ID)); err != nil {
				log.Fatal(err)
			}
			fmt.Println("note added to:", pr.Name)
		}
	}
}
