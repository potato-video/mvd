package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPairCodeVector(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if c := pairCode(key); c != "CC6WTAB6" {
		t.Errorf("code = %s", c)
	}
	for _, s := range []string{"CC6W-TAB6", "cc6wtab6", " cc6w-tab6 "} {
		if normCode(s) != "CC6WTAB6" {
			t.Errorf("%q: %s", s, normCode(s))
		}
	}
}

// device is the device's side of pairing, as the Apple apps do it.
type device struct {
	priv *ecdh.PrivateKey
	code string
}

func newDevice(t *testing.T) *device {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &device{priv: priv, code: pairCode(priv.PublicKey().Bytes())}
}

func (d *device) ask(ts *testServer, name string) (int, string) {
	b, _ := json.Marshal(map[string]string{"public_key": b64.EncodeToString(d.priv.PublicKey().Bytes()), "device": name})
	return ts.body("POST", "/api/pair", string(b), nil)
}

// open opens the box, with the code as additional data.
func (d *device) open(box pairBox, code string) (pairSecret, error) {
	var s pairSecret
	serverKey, _ := b64.DecodeString(box.ServerKey)
	nonce, _ := b64.DecodeString(box.Nonce)
	sealed, _ := b64.DecodeString(box.Sealed)
	pub, err := ecdh.X25519().NewPublicKey(serverKey)
	if err != nil {
		return s, err
	}
	shared, err := d.priv.ECDH(pub)
	if err != nil {
		return s, err
	}
	key, _ := hkdf.Key(sha256.New, shared, append(d.priv.PublicKey().Bytes(), serverKey...), "mvd pair v1", 32)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, sealed, []byte(code))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(plain, &s)
}

// The whole exchange: the device asks, waits, an admin approves it with
// kids' token, the device collects its key once and signs a request with it.
func TestPairing(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	d := newDevice(t)
	code, b := d.ask(ts, "Living Room\x07 TV")
	if code != 201 || !strings.Contains(b, `"code":"`+d.code+`"`) || !strings.Contains(b, `"expires_in":300`) {
		t.Fatalf("ask: %d %s", code, b)
	}
	if code, b := ts.body("GET", "/api/pair/"+d.code, "", nil); code != 202 || !strings.Contains(b, "waiting") {
		t.Errorf("waiting: %d %s", code, b)
	}

	// What's waiting, for the admin only.
	if code, _ := ts.body("GET", "/api/pair", "", bearer(denTok)); code != 403 {
		t.Errorf("a viewer listing: %d", code)
	}
	var list []pairView
	decode(t, ts.do("GET", "/api/pair", "", bearer(adminTok)), &list)
	if len(list) != 1 || list[0].Device != "Living Room TV" || list[0].Code != d.code {
		t.Errorf("list: %+v", list)
	}

	// Approving takes a token. A viewer gives its own and no other; the
	// admin names one there is.
	approve := "/api/pair/" + showCode(strings.ToLower(d.code)) + "/approve"
	for _, other := range []string{"kids", "admin"} {
		if code, _ := ts.body("POST", approve, `{"token": "`+other+`"}`, bearer(denTok)); code != 403 {
			t.Errorf("a viewer giving %s: %d", other, code)
		}
	}
	if code, _ := ts.body("POST", approve, `{"token": "kids"}`, nil); code != 401 {
		t.Errorf("no one approving: %d", code)
	}
	if code, b := ts.body("POST", approve, `{"token": "nobody"}`, bearer(adminTok)); code != 400 || !strings.Contains(b, `"kids"`) {
		t.Errorf("an unknown token: %d %s", code, b)
	}
	if code, b := ts.body("POST", approve, `{"token": "KIDS"}`, bearer(adminTok)); code != 200 || !strings.Contains(b, `"token":"kids"`) {
		t.Fatalf("approve: %d %s", code, b)
	}

	// Collected once, then it's gone.
	var box pairBox
	decode(t, ts.do("GET", "/api/pair/"+d.code, "", nil), &box)
	if code, _ := ts.body("GET", "/api/pair/"+d.code, "", nil); code != 404 {
		t.Errorf("a second collection: %d", code)
	}
	secret, err := d.open(box, d.code)
	if err != nil {
		t.Fatal(err)
	}
	kids := deriveKeys(kidsTok)
	if secret.K != kids.kid || secret.Name != "kids" || secret.Admin || secret.RequestKey != b64.EncodeToString(kids.request) {
		t.Errorf("secret: %+v", secret)
	}
	if strings.Contains(fmt.Sprint(box), kidsTok) {
		t.Error("the token is in the box")
	}

	// Tampered with, or opened with another code, it doesn't open.
	sealed, _ := b64.DecodeString(box.Sealed)
	sealed[0] ^= 1
	if _, err := d.open(pairBox{ServerKey: box.ServerKey, Nonce: box.Nonce, Sealed: b64.EncodeToString(sealed)}, d.code); err == nil {
		t.Error("a tampered box opened")
	}
	if _, err := d.open(box, "AAAAAAAA"); err == nil {
		t.Error("opened with the wrong code")
	}

	// The key works: a signed request as kids.
	rk, _ := b64.DecodeString(secret.RequestKey)
	now := fmt.Sprint(time.Now().Unix())
	auth := "MVD1 k=" + secret.K + ",t=" + now + ",sig=" + requestSig(rk, "GET", "/api/status", now, sha256.Sum256(nil))
	var st statusView
	decode(t, ts.do("GET", "/api/status", "", map[string]string{"Authorization": auth}), &st)
	if st.Access.Name != "kids" {
		t.Errorf("signed in as %+v", st.Access)
	}
}

