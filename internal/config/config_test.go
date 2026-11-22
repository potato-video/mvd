package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mvd/internal/testutil"
)

func TestConfigValue(t *testing.T) {
	cases := map[string]string{
		" /mnt/Maxtor/.mvd-cache   # keep the cache off flash": "/mnt/Maxtor/.mvd-cache",
		" en-US":                               "en-US",
		` "abc # not a comment" # but this is`: "abc # not a comment",
		" 'quoted'":                            "quoted",
		" a#b":                                 "a#b", // no space before #: part of the value
		" # just a comment":                    "",
	}
	for in, want := range cases {
		if got := Value(in); got != want {
			t.Errorf("configValue(%q) = %q, want %q", in, got, want)
		}
	}
}

// load writes config.toml and reads it back.
func load(t *testing.T, toml string) Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("TMDB_API_KEY", "")
	if toml != "" {
		testutil.Write(t, filepath.Join(dir, "mvd/config.toml"), toml)
	}
	return Load()
}

func TestLoadConfigDirs(t *testing.T) {
	c := load(t, "[paths]\nvideo_dir = /mnt/Maxtor/video  # root\ntv_dir = /elsewhere/tv\n")
	if c.MoviesDir != "/mnt/Maxtor/video/movies" || c.TVDir != "/elsewhere/tv" ||
		c.ImportDir != "/mnt/Maxtor/video/import" || c.TrashDir != "/mnt/Maxtor/video/.trash" {
		t.Errorf("got %+v", c)
	}
}

func TestConfigDataDir(t *testing.T) {
	t.Setenv("HOME", "/home/warren")

	// Defaults: everything under ~/.config/mvd.
	c := load(t, "")
	conf := filepath.Dir(Path())
	if c.DataDir != conf || c.CacheDir != conf+"/cache" || c.StateFile != conf+"/state.json" || c.IndexFile != conf+"/index.json" {
		t.Errorf("defaults: %+v", c)
	}

	// data_dir moves the lot; ~ expands.
	c = load(t, `# mvd
[paths]
video_dir = "/mnt/Maxtor/video"
data_dir  = "/mnt/Maxtor/.mvd"   # on the drive, not flash
index_file = "~/idx.json"
`)
	if c.MoviesDir != "/mnt/Maxtor/video/movies" || c.CacheDir != "/mnt/Maxtor/.mvd/cache" ||
		c.StateFile != "/mnt/Maxtor/.mvd/state.json" || c.IndexFile != "/home/warren/idx.json" {
		t.Errorf("toml: %+v", c)
	}
}

func TestLibrarySettings(t *testing.T) {
	c := load(t, "")
	if !c.AutoImport || c.ResumeAfter != time.Minute || c.TrashMaxAge != 7*24*time.Hour || c.TrashMaxSize != 2<<30 {
		t.Errorf("defaults: %+v", c)
	}
	c = load(t, "[library]\nauto_import = false\nresume_after = \"90s\"\ntrash_max_age = \"36h\"\ntrash_max_size = \"500MB\"\n")
	if c.AutoImport || c.ResumeAfter != 90*time.Second || c.TrashMaxAge != 36*time.Hour || c.TrashMaxSize != 500<<20 {
		t.Errorf("set: %+v", c)
	}
	if c := load(t, "[library]\nresume_after = \"0\"\n"); c.ResumeAfter != 0 {
		t.Errorf("0 (off): %v", c.ResumeAfter)
	}
}

func TestServerSettings(t *testing.T) {
	c := load(t, "")
	if c.Listen() != ":6837" || !c.Uploads || c.UploadPreallocate || c.UploadBuffer != 256<<10 ||
		!c.MDNS || c.MDNSName != "" || c.MDNSInterfaces != "" {
		t.Errorf("defaults: %+v", c)
	}
	c = load(t, `[server]
address = "192.168.8.1"
port = 6838
uploads = false
upload_preallocate = true
upload_buffer = "1MB"
mdns = false
mdns_name = "Living room"
mdns_interfaces = "br-lan"
`)
	if c.Listen() != "192.168.8.1:6838" || c.Uploads || !c.UploadPreallocate || c.UploadBuffer != 1<<20 ||
		c.MDNS || c.MDNSName != "Living room" || c.MDNSInterfaces != "br-lan" {
		t.Errorf("set: %+v", c)
	}
	if c := load(t, "[server]\naddress = \"::1\"\nport = 99999\n"); c.Listen() != "[::1]:6837" {
		t.Errorf("IPv6, bad port: %s", c.Listen())
	}
}

