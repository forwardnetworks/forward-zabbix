package fwdzbx

import (
	"errors"
	"testing"

	"github.com/forwardnetworks/forward-zabbix/internal/config"
)

func TestCollectorIdentityNeedsATokenAndNeverEchoesIt(t *testing.T) {
	if _, err := collectorIdentity(func(string) string { return "" }); !errors.Is(err, config.ErrConfig) {
		t.Fatalf("empty: %v", err)
	}
	id, err := collectorIdentity(func(string) string { return "collector-abc:s3cret" })
	if err != nil || id.Username != "collector-abc" || id.AuthorizationKey != "s3cret" {
		t.Fatalf("%+v %v", id, err)
	}
}
