package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The credentials besides the token itself, as docs/SERVER_DEVELOPMENT.md
// ("The formats") has them: signed links for players, signed requests for
// the Apple apps, and the website's cookie. Each is HMAC-SHA256 with a key
// derived from a token, one key per use, none of them the token.

// keys are what's derived from a token, once, at startup.
type keys struct {
	kid     string // 16 hex characters: which token, without naming it
	link    []byte // signs links
	request []byte // signs requests
	cookie  string // the mvd_session cookie's whole value
}

func hmacSum(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

var b64 = base64.RawURLEncoding

func deriveKeys(token string) keys {
	t := []byte(token)
	kid := hex.EncodeToString(hmacSum(t, "mvd key id v1")[:8])
	return keys{
		kid:     kid,
		link:    hmacSum(t, "mvd link v1"),
		request: hmacSum(t, "mvd request v1"),
		cookie:  kid + "." + b64.EncodeToString(hmacSum(hmacSum(t, "mvd cookie v1"), "mvd session v1")),
	}
}

// sessionCookie signs a browser in to the website: its value is the
// token's kid and a MAC (keys.cookie). tokenCookie is the one before it,
// which held the token itself: no longer read, and cleared on signing in.
const (
	sessionCookie = "mvd_session"
	tokenCookie   = "mvd_token"
)

// --- signed links ---

// linkRoutes are the routes a signed link works on: the files players
// fetch. No other route takes one.
var linkRoutes = map[string]bool{
	"GET /stream/{id}":     true,
	"GET /subs/{id}/{n}":   true,
	"GET /art/{id}/{kind}": true,
	"GET /play/{file}":     true,
}

// linkSig is a link's signature: the first 16 bytes of the HMAC of
// "GET\n" + target, which is the escaped path and the query without k and
// sig. HEAD is signed as GET.
func linkSig(key []byte, target string) string {
	var s linkSigner
	s.key = key
	return string(s.sig(target))
}

// linkTarget is what a link's signature covers, and its k and sig (both
// "" when it has neither).
func linkTarget(r *http.Request) (target, kid, sig string) {
	raw := r.URL.RawQuery
	if raw == "" {
		return r.URL.EscapedPath(), "", ""
	}
	var keep []string
	for part := range strings.SplitSeq(raw, "&") {
		switch {
		case strings.HasPrefix(part, "k="):
			kid = part[2:]
		case strings.HasPrefix(part, "sig="):
			sig = part[4:]
		default:
			keep = append(keep, part)
		}
	}
	target = r.URL.EscapedPath()
	if len(keep) > 0 {
		target += "?" + strings.Join(keep, "&")
	}
	return target, kid, sig
}

// linkSigner signs the media paths one request answers with, using its
// token's link key. One per request, so its MAC and buffers needn't be
// shared, and a list of a few hundred movies signs without garbage beyond
// the strings it hands out.
type linkSigner struct {
	kid     string
	key     []byte
	mac     hash.Hash
	msg     []byte
	sum     [sha256.Size]byte
	encoded [22]byte // 16 bytes, base64url
}

// sig is target's signature, in s.encoded.
func (s *linkSigner) sig(target string) []byte {
	if s.mac == nil {
		s.mac = hmac.New(sha256.New, s.key)
	}
	s.msg = append(append(s.msg[:0], "GET\n"...), target...)
	s.mac.Reset()
	s.mac.Write(s.msg)
	b64.Encode(s.encoded[:], s.mac.Sum(s.sum[:0])[:16])
	return s.encoded[:]
}

// sign is path with k and sig added.
func (s *linkSigner) sign(path string) string {
	if path == "" {
		return ""
	}
	sig := s.sig(path)
	var b strings.Builder
	b.Grow(len(path) + len("&k=&sig=") + len(s.kid) + len(sig))
	b.WriteString(path)
	if strings.IndexByte(path, '?') >= 0 {
		b.WriteString("&k=")
	} else {
		b.WriteString("?k=")
	}
	b.WriteString(s.kid)
	b.WriteString("&sig=")
	b.Write(sig)
	return b.String()
}

// --- signed requests ---

// requestScheme is the Authorization scheme of a signed request, and what
// every 401 says in WWW-Authenticate, so a client can tell a server that
// knows it from an older one.
const requestScheme = "MVD1"

// requestWindow is how far a signed request's time may be from the
// server's clock.
const requestWindow = 300

func requestSig(key []byte, method, target, t string, bodySum [sha256.Size]byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(requestScheme + "\n" + method + "\n" + target + "\n" + t + "\n"))
	var h [2 * sha256.Size]byte
	hex.Encode(h[:], bodySum[:])
	m.Write(h[:])
	return b64.EncodeToString(m.Sum(nil))
}