func TestDownloadsAndClientSettings(t *testing.T) {
	if c := load(t, ""); c.SeedRatio != 1 || c.MaxDownloads != 2 {
		t.Errorf("defaults: %+v", c)
	}
	c := load(t, `[tmdb]
api_key = "k"
[downloads]
transmission = "http://127.0.0.1:9091/transmission/rpc"
seed_ratio = 0
max_downloads = 3
[client]
server = "http://router:6837/"
`)
	if c.APIKey != "k" || c.Transmission == "" || c.SeedRatio != 0 || c.MaxDownloads != 3 || c.Server != "http://router:6837" {
		t.Errorf("%+v", c)
	}
}

// [torrent] was [downloads]' name: a config that still says it works.
func TestTorrentSectionStillRead(t *testing.T) {
	c := load(t, "[torrent]\ntransmission = \"http://127.0.0.1:9091/transmission/rpc\"\nseed_ratio = 0\n")
	if c.Transmission == "" || c.SeedRatio != 0 {
		t.Errorf("%+v", c)
	}
}

// Settings outside their section are ignored, with a hint.
func TestConfigHint(t *testing.T) {
	if c := load(t, "video_dir = \"/v\"\n[downloads]\nport = 1\n"); c.VideoDir != "" || c.Port != "6837" {
		t.Errorf("misplaced settings used: %+v", c)
	}
	for k, want := range map[string]string{
		".transmission":    " (it belongs under [downloads])",
		"server.video_dir": " (it belongs under [paths])",
		"paths.vidoe_dir":  "",
	} {
		if got := hint(k); got != want {
			t.Errorf("hint(%q) = %q, want %q", k, got, want)
		}
	}
	if got := name(".port") + ", " + name("server.port"); got != "port, [server] port" {
		t.Errorf("names: %s", got)
	}
}

func TestSettingsListed(t *testing.T) {
	s := Settings()
	if len(s) != len(settings)+len(tokenSettings) || !strings.HasPrefix(s[0], "client.") || !slices.Contains(s, "server.tokens.ratings") {
		t.Errorf("%v", s)
	}
}

// Review L2: paths are cleaned, so code that cuts a root off a path
// (scanImport, say) can rely on its form.
func TestConfigPathsCleaned(t *testing.T) {
	c := load(t, "[paths]\nimport_dir = /mnt/drive/video/import/\nmovies_dir = /mnt/drive//video/movies/\n")
	if c.ImportDir != "/mnt/drive/video/import" || c.MoviesDir != "/mnt/drive/video/movies" {
		t.Errorf("got %q, %q", c.ImportDir, c.MoviesDir)
	}
}

// Review L14: config.toml holds the token, the TMDB key and Transmission's
// password, so one other users can read is worth a warning.
func TestSecretsExposed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := Path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("[tmdb]\napi_key = k\n"), 0o644)
	os.Chmod(p, 0o644)
	if !SecretsExposed(Load()) {
		t.Error("a key in a world-readable config.toml")
	}
	os.Chmod(p, 0o600)
	if SecretsExposed(Load()) {
		t.Error("a private config.toml")
	}
	os.WriteFile(p, []byte("[paths]\nvideo_dir = /v\n"), 0o644)
	os.Chmod(p, 0o644)
	if SecretsExposed(Load()) {
		t.Error("no secrets in it")
	}
}

