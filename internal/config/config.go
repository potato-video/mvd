// Package config reads mvd's config.toml.
package config

import (
	"bufio"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// [paths]
	VideoDir  string
	MoviesDir string
	TVDir     string
	ImportDir string
	TrashDir  string
	DataDir   string
	CacheDir  string
	StateFile string
	IndexFile string

	// [library]
	AutoImport   bool          // the server imports by itself: finished downloads and uploads, and anything copied in
	ResumeAfter  time.Duration // how far into something before it's "in progress"
	TrashMaxAge  time.Duration
	TrashMaxSize int64

	// [server]
	Address           string // where mvd server listens; every interface if empty
	Port              string
	Token             string  // the admin's
	Tokens            []Token // [[server.tokens]]: viewers, who can only watch
	HostNames         string  // extra names the server answers changes on (DNS rebinding guard)
	ClientIPHeader    string  // the header a reverse proxy puts the client's address in, for limiting guesses
	Uploads           bool    // uploads from the Incoming page and mvd upload: /api/uploads
	UploadPreallocate bool    // set an upload's full size on the drive before its first piece
	UploadBuffer      int64   // memory each piece is written through
	MDNS              bool    // advertise the server on the local network (_mvd._tcp), so apps find it
	MDNSName          string  // what apps show; the machine's name if empty
	MDNSInterfaces    string  // only on these interfaces ("br-lan"); every private network if empty

	// [tmdb]
	APIKey   string
	Language string
	Region   string

	// [downloads]
	Transmission string
	TorrentUser  string
	TorrentPass  string
	SeedRatio    float64
	MaxDownloads int // Transmission's download queue (mvd setup transmission); 0 leaves its own

	// [client]
	Server string // where mvd's own client commands (mvd upload) find the server: "http://router:6837"
}

// Token is a [[server.tokens]] entry: a viewer's token, which can browse,
// play and keep its own progress, and nothing more.
type Token struct {
	Name    string
	Token   string
	Ratings []string // the certifications it may see, if Limited
	Limited bool     // ratings was given: an empty list isn't "all"
	Unrated bool     // it sees things with no certification (only matters if Limited)
}

// Listen is the server's address and port, for net.Listen.
func (c Config) Listen() string { return net.JoinHostPort(c.Address, c.Port) }

// Path is where the config is read from: ~/.config/mvd/config.toml, or the
// same under $XDG_CONFIG_HOME.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "mvd", "config.toml")
}

// SecretsExposed says whether config.toml holds a secret (the token, the
// TMDB key, Transmission's password) and other users can read it.
func SecretsExposed(c Config) bool {
	if c.Token == "" && len(c.Tokens) == 0 && c.APIKey == "" && c.TorrentPass == "" {
		return false
	}
	st, err := os.Stat(Path())
	return err == nil && st.Mode().Perm()&0o044 != 0
}