// parseMVD1 reads "k=…,t=…,sig=…" (after "MVD1 ").
func parseMVD1(v string) (kid, t, sig string, ok bool) {
	for part := range strings.SplitSeq(v, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "k":
			kid = val
		case "t":
			t = val
		case "sig":
			sig = val
		}
	}
	return kid, t, sig, kid != "" && t != "" && sig != ""
}

// fresh says whether a signed request's time is within the window.
func fresh(t string, now time.Time) bool {
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return false
	}
	d := now.Unix() - n
	return d >= -requestWindow && d <= requestWindow
}

// --- limiting guesses ---

// guesses counts wrong credentials by address, in a fixed table: a flood
// of addresses can't grow it. After guessesFree wrong in a row, an address
// is locked out for a second, doubling with each further one up to
// guessesMaxWait. No goroutine, no allocation.
type guesses struct {
	mu sync.Mutex
	t  [64]guessEntry
}

type guessEntry struct {
	addr  netip.Addr
	fails int
	last  time.Time // the latest failure; zero for a free slot
	until time.Time // locked out until then
}

const (
	guessesFree    = 5
	guessesMaxWait = time.Minute
)

// find is addr's entry, or nil.
func (g *guesses) find(addr netip.Addr) *guessEntry {
	for i := range g.t {
		if e := &g.t[i]; !e.last.IsZero() && e.addr == addr {
			return e
		}
	}
	return nil
}

// wait is how long addr must wait before presenting a credential: 0 for
// not at all.
func (g *guesses) wait(addr netip.Addr, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.find(addr); e != nil && now.Before(e.until) {
		return e.until.Sub(now)
	}
	return 0
}

// fail counts a wrong credential from addr. When the table's full, the
// entry whose last failure is oldest makes room.
func (g *guesses) fail(addr netip.Addr, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.find(addr)
	if e == nil {
		e = &g.t[0]
		for i := range g.t {
			if g.t[i].last.Before(e.last) {
				e = &g.t[i]
			}
		}
		*e = guessEntry{addr: addr}
	}
	e.fails++
	e.last = now
	if e.fails >= guessesFree {
		wait := guessesMaxWait
		if n := e.fails - guessesFree; n < 6 {
			wait = min(time.Second<<n, guessesMaxWait)
		}
		e.until = now.Add(wait)
	}
}

// ok clears addr, after a credential that was right.
func (g *guesses) ok(addr netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.find(addr); e != nil {
		*e = guessEntry{}
	}
}

// addrOf is the address a request comes from, for counting guesses: the
// connection's, but behind a reverse proxy on loopback or a private
// network, the client's address in the header client_ip_header names
// (client, "" when it isn't set or the request has none), and behind one
// on the same machine (loopback, with X-Forwarded-For), the last address
// the proxy added.
func addrOf(remote, forwarded, client string) netip.Addr {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return netip.Addr{}
	}
	a := ap.Addr().Unmap()
	if client != "" && (a.IsLoopback() || a.IsPrivate()) {
		if f, err := netip.ParseAddr(strings.TrimSpace(client)); err == nil {
			return f.Unmap()
		}
	}
	if a.IsLoopback() && forwarded != "" {
		last := forwarded
		if i := strings.LastIndexByte(forwarded, ','); i >= 0 {
			last = forwarded[i+1:]
		}
		if f, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
			return f.Unmap()
		}
	}
	return a
}

func (sv *Server) clientAddr(r *http.Request) netip.Addr {
	var client string
	if h := sv.cfg.ClientIPHeader; h != "" {
		client = r.Header.Get(h)
	}
	return addrOf(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), client)
}

// tooMany answers a locked-out address.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
	http.Error(w, "too many tries: wait a moment, then try again with the right token", http.StatusTooManyRequests)
}