func TestTokens(t *testing.T) {
	c := load(t, `[server]
token = "long-random-admin-token"

[[server.tokens]]
name = "kids"
token = "another-long-random-token"
ratings = ["U", 'PG', "FSK 12",]   # only these
unrated = true

[[server.tokens]]
name = "living room"
token = "a-third-long-random-token"
colour = "blue"

[[server.tokens]]
name = "nothing"
token = "a-fourth-long-random-token"
ratings = []

[tmdb]
region = "US"
`)
	want := []Token{
		{Name: "kids", Token: "another-long-random-token", Ratings: []string{"U", "PG", "FSK 12"}, Limited: true, Unrated: true},
		{Name: "living room", Token: "a-third-long-random-token"},
		{Name: "nothing", Token: "a-fourth-long-random-token", Ratings: []string{}, Limited: true},
	}
	if c.Token != "long-random-admin-token" || c.Region != "US" || len(c.Tokens) != len(want) {
		t.Fatalf("%+v", c)
	}
	for i, w := range want {
		g := c.Tokens[i]
		if g.Name != w.Name || g.Token != w.Token || g.Limited != w.Limited || g.Unrated != w.Unrated ||
			strings.Join(g.Ratings, "|") != strings.Join(w.Ratings, "|") || (g.Ratings == nil) != (w.Ratings == nil) {
			t.Errorf("token %d: %+v, want %+v", i, g, w)
		}
	}
	if w, err := c.CheckTokens(); err != nil || len(w) != 0 {
		t.Errorf("check: %v %v", w, err)
	}
	if c := load(t, "[[server.tokens]]\nratings = \"U\"\n"); len(c.Tokens) != 1 || c.Tokens[0].Limited {
		t.Errorf("not a list: %+v", c.Tokens)
	}
}

func TestParseList(t *testing.T) {
	for in, want := range map[string]string{
		`[]`: "", `["U"]`: "U", `[ "U" , "PG" ]`: "U|PG", `['12A', "FSK 12"]`: "12A|FSK 12", `["U",]`: "U",
	} {
		if got, err := parseList(in); err != nil || strings.Join(got, "|") != want {
			t.Errorf("parseList(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{`U`, `["U"`, `[U]`, `["U" "PG"]`, `["U`, ``} {
		if got, err := parseList(in); err == nil {
			t.Errorf("parseList(%s) = %q, want an error", in, got)
		}
	}
}

func TestCheckTokens(t *testing.T) {
	long := "0123456789abcdef"
	viewer := func(name, tok string) Token { return Token{Name: name, Token: tok} }
	for _, c := range []struct {
		cfg  Config
		want string // in the error; "" for none
		warn int
	}{
		{Config{}, "", 0},
		{Config{Token: "short"}, "", 1},
		{Config{Token: long, Tokens: []Token{viewer("kids", long+"1")}}, "", 0},
		{Config{Token: long, Tokens: []Token{viewer("kids", "short")}}, "", 1},
		{Config{Tokens: []Token{viewer("kids", long)}}, "needs a [server] token", 0},
		{Config{Token: long, Tokens: []Token{viewer("", long+"1")}}, "has no name", 0},
		{Config{Token: long, Tokens: []Token{viewer("kids", "")}}, "has no token", 0},
		{Config{Token: long, Tokens: []Token{viewer("kids!", long+"1")}}, "letters, digits", 0},
		{Config{Token: long, Tokens: []Token{viewer("0123456789012345678901234567890123", long+"1")}}, "1 to 32", 0},
		{Config{Token: long, Tokens: []Token{viewer("Admin", long+"1")}}, "the [server] token's", 0},
		{Config{Token: long, Tokens: []Token{viewer("Living Room", long+"1"), viewer("living-room", long+"2")}}, "share a progress file", 0},
		{Config{Token: long, Tokens: []Token{viewer("kids", long)}}, "same token as \"admin\"", 0},
		{Config{Token: long, Tokens: []Token{viewer("kids", long+"1"), viewer("den", long+"1")}}, "same token as \"kids\"", 0},
	} {
		w, err := c.cfg.CheckTokens()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%+v: %v", c.cfg, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%+v: %v, want %q", c.cfg, err, c.want)
		case err == nil && len(w) != c.warn:
			t.Errorf("%+v: warnings %q, want %d", c.cfg, w, c.warn)
		}
	}
	if got := TokenSlug("Living Room"); got != "living-room" {
		t.Errorf("slug: %s", got)
	}
}