func Load() Config {
	c := Config{Language: "en-US", Region: "GB", Port: "6837", ResumeAfter: time.Minute,
		TrashMaxAge: 7 * 24 * time.Hour, TrashMaxSize: 2 << 30, SeedRatio: 1, MaxDownloads: 2,
		Uploads: true, UploadBuffer: 256 << 10, MDNS: true, AutoImport: true}

	if f, err := os.Open(Path()); err == nil {
		sc := bufio.NewScanner(f)
		section := ""
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] == '#' {
				continue
			}
			// [[server.tokens]]: each starts another entry, and the keys
			// after it are that entry's.
			if strings.HasPrefix(line, "[[") {
				if end := strings.Index(line, "]]"); end > 0 {
					section = strings.TrimSpace(line[2:end])
				}
				if section == "server.tokens" {
					c.Tokens = append(c.Tokens, Token{})
				} else {
					fmt.Fprintf(os.Stderr, "mvd: config: unknown table [[%s]]\n", section)
				}
				continue
			}
			// [section]: as in TOML, the keys after it belong to it.
			if line[0] == '[' {
				if end := strings.IndexByte(line, ']'); end > 0 {
					section = strings.TrimSpace(line[1:end])
				}
				if section == "torrent" { // [downloads]' old name
					section = "downloads"
				}
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = section + "." + strings.TrimSpace(k)
			if section == "server.tokens" && len(c.Tokens) > 0 {
				set, ok := tokenSettings[strings.TrimSpace(k[len(section)+1:])]
				if !ok {
					fmt.Fprintf(os.Stderr, "mvd: config: unknown setting %s in [[server.tokens]]\n", k[len(section)+1:])
				} else if err := set(&c.Tokens[len(c.Tokens)-1], v); err != nil {
					fmt.Fprintf(os.Stderr, "mvd: config: [[server.tokens]] %s: %v\n", k[len(section)+1:], err)
				}
				continue
			}
			set, ok := settings[k]
			if !ok {
				fmt.Fprintf(os.Stderr, "mvd: config: unknown setting %s%s\n", name(k), hint(k))
				continue
			}
			if err := set(&c, Value(v)); err != nil {
				fmt.Fprintf(os.Stderr, "mvd: config: %s: %v\n", name(k), err)
			}
		}
		f.Close()
	}

	if c.VideoDir != "" {
		if c.MoviesDir == "" {
			c.MoviesDir = filepath.Join(c.VideoDir, "movies")
		}
		if c.TVDir == "" {
			c.TVDir = filepath.Join(c.VideoDir, "tv")
		}
		if c.ImportDir == "" {
			c.ImportDir = filepath.Join(c.VideoDir, "import")
		}
		if c.TrashDir == "" {
			c.TrashDir = filepath.Join(c.VideoDir, ".trash")
		}
	}
	// Everything mvd keeps for itself lives under data_dir unless told
	// otherwise: one key moves the lot (onto the USB drive, say).
	if c.DataDir == "" {
		c.DataDir = filepath.Dir(Path())
	}
	if c.CacheDir == "" {
		c.CacheDir = filepath.Join(c.DataDir, "cache")
	}
	if c.StateFile == "" {
		c.StateFile = filepath.Join(c.DataDir, "state.json")
	}
	if c.IndexFile == "" {
		c.IndexFile = filepath.Join(c.DataDir, "index.json")
	}
	if v := strings.TrimSpace(os.Getenv("TMDB_API_KEY")); v != "" {
		c.APIKey = v
	}
	return c
}

// settings are every setting there is, as "section.key", each setting its
// part of the config from the value.
var settings = map[string]func(c *Config, v string) error{
	"paths.video_dir":  path(func(c *Config) *string { return &c.VideoDir }),
	"paths.movies_dir": path(func(c *Config) *string { return &c.MoviesDir }),
	"paths.tv_dir":     path(func(c *Config) *string { return &c.TVDir }),
	"paths.import_dir": path(func(c *Config) *string { return &c.ImportDir }),
	"paths.trash_dir":  path(func(c *Config) *string { return &c.TrashDir }),
	"paths.data_dir":   path(func(c *Config) *string { return &c.DataDir }),
	"paths.cache_dir":  path(func(c *Config) *string { return &c.CacheDir }),
	"paths.state_file": path(func(c *Config) *string { return &c.StateFile }),
	"paths.index_file": path(func(c *Config) *string { return &c.IndexFile }),

	"library.auto_import":    boolean(func(c *Config) *bool { return &c.AutoImport }),
	"library.resume_after":   age(func(c *Config) *time.Duration { return &c.ResumeAfter }),
	"library.trash_max_age":  age(func(c *Config) *time.Duration { return &c.TrashMaxAge }),
	"library.trash_max_size": func(c *Config, v string) (err error) { c.TrashMaxSize, err = parseSize(v); return },

	"server.address": text(func(c *Config) *string { return &c.Address }),
	"server.port": func(c *Config, v string) error {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%q isn't a port from 1 to 65535", v)
		}
		c.Port = v
		return nil
	},
	"server.token":              text(func(c *Config) *string { return &c.Token }),
	"server.host_names":         text(func(c *Config) *string { return &c.HostNames }),
	"server.client_ip_header":   text(func(c *Config) *string { return &c.ClientIPHeader }),
	"server.uploads":            boolean(func(c *Config) *bool { return &c.Uploads }),
	"server.upload_preallocate": boolean(func(c *Config) *bool { return &c.UploadPreallocate }),
	"server.upload_buffer": func(c *Config, v string) error {
		n, err := parseSize(v)
		if err != nil || n < 4<<10 || n > 64<<20 {
			return fmt.Errorf("%q isn't a size from 4KB to 64MB", v)
		}
		c.UploadBuffer = n
		return nil
	},
	"server.mdns":            boolean(func(c *Config) *bool { return &c.MDNS }),
	"server.mdns_name":       text(func(c *Config) *string { return &c.MDNSName }),
	"server.mdns_interfaces": text(func(c *Config) *string { return &c.MDNSInterfaces }),

	"tmdb.api_key":  text(func(c *Config) *string { return &c.APIKey }),
	"tmdb.language": text(func(c *Config) *string { return &c.Language }),
	"tmdb.region":   text(func(c *Config) *string { return &c.Region }),

	"downloads.transmission": text(func(c *Config) *string { return &c.Transmission }),
	"downloads.username":     text(func(c *Config) *string { return &c.TorrentUser }),
	"downloads.password":     text(func(c *Config) *string { return &c.TorrentPass }),
	"downloads.seed_ratio": func(c *Config, v string) error {
		r, err := strconv.ParseFloat(v, 64)
		if err != nil || r < 0 {
			return fmt.Errorf("%q isn't a number", v)
		}
		c.SeedRatio = r
		return nil
	},
	"downloads.max_downloads": func(c *Config, v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("%q isn't a whole number", v)
		}
		c.MaxDownloads = n
		return nil
	},

	"client.server": func(c *Config, v string) error { c.Server = strings.TrimSuffix(v, "/"); return nil },
}