// A viewer lets a device in as itself, saying nothing or its own name.
func TestPairingAsAViewer(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	for _, body := range []string{"", "{}", `{"token": "den"}`} {
		d := newDevice(t)
		d.ask(ts, "Bedroom")
		if code, b := ts.body("POST", "/api/pair/"+d.code+"/approve", body, bearer(denTok)); code != 200 || !strings.Contains(b, `"token":"Den"`) {
			t.Fatalf("approve with %q: %d %s", body, code, b)
		}
		var box pairBox
		decode(t, ts.do("GET", "/api/pair/"+d.code, "", nil), &box)
		den := deriveKeys(denTok)
		if s, err := d.open(box, d.code); err != nil || s.K != den.kid || s.Name != "Den" || s.Admin {
			t.Errorf("with %q: %+v %v", body, s, err)
		}
	}

	// The admin acting as a viewer gives that viewer's access.
	d := newDevice(t)
	d.ask(ts, "Bedroom")
	as := map[string]string{"Authorization": "Bearer " + adminTok, asHeader: "kids"}
	if code, b := ts.body("POST", "/api/pair/"+d.code+"/approve", "", as); code != 200 || !strings.Contains(b, `"token":"kids"`) {
		t.Errorf("as kids: %d %s", code, b)
	}

	// A viewer can refuse a device it has the code of.
	d = newDevice(t)
	d.ask(ts, "Bedroom")
	if code, _ := ts.body("DELETE", "/api/pair/"+d.code, "", bearer(kidsTok)); code != 204 {
		t.Errorf("a viewer refusing: %d", code)
	}
}

func TestPairRefuseAndExpire(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	now := time.Now()
	ts.sv.now = func() time.Time { return now }

	d := newDevice(t)
	d.ask(ts, "Den")
	if code, _ := ts.body("DELETE", "/api/pair/"+d.code, "", nil); code != 401 {
		t.Errorf("no one refusing: %d", code)
	}
	if code, _ := ts.body("DELETE", "/api/pair/"+d.code, "", bearer(adminTok)); code != 204 {
		t.Errorf("refuse: %d", code)
	}
	if code, _ := ts.body("GET", "/api/pair/"+d.code, "", nil); code != 404 {
		t.Errorf("refused, collecting: %d", code)
	}

	e := newDevice(t)
	e.ask(ts, "Den")
	now = now.Add(pairFor)
	if code, _ := ts.body("GET", "/api/pair/"+e.code, "", nil); code != 404 {
		t.Errorf("expired: %d", code)
	}
	if code, _ := ts.body("POST", "/api/pair/"+e.code+"/approve", `{"token": "admin"}`, bearer(adminTok)); code != 404 {
		t.Errorf("approving an expired one: %d", code)
	}
}

