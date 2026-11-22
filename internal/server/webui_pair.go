//go:build webui

package server

import "net/http"

// The Pair page: someone signed in lets a device in. With ?code= (the
// link in the device's QR code), that device: its name and address, the
// code in large type to check against its screen, and one button, which
// gives it this browser's access. The admin, with viewers' tokens to
// give, chooses which, none chosen to start with. Without a code, a field
// to type one into, and for the admin what's waiting.

type pairPage struct {
	Code    string // as shown, "CC6W-TAB6"
	Raw     string // as sent, "CC6WTAB6"
	Device  string
	Address string
	Asked   string   // "2 minutes ago"
	As      string   // whose access the button gives: this browser's
	Admin   bool     // which is everything
	Tokens  []string // to choose from instead: the admin's page, with viewers
	Waiting []pairView
	Missing bool   // a code was asked for that isn't waiting
	Done    string // what was done, after approving or refusing
	Err     string
}

// pairPagePath is where the Pair page is (as registered below), for POST
// /api/pair to tell devices.
const pairPagePath = "/pair"

func (sv *Server) uiPairRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /pair", sv.uiPair)
	mux.HandleFunc("POST /ui/pair", sv.uiPairPost)
}

func (sv *Server) uiPair(w http.ResponseWriter, r *http.Request) {
	sv.renderPair(w, r, pairPage{}, r.URL.Query().Get("code"))
}

// renderPair is the page for code ("" for none), with d's message.
func (sv *Server) renderPair(w http.ResponseWriter, r *http.Request, d pairPage, code string) {
	if code != "" {
		if req, ok := sv.pairWaiting(code); ok {
			d.Code, d.Raw, d.Device, d.Address = showCode(req.code), req.code, req.device, req.addr.String()
			d.Asked = agoAt(req.at, sv.clock())
			if d.Device == "" {
				d.Device = "A device"
			}
			for _, a := range sv.pairChoices(sv.who(r)) {
				d.Tokens = append(d.Tokens, a.name)
			}
		} else if d.Done == "" {
			d.Missing = true
		}
	}
	acc := sv.who(r)
	d.As, d.Admin = acc.name, acc.admin
	if d.Code == "" && acc.admin {
		d.Waiting = sv.pairViews()
	}
	sv.render(w, r, "pair", page{Title: "Pair a device", Data: d})
}

// pairChoices is the tokens by chooses between when letting a device in:
// none unless it's the admin and there are viewers.
func (sv *Server) pairChoices(by *access) []*access {
	if !by.admin || len(sv.accesses) < 2 {
		return nil
	}
	return sv.accesses
}

func (sv *Server) uiPairPost(w http.ResponseWriter, r *http.Request) {
	code, token := r.FormValue("code"), r.FormValue("token")
	var d pairPage
	switch r.FormValue("action") {
	case "refuse":
		if sv.refusePair(code) {
			d.Done = "Refused. That device wasn't let in."
		}
	default:
		by := sv.who(r)
		if token == "" && sv.pairChoices(by) != nil {
			d.Err = "Choose who the device should be let in as."
			break
		}
		req, acc, err := sv.approvePair(by, code, token)
		if err != nil {
			d.Err = err.Error()
			break
		}
		name := req.device
		if name == "" {
			name = "The device"
		}
		d.Done = name + " is let in as " + acc.name + ". It connects within a few seconds."
	}
	if d.Done != "" {
		code = "" // back to what's waiting
	}
	sv.renderPair(w, r, d, code)
}