// tokenSettings are the keys of a [[server.tokens]] entry, each taking
// the raw right-hand side of its line.
var tokenSettings = map[string]func(t *Token, v string) error{
	"name":  func(t *Token, v string) error { t.Name = Value(v); return nil },
	"token": func(t *Token, v string) error { t.Token = Value(v); return nil },
	"ratings": func(t *Token, v string) (err error) {
		t.Ratings, err = parseList(Value(v))
		t.Limited = err == nil
		return err
	},
	"unrated": func(t *Token, v string) error {
		return boolean(func(*Config) *bool { return &t.Unrated })(nil, Value(v))
	},
}

// Settings lists every setting, as "section.key", sorted. A
// [[server.tokens]] entry's keys are "server.tokens.key".
func Settings() []string {
	all := slices.Collect(maps.Keys(settings))
	for k := range tokenSettings {
		all = append(all, "server.tokens."+k)
	}
	slices.Sort(all)
	return all
}

// parseList reads a one-line array of quoted strings: ["U", "PG", '12A'].
// Only that: this isn't a TOML parser.
func parseList(v string) ([]string, error) {
	bad := fmt.Errorf("%q isn't a list like [\"U\", \"PG\"]", v)
	if len(v) < 2 || v[0] != '[' || v[len(v)-1] != ']' {
		return nil, bad
	}
	out := []string{}
	rest := strings.TrimSpace(v[1 : len(v)-1])
	for rest != "" {
		q := rest[0]
		if q != '"' && q != '\'' {
			return nil, bad
		}
		end := strings.IndexByte(rest[1:], q)
		if end < 0 {
			return nil, bad
		}
		out = append(out, rest[1:1+end])
		rest = strings.TrimSpace(rest[2+end:])
		if rest == "" {
			break
		}
		if rest[0] != ',' {
			return nil, bad
		}
		rest = strings.TrimSpace(rest[1:]) // a trailing comma is fine
	}
	return out, nil
}

// AdminName is the [server] token's name, which no other token may have.
const AdminName = "admin"

// ShortToken is the length under which a token is warned about.
const ShortToken = 16

// TokenSlug is how a token's name appears in its progress file's name:
// lower-cased, with spaces as "-" ("living room": state-living-room.json).
func TokenSlug(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), " ", "-")
}

