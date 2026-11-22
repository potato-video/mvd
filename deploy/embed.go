// Package deploy holds what it takes to set mvd up on a machine: a commented
// config.toml and the service files. They're built into the binary, so
// mvd setup can print them where there's only the binary (the router).
package deploy

import _ "embed"

//go:embed config.example.toml
var ConfigExample string

//go:embed openwrt/mvd.init
var OpenWRT string

//go:embed openrc/mvd.init
var OpenRC string

//go:embed systemd/mvd.service
var Systemd string

//go:embed launchd/local.mvd.server.plist
var Launchd string
