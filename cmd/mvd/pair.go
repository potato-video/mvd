package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"mvd/internal/config"
)

const pairUsage = `usage: mvd pair [-token NAME] [CODE]

  mvd pair                       the devices waiting to be let in
  mvd pair CC6W-TAB6             let that one in, with the admin's token
  mvd pair -token kids CC6W-TAB6 let it in with the [[server.tokens]] entry named kids

An Apple TV, iPhone or iPad setting up shows a code. This gives that device
a token without it being typed there: check the code against the device's
screen first. Codes last five minutes. The website's Pair page does the same.

The server does this, so mvd server needs to be running.
`

func cmdPair(args []string) int {
	fs := newFlagSet("mvd pair")
	token := fs.String("token", config.AdminName, "the token to give the device, by `NAME`")
	fs.Usage = func() { fmt.Fprint(os.Stderr, pairUsage) }
	pos, code := parseFlags(fs, args)
	if code >= 0 {
		return code
	}
	if len(pos) > 1 {
		fmt.Fprint(os.Stderr, pairUsage)
		return 2
	}
	cfg := config.Load()
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "mvd: the server has no token, so there's nothing to pair: devices connect without one")
		return 1
	}
	api := localAPI(cfg)
	api.hint = ""
	if len(pos) == 0 {
		return pairList(api, os.Stdout)
	}
	if err := pairApprove(api, os.Stdout, pos[0], *token); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	return 0
}

func pairList(api *apiClient, out io.Writer) int {
	var waiting []struct {
		Code    string `json:"code"`
		Device  string `json:"device"`
		Address string `json:"address"`
		Age     int    `json:"age"`
	}
	if err := api.get("/api/pair", &waiting); err != nil {
		fmt.Fprintln(os.Stderr, "mvd:", err)
		return 1
	}
	if len(waiting) == 0 {
		fmt.Fprintln(out, "No devices are waiting to be let in.")
		return 0
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tDEVICE\tADDRESS\tASKED")
	for _, p := range waiting {
		name := p.Device
		if name == "" {
			name = "(no name)"
		}
		c := p.Code
		if len(c) == 8 {
			c = c[:4] + "-" + c[4:]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s ago\n", c, name, p.Address, (time.Duration(p.Age) * time.Second).String())
	}
	w.Flush()
	fmt.Fprintln(out, "\nmvd pair CODE lets one in; check the code against its screen first.")
	return 0
}

func pairApprove(api *apiClient, out io.Writer, code, token string) error {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	var got struct {
		Device string `json:"device"`
		Token  string `json:"token"`
	}
	err := api.do("POST", "/api/pair/"+code+"/approve", map[string]string{"token": token}, &got)
	if err != nil && strings.HasPrefix(err.Error(), "not found:") {
		return fmt.Errorf("no device is waiting with the code %s: codes last five minutes, so check the device's screen", code)
	}
	if err != nil {
		return err
	}
	name := got.Device
	if name == "" {
		name = "The device"
	}
	fmt.Fprintf(out, "%s is let in with the %s token. It connects within a few seconds.\n", name, got.Token)
	return nil
}