// CheckTokens is what must be true of the tokens before the server starts:
// each an error saying what's wrong. (Not in Load, so the other commands
// still run.) warnings are tokens too short to be safe, which only warn.
func (c Config) CheckTokens() (warnings []string, err error) {
	if len(c.Tokens) > 0 && c.Token == "" {
		return nil, fmt.Errorf("[[server.tokens]] needs a [server] token, the admin's, as well")
	}
	if c.Token != "" && len(c.Token) < ShortToken {
		warnings = append(warnings, fmt.Sprintf("[server] token is under %d characters: anyone who can reach the server could guess it", ShortToken))
	}
	slugs := map[string]string{}
	tokens := map[string]string{c.Token: AdminName}
	for i, t := range c.Tokens {
		switch {
		case t.Name == "":
			return nil, fmt.Errorf("[[server.tokens]] entry %d has no name", i+1)
		case t.Token == "":
			return nil, fmt.Errorf("[[server.tokens]] %q has no token", t.Name)
		case !validTokenName(t.Name):
			return nil, fmt.Errorf("[[server.tokens]] name %q: use 1 to 32 letters, digits, spaces, - and _", t.Name)
		case strings.EqualFold(t.Name, AdminName):
			return nil, fmt.Errorf("[[server.tokens]] name %q is the [server] token's", t.Name)
		}
		slug := TokenSlug(t.Name)
		if other, ok := slugs[slug]; ok {
			return nil, fmt.Errorf("[[server.tokens]] %q and %q would share a progress file (state-%s.json): rename one", other, t.Name, slug)
		}
		slugs[slug] = t.Name
		if other, ok := tokens[t.Token]; ok {
			return nil, fmt.Errorf("[[server.tokens]] %q has the same token as %q", t.Name, other)
		}
		tokens[t.Token] = t.Name
		if len(t.Token) < ShortToken {
			warnings = append(warnings, fmt.Sprintf("[[server.tokens]] %q: the token is under %d characters: anyone who can reach the server could guess it", t.Name, ShortToken))
		}
	}
	return warnings, nil
}

func validTokenName(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func text(f func(*Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error { *f(c) = v; return nil }
}

func path(f func(*Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error {
		if v = expandHome(v); v != "" {
			v = filepath.Clean(v)
		}
		*f(c) = v
		return nil
	}
}

func boolean(f func(*Config) *bool) func(*Config, string) error {
	return func(c *Config, v string) error {
		switch v {
		case "true":
			*f(c) = true
		case "false":
			*f(c) = false
		default:
			return fmt.Errorf("%q isn't true or false", v)
		}
		return nil
	}
}

func age(f func(*Config) *time.Duration) func(*Config, string) error {
	return func(c *Config, v string) (err error) { *f(c), err = ParseAge(v); return }
}

// name is a setting as people write it: "[server] port", or "port" for one
// above every section.
func name(k string) string {
	sec, key, _ := strings.Cut(k, ".")
	if sec == "" {
		return key
	}
	return "[" + sec + "] " + key
}

// hint says where a misplaced setting belongs, if it's one mvd has.
func hint(k string) string {
	_, key, _ := strings.Cut(k, ".")
	var in []string
	for s := range settings {
		if sec, kk, _ := strings.Cut(s, "."); kk == key {
			in = append(in, "["+sec+"]")
		}
	}
	if len(in) == 0 {
		return ""
	}
	slices.Sort(in)
	return " (it belongs under " + strings.Join(in, " or ") + ")"
}

// ParseAge reads an age or duration: "7d", "36h", "90m", "45s" or "0".
func ParseAge(v string) (time.Duration, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "0" {
		return 0, nil
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(v, "d"), 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q isn't an age like \"7d\" or \"36h\"", v)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q isn't an age like \"7d\" or \"36h\"", v)
	}
	return d, nil
}

// parseSize reads "2GB", "500MB", "1.5G", "800k" or "0" (binary units).
func parseSize(v string) (int64, error) {
	v = strings.TrimSpace(strings.ToUpper(v))
	v = strings.TrimSuffix(strings.TrimSuffix(v, "B"), "I")
	mult := float64(1)
	if v != "" {
		switch v[len(v)-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult > 1 {
			v = v[:len(v)-1]
		}
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q isn't a size like \"2GB\" or \"500MB\"", v)
	}
	return int64(n * mult), nil
}

// expandHome turns a leading ~/ into the home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Value cleans up the right-hand side of a "key = value" line: a
// quoted value runs to its closing quote ("a # b" keeps the #); otherwise a
// # with whitespace (or nothing) before it starts a trailing comment.
func Value(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
		return v[1:]
	}
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v)
}
