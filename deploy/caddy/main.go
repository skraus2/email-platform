// Command caddy is the stock Caddy server with its standard modules, built
// here so the binary carries the patched Go toolchain and golang.org/x/net
// pinned in go.mod instead of whatever the upstream release was built with.
package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"

	_ "github.com/caddyserver/caddy/v2/modules/standard"
)

func main() {
	caddycmd.Main()
}
