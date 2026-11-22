package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPair(t *testing.T) {
	var approved []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/pair", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"code":"CC6WTAB6","device":"Living Room","address":"192.0.2.7","age":65}]`)
	})
	mux.HandleFunc("POST /api/pair/{code}/approve", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-tok" {
			http.Error(w, "token needed", 401)
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if r.PathValue("code") != "CC6WTAB6" {
			http.Error(w, "no device is waiting", 404)
			return
		}
		approved = append(approved, r.PathValue("code")+"="+b["token"])
		io.WriteString(w, `{"code":"CC6WTAB6","device":"Living Room","token":"kids"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api := &apiClient{base: srv.URL, token: "admin-tok", http: srv.Client()}

	var out bytes.Buffer
	if pairList(api, &out) != 0 || !strings.Contains(out.String(), "CC6W-TAB6  Living Room  192.0.2.7  1m5s ago") {
		t.Errorf("list:\n%s", out.String())
	}
	out.Reset()
	if err := pairApprove(api, &out, "cc6w-tab6", "kids"); err != nil || out.String() != "Living Room is let in with the kids token. It connects within a few seconds.\n" {
		t.Errorf("approve: %v %q", err, out.String())
	}
	if len(approved) != 1 || approved[0] != "CC6WTAB6=kids" {
		t.Errorf("approved %v", approved)
	}
	if err := pairApprove(api, &out, "AAAA-AAAA", "admin"); err == nil || !strings.Contains(err.Error(), "no device is waiting with the code AAAAAAAA") {
		t.Errorf("unknown: %v", err)
	}
}
