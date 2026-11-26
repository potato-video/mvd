package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Pairing lets a device in without typing a token on it: the device sends
// a public key and shows a code made from it; an admin approves the code
// (on the website's Pair page, or with mvd pair) and chooses a token; the
// device collects that token's key id and request key, sealed so only it
// can open them. The device never has the token.
//
//	POST /api/pair             {public_key, device}  → 201 {code, expires_in}
//	GET  /api/pair/{code}      → 202 waiting, or once, 200 {server_key, nonce, sealed}
//	GET  /api/pair             what's waiting (admin)
//	POST /api/pair/{code}/approve {token}            (admin)
//	DELETE /api/pair/{code}    refuse it              (admin)
//
// The code is the first 5 bytes of SHA-256 of the device's key in
// Crockford base32, so both sides work it out and an admin checks the
// screen against the page. Approving makes a new X25519 key pair; the
// box is AES-256-GCM with a key from HKDF-SHA256 over the shared secret,
// the code as additional data.

const (
	pairFor     = 5 * time.Minute // how long a request waits
	pairMax     = 8               // waiting at once
	pairPerAddr = 2               // from one address
	deviceMax   = 64              // characters of a device's name
)

type pairing struct {
	mu sync.Mutex
	m  map[string]*pairRequest // by code
}

type pairRequest struct {
	code   string
	device string
	addr   netip.Addr
	key    []byte // the device's X25519 public key
	at     time.Time
	box    *pairBox // once approved
}

// pairBox is what an approved device collects.
type pairBox struct {
	Status    string `json:"status"` // "approved"
	ServerKey string `json:"server_key"`
	Nonce     string `json:"nonce"`
	Sealed    string `json:"sealed"`
}

// pairSecret is what's sealed in the box: all a device needs to sign
// requests as that token.
type pairSecret struct {
	K          string `json:"k"`
	Name       string `json:"name"`
	Admin      bool   `json:"admin"`
	RequestKey string `json:"request_key"`
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// pairCode is the code for a device's public key: "CC6WTAB6".
func pairCode(pub []byte) string {
	sum := sha256.Sum256(pub)
	n := uint64(sum[0])<<32 | uint64(binary.BigEndian.Uint32(sum[1:5]))
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = crockford[n&31]
		n >>= 5
	}
	return string(b[:])
}

