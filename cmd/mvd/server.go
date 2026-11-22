package main

import (
	"flag"

	"mvd/internal/config"
	"mvd/internal/server"
)

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("mvd server", flag.ContinueOnError)
	address := fs.String("address", "", "address to listen on (overrides config)")
	port := fs.String("port", "", "port to listen on (overrides config)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	cfg := config.Load()
	if *address != "" {
		cfg.Address = *address
	}
	if *port != "" {
		cfg.Port = *port
	}
	server.Version = version
	return server.Run(cfg)
}
