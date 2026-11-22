package library

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"mvd/internal/tmdb"
)

// Questions: what an import or refresh can't settle by itself. The library
// never reads a terminal of its own accord: a run is given an AskFunc
// (AskTerminal, at a terminal) or none (-y, and the server), and with none
// it leaves the question in its results.

// Question asks which TMDB title something is (from Candidates), or, when
// Groups is set, which of a show's episode orders to use.
type Question struct {
	Label      string
	Kind       string // "movie" or "tv"
	Parsed     Parsed
	Candidates []Candidate

	Show   string // which order: the show's title and year
	Groups []tmdb.EpisodeGroupInfo

	search func(Parsed) ([]Candidate, error) // a new search, for [e]
	find   func(imdbID string) (int, error)  // an IMDb id looked up, for [i]
}

// Answer is a TMDB id (a match) or an episode group's id (an order). With
// neither, it's a skip.
type Answer struct {
	TMDB  int
	Group string
	Quit  bool
}

// AskFunc answers a question.
type AskFunc func(Question) (Answer, error)

// AskTerminal asks at a terminal, beets-style.
func AskTerminal(in io.Reader, out io.Writer) AskFunc {
	t := &terminal{in: bufio.NewReader(in), out: out}
	return func(q Question) (Answer, error) {
		if q.Groups != nil {
			return t.askGroup(q), nil
		}
		return t.askMatch(q)
	}
}

type terminal struct {
	in  *bufio.Reader
	out io.Writer
}

func (t *terminal) printf(format string, a ...any) { fmt.Fprintf(t.out, format, a...) }

func (t *terminal) readLine(prompt string) (string, error) {
	fmt.Fprint(t.out, prompt)
	line, err := t.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(t.out)
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// askMatch shows the candidates and lets the user pick.
func (t *terminal) askMatch(q Question) (Answer, error) {
	cs := q.Candidates
	for {
		t.printf("\n? %s\n", q.Label)
		if len(cs) == 0 {
			t.printf("  no matches on TMDB\n")
		}
		for i, c := range cs {
			if i >= 5 {
				break
			}
			t.printf("  %d) %s  %3.0f%%  tmdb:%d\n", i+1, TitleYear(c.Title, c.Year()), c.Score*100, c.ID)
			if c.OriginalTitle != "" && c.OriginalTitle != c.Title {
				t.printf("     (%s)\n", c.OriginalTitle)
			}
			if c.Overview != "" {
				t.printf("     %s\n", Clip(c.Overview, 90))
			}
		}
		prompt := "  [1-5] pick · [s]kip · [e]nter search · [i]d · [q]uit: leave this and the rest"
		if len(cs) > 0 {
			prompt += " (enter = 1)"
		}
		line, err := t.readLine(prompt + " > ")
		if err != nil {
			return Answer{}, nil // stdin closed: treat as skip
		}
		switch line = strings.ToLower(line); {
		case line == "" && len(cs) > 0:
			return Answer{TMDB: cs[0].ID}, nil
		case line == "s":
			return Answer{}, nil
		case line == "q":
			return Answer{Quit: true}, nil
		case line == "e":
			s, err := t.readLine("  search (Title or Title (Year)) > ")
			if err != nil || s == "" {
				continue
			}
			ncs, err := q.search(parseMovieName(s))
			if err != nil {
				return Answer{}, err
			}
			cs = ncs
		case line == "i":
			id, err := t.readLine("  TMDB id or IMDb tt-id > ")
			if err != nil || id == "" {
				continue
			}
			if strings.HasPrefix(id, "tt") {
				n, err := q.find(id)
				if err != nil {
					t.printf("  ! %v\n", err)
					continue
				}
				return Answer{TMDB: n}, nil
			}
			if n := Atoi(strings.TrimPrefix(id, "tmdb:")); n > 0 {
				return Answer{TMDB: n}, nil
			}
		default:
			if n := Atoi(line); n >= 1 && n <= min(len(cs), 5) {
				return Answer{TMDB: cs[n-1].ID}, nil
			}
		}
	}
}

// askGroup lists a show's episode orders and lets the user pick one.
func (t *terminal) askGroup(q Question) Answer {
	for {
		t.printf("\n? which order for %s\n", q.Show)
		for i, g := range q.Groups {
			t.printf("  %d) %s  (%s, %d seasons, %d episodes)\n", i+1, g.Name,
				groupTypeNames[g.Type], g.GroupCount, g.EpisodeCount)
		}
		line, err := t.readLine("  [number] pick · [s]kip (enter = 1) > ")
		if err != nil || strings.EqualFold(line, "s") {
			return Answer{}
		}
		if line == "" {
			return Answer{Group: q.Groups[0].ID}
		}
		if n := Atoi(line); n >= 1 && n <= len(q.Groups) {
			return Answer{Group: q.Groups[n-1].ID}
		}
	}
}
