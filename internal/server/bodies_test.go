package server

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"mvd/internal/config"
)

// countingBody is a request body of n bytes that counts how many were read.
type countingBody struct {
	prefix string
	n, off int64
	read   int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.off >= b.n {
		return 0, io.EOF
	}
	if int64(len(p)) > b.n-b.off {
		p = p[:b.n-b.off]
	}
	for i := range p {
		if j := b.off + int64(i); j < int64(len(b.prefix)) {
			p[i] = b.prefix[j]
		} else {
			p[i] = 'x'
		}
	}
	b.off += int64(len(p))
	b.read += int64(len(p))
	return len(p), nil
}

// Review H1: a form body of any size isn't read (and spilled to /tmp),
// signed in or not. Only adding a .torrent takes a multipart body.
func TestLargeFormBodiesAreRefused(t *testing.T) {
	for _, token := range []string{"", "secret"} {
		sv := &Server{cfg: config.Config{Token: token}}
		h := sv.routes()
		for _, route := range []string{"POST /login", "GET /api/import/search", "GET /ui/import/search",
			"POST /ui/import/retry", "POST /api/progress/m1"} {
			method, path, _ := strings.Cut(route, " ")
			for _, ct := range []string{"multipart/form-data; boundary=b", "application/x-www-form-urlencoded"} {
				body := &countingBody{prefix: "--b\r\nContent-Disposition: form-data; name=\"f\"; filename=\"f\"\r\n\r\n", n: 48 << 20}
				req := httptest.NewRequest(method, path, body)
				req.Header.Set("Content-Type", ct)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if body.read > 1<<20 {
					t.Errorf("token %q, %s %s: read %d bytes of the body", token, path, ct, body.read)
				}
				if strings.HasPrefix(ct, "multipart/") && rec.Code < 400 {
					t.Errorf("token %q, %s: a multipart body answered %d", token, path, rec.Code)
				}
			}
		}
	}
}
