// Package config reads settings from the environment only; nothing is read from or written to disk.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
)

// ErrConfig marks a missing or invalid setting; the CLI exits 64 for it.
var ErrConfig = errors.New("configuration error")

func errf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrConfig, fmt.Sprintf(format, a...))
}

// Zabbix is how to reach a Zabbix server: the API through its frontend and the trapper port on the server.
type Zabbix struct {
	URL         string
	Token       string
	User        string
	Password    string
	Server      string // trapper host; defaults to the host of URL
	TrapperPort int
}

func ZabbixFromEnv() (Zabbix, error) {
	z := Zabbix{URL: os.Getenv("ZABBIX_URL"), Token: os.Getenv("ZABBIX_TOKEN"),
		User: os.Getenv("ZABBIX_USER"), Password: os.Getenv("ZABBIX_PASSWORD"),
		Server: os.Getenv("ZABBIX_SERVER"), TrapperPort: 10051}
	if z.URL == "" {
		return z, errf("ZABBIX_URL is required (e.g. http://127.0.0.1:8081)")
	}
	if z.Token == "" && (z.User == "" || z.Password == "") {
		return z, errf("set ZABBIX_TOKEN, or ZABBIX_USER and ZABBIX_PASSWORD")
	}
	if p := os.Getenv("ZABBIX_TRAPPER_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return z, errf("ZABBIX_TRAPPER_PORT must be a port number, got %q", p)
		}
		z.TrapperPort = n
	}
	if z.Server == "" {
		u, err := url.Parse(z.URL)
		if err != nil || u.Hostname() == "" {
			z.Server = "127.0.0.1"
		} else {
			z.Server = u.Hostname()
		}
	}
	return z, nil
}

// Forward is how to reach Forward. The same variables the Forward Python SDK used.
type Forward struct {
	URL, Username, Password string
	InsecureSkipVerify      bool
	CAFile                  string
}

func ForwardFromEnv() (Forward, error) {
	f := Forward{URL: os.Getenv("FORWARD_URL"), Username: os.Getenv("FORWARD_USERNAME"), Password: os.Getenv("FORWARD_PASSWORD")}
	if f.URL == "" || f.Username == "" || f.Password == "" {
		return f, errf("set FORWARD_URL, FORWARD_USERNAME and FORWARD_PASSWORD")
	}
	switch v := os.Getenv("FORWARD_VERIFY_TLS"); v {
	case "", "true", "1":
	case "false", "0":
		f.InsecureSkipVerify = true
	default:
		f.CAFile = v // a path to a CA bundle
	}
	return f, nil
}