// normCode is a code as typed, with or without the hyphen, in either case.
func normCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// showCode is a code as people read it: "CC6W-TAB6".
func showCode(c string) string {
	if len(c) != 8 {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// cleanDevice is a device's name, fit to show: no control characters, at
// most deviceMax characters.
func cleanDevice(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
	if r := []rune(strings.TrimSpace(s)); len(r) > deviceMax {
		s = string(r[:deviceMax])
	}
	return strings.TrimSpace(s)
}

// prune forgets what's expired. With p.mu held.
func (p *pairing) prune(now time.Time) {
	for c, req := range p.m {
		if now.Sub(req.at) >= pairFor {
			delete(p.m, c)
		}
	}
}

// waiting is what's waiting for approval, oldest first.
func (sv *Server) waiting() []pairRequest {
	p := &sv.pairs
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(sv.clock())
	var out []pairRequest
	for _, req := range p.m {
		if req.box == nil {
			out = append(out, *req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// pairWaiting is the request waiting with that code, if there is one.
func (sv *Server) pairWaiting(code string) (pairRequest, bool) {
	p := &sv.pairs
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(sv.clock())
	if req := p.m[normCode(code)]; req != nil && req.box == nil {
		return *req, true
	}
	return pairRequest{}, false
}

// approvePair lets the device waiting with code in, on by's say: its box
// is sealed, for it to collect. It gets by's own access, or, when by is
// the admin, the named token's ("admin" for the [server] token). Whoever
// knows a token could type it on the device, so passing it on gives
// nothing new; giving another's takes the admin.
func (sv *Server) approvePair(by *access, code, token string) (pairRequest, *access, error) {
	acc := by
	switch {
	case token == "" || strings.EqualFold(token, by.name):
	case !by.admin:
		return pairRequest{}, nil, errNotYours
	case strings.EqualFold(token, sv.accesses[0].name):
		acc = sv.accesses[0]
	default:
		if acc = sv.accessNamed(token); acc == nil {
			return pairRequest{}, nil, fmt.Errorf("there's no token named %q; there's \"admin\", %s", token, sv.tokenNames())
		}
	}
	p := &sv.pairs
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(sv.clock())
	req := p.m[normCode(code)]
	if req == nil || req.box != nil {
		return pairRequest{}, nil, errNoPair
	}
	box, err := sealPair(req.key, req.code, pairSecret{K: acc.kid, Name: acc.name, Admin: acc.admin,
		RequestKey: b64.EncodeToString(acc.request)})
	if err != nil {
		delete(p.m, req.code)
		return pairRequest{}, nil, fmt.Errorf("the device's key won't do: %v", err)
	}
	req.box = box
	return *req, acc, nil
}

var (
	errNoPair   = fmt.Errorf("no device is waiting with that code: it may have expired, so check the device's screen")
	errNotYours = fmt.Errorf("refused: this token lets a device in as itself; only the admin can give it another")
)

// refusePair forgets the device waiting with code; false if there's none.
func (sv *Server) refusePair(code string) bool {
	p := &sv.pairs
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(sv.clock())
	if req := p.m[normCode(code)]; req != nil && req.box == nil {
		delete(p.m, req.code)
		return true
	}
	return false
}

// sealPair seals secret for the device with that public key.
func sealPair(devKey []byte, code string, secret pairSecret) (*pairBox, error) {
	x := ecdh.X25519()
	dev, err := x.NewPublicKey(devKey)
	if err != nil {
		return nil, err
	}
	priv, err := x.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(dev)
	if err != nil {
		return nil, err
	}
	serverKey := priv.PublicKey().Bytes()
	key, err := hkdf.Key(sha256.New, shared, append(append([]byte{}, devKey...), serverKey...), "mvd pair v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	plain, _ := json.Marshal(secret)
	return &pairBox{Status: "approved", ServerKey: b64.EncodeToString(serverKey), Nonce: b64.EncodeToString(nonce),
		Sealed: b64.EncodeToString(gcm.Seal(nil, nonce, plain, []byte(code)))}, nil
}

// --- the API ---

func (sv *Server) pairRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/pair", sv.apiPairStart)
	mux.HandleFunc("GET /api/pair/{code}", sv.apiPairCollect)
	mux.HandleFunc("GET /api/pair", sv.apiPairList)
	mux.HandleFunc("POST /api/pair/{code}/approve", sv.apiPairApprove)
	mux.HandleFunc("DELETE /api/pair/{code}", sv.apiPairRefuse)
}

// pairLocked answers a device's request from an address that's made too
// many wrong guesses, as for any credential.
func (sv *Server) pairLocked(w http.ResponseWriter, r *http.Request) bool {
	if wait := sv.guesses.wait(sv.clientAddr(r), sv.clock()); wait > 0 {
		tooMany(w, wait)
		return true
	}
	return false
}

func (sv *Server) apiPairStart(w http.ResponseWriter, r *http.Request) {
	if sv.pairLocked(w, r) {
		return
	}
	if sv.cfg.Token == "" {
		http.Error(w, "this server has no token: there's nothing to pair, so connect without one", http.StatusConflict)
		return
	}
	var body struct {
		PublicKey string `json:"public_key"`
		Device    string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	key, err := b64.DecodeString(strings.TrimRight(body.PublicKey, "="))
	if err != nil || len(key) != 32 {
		http.Error(w, "public_key must be 32 bytes (X25519), base64url", http.StatusBadRequest)
		return
	}
	code, addr, now := pairCode(key), sv.clientAddr(r), sv.clock()
	p := &sv.pairs
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(now)
	if req := p.m[code]; req != nil {
		if string(req.key) != string(key) {
			http.Error(w, "that code is taken: make a new key and try again", http.StatusConflict)
			return
		}
		// The same device asking again: the same request.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, pairStarted(code, pairFor-now.Sub(req.at)))
		return
	}
	fromAddr := 0
	for _, req := range p.m {
		if req.addr == addr {
			fromAddr++
		}
	}
	if len(p.m) >= pairMax || fromAddr >= pairPerAddr {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many devices are waiting to be paired: try again in a few minutes", http.StatusTooManyRequests)
		return
	}
	if p.m == nil {
		p.m = map[string]*pairRequest{}
	}
	p.m[code] = &pairRequest{code: code, device: cleanDevice(body.Device), addr: addr, key: key, at: now}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, pairStarted(code, pairFor))
}

// pairStarted is the reply to a device asking: its code, how long it has,
// and the website's Pair page for the device to send people to, empty
// without one. Always there, empty or not: the apps take a reply without
// it to be from a server older than this, which had /pair.
func pairStarted(code string, left time.Duration) map[string]any {
	return map[string]any{"code": code, "expires_in": int(left / time.Second), "page": pairPagePath}
}

// apiPairCollect is a device asking whether it's been let in. Unknown,
// expired, refused and already-collected codes are all a 404, counted as
// a wrong guess.
func (sv *Server) apiPairCollect(w http.ResponseWriter, r *http.Request) {
	if sv.pairLocked(w, r) {
		return
	}
	code := normCode(r.PathValue("code"))
	p := &sv.pairs
	p.mu.Lock()
	p.prune(sv.clock())
	req := p.m[code]
	var box *pairBox
	if req != nil && req.box != nil {
		box = req.box
		delete(p.m, code) // once only
	}
	p.mu.Unlock()
	switch {
	case req == nil:
		sv.guesses.fail(sv.clientAddr(r), sv.clock())
		http.Error(w, "no such code: it may have expired, been refused, or been collected", http.StatusNotFound)
	case box == nil:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]string{"status": "waiting"})
	default:
		writeJSON(w, box)
	}
}

type pairView struct {
	Code    string `json:"code"`
	Device  string `json:"device"`
	Address string `json:"address"`
	Age     int    `json:"age"` // seconds
}

func (sv *Server) pairViews() []pairView {
	now := sv.clock()
	out := []pairView{}
	for _, req := range sv.waiting() {
		out = append(out, pairView{Code: req.code, Device: req.device, Address: req.addr.String(),
			Age: int(now.Sub(req.at) / time.Second)})
	}
	return out
}

func (sv *Server) apiPairList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sv.pairViews())
}

func (sv *Server) apiPairApprove(w http.ResponseWriter, r *http.Request) {
	// No body, or no token in it: the asker's own.
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, `bad JSON: {"token": NAME} gives another token (admin only), nothing gives your own`, http.StatusBadRequest)
		return
	}
	req, acc, err := sv.approvePair(sv.who(r), r.PathValue("code"), body.Token)
	switch {
	case err == errNoPair:
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err == errNotYours:
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"code": req.code, "device": req.device, "token": acc.name})
}

func (sv *Server) apiPairRefuse(w http.ResponseWriter, r *http.Request) {
	if !sv.refusePair(r.PathValue("code")) {
		http.Error(w, errNoPair.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