// At most 8 waiting, 2 from an address; nothing's pushed out to make room.
func TestPairLimits(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	from := func(addr string) map[string]string { return map[string]string{"X-Forwarded-For": addr} }
	ask := func(addr string) int {
		d := newDevice(t)
		c, _ := ts.body("POST", "/api/pair", fmt.Sprintf(`{"public_key": %q}`, b64.EncodeToString(d.priv.PublicKey().Bytes())), from(addr))
		return c
	}
	for i := range 2 {
		if c := ask("192.0.2.1"); c != 201 {
			t.Fatalf("%d from one address: %d", i+1, c)
		}
	}
	if c := ask("192.0.2.1"); c != 429 {
		t.Errorf("a third from one address: %d", c)
	}
	for i := 2; i < pairMax; i++ {
		if c := ask(fmt.Sprintf("192.0.2.%d", 10+i)); c != 201 {
			t.Fatalf("%d: %d", i+1, c)
		}
	}
	if c := ask("192.0.2.200"); c != 429 {
		t.Errorf("a ninth: %d", c)
	}

	// The same device asking again is the same request, not another.
	ts2 := newTokenServer(t)
	defer ts2.hs.Close()
	d := newDevice(t)
	for range 3 {
		if c, _ := d.ask(ts2, "TV"); c != 201 {
			t.Errorf("asking again: %d", c)
		}
	}

	// A bad key.
	if c, _ := ts2.body("POST", "/api/pair", `{"public_key": "AAAA"}`, nil); c != 400 {
		t.Errorf("a short key: %d", c)
	}
	// With no token, there's nothing to pair.
	open := newTestServer(t, "")
	defer open.hs.Close()
	if c, _ := newDevice(t).ask(open, "TV"); c != 409 {
		t.Errorf("no token: %d", c)
	}
}

// Guessing codes counts as guessing: five unknown ones and that address waits.
func TestPairGuesses(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	for i := range 5 {
		ts.body("GET", fmt.Sprintf("/api/pair/AAAAAAA%d", i), "", nil)
	}
	if c, _ := ts.body("GET", "/api/pair/AAAAAAAA", "", nil); c != http.StatusTooManyRequests {
		t.Errorf("after five unknown codes: %d", c)
	}
	if c, _ := newDevice(t).ask(ts, "TV"); c != http.StatusTooManyRequests {
		t.Errorf("asking, locked out: %d", c)
	}
}

// The reply says where the website's Pair page is, so a device can point
// people there, and says so when there isn't one (a build without the web
// UI): an empty page, never a missing one, which the apps take to be an
// older server.
func TestPairStartSaysWhereThePageIs(t *testing.T) {
	ts := newTokenServer(t)
	defer ts.hs.Close()
	c, body := newDevice(t).ask(ts, "Living Room")
	if c != 201 {
		t.Fatalf("ask: %d %s", c, body)
	}
	var got struct {
		Page *string `json:"page"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Page == nil {
		t.Fatalf("no page in %s", body)
	}
	if *got.Page == "" {
		// Signed in, so it's the missing page that answers, not the sign-in.
		admin := map[string]string{"Authorization": "Bearer " + adminTok}
		if c, _ := ts.body("GET", "/pair", "", admin); c != http.StatusNotFound {
			t.Errorf("an empty page in the reply, but GET /pair is %d", c)
		}
		return
	}
	if *got.Page != "/pair" {
		t.Errorf("page = %q", *got.Page)
	}
	if c, _ := ts.body("GET", *got.Page, "", nil); c == http.StatusNotFound {
		t.Errorf("the reply says %s, which is a 404", *got.Page)
	}
}
