// Command fwdzbx is the Forward <-> Zabbix integration. See the README.
package main

import (
	"context"
	"os"

	"github.com/forwardnetworks/forward-zabbix/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Live{}, os.Getenv))
}
