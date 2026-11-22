//go:build files

package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Folder uploads. A folder (a season and its subtitles, say) is sent as a
// group of uploads sharing a group id: 16 hex digits the client chooses,
// the same every time it sends that folder, so sending it again after an
// interruption finds what's already in. Each file's name is its path in
// the folder, starting with the folder's own name. A finished file is put
// by in .uploads/<group>.d, at its path; when all group_files are in, the
// folder moves into the import folder in one go, so an import never takes
// half a season, or a movie without its subtitles. .uploads/<group>.group
// records the group (how many files, what kind).

type uploadGroup struct {
	Group      string    `json:"group"`
	Kind       string    `json:"kind,omitempty"`
	GroupFiles int       `json:"group_files"`
	Started    time.Time `json:"started"`
}

func (sv *Server) groupDir(g string) string  { return filepath.Join(sv.uploadDir(), g+".d") }
func (sv *Server) groupFile(g string) string { return filepath.Join(sv.uploadDir(), g+".group") }

// cleanFolderPath checks a path in a folder part by part: no climbing out,
// nothing hidden, at least a folder and a file, and the file one that can
// be uploaded.
func cleanFolderPath(p string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	var out []string
	for i, part := range parts {
		part = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, part))
		switch {
		case part == "" || part == ".":
			continue
		case part == "..":
			return "", errors.New(p + ": no .. in a path")
		case strings.HasPrefix(part, "."):
			return "", errors.New(p + ": a hidden file or folder")
		case len(part) > maxNameBytes:
			return "", errors.New(part + ": the name is too long")
		}
		if i == len(parts)-1 {
			name, err := cleanUploadName(part)
			if err != nil {
				return "", err
			}
			part = name
		}
		out = append(out, part)
	}
	if len(out) < 2 {
		return "", errors.New(p + ": in a folder upload, a path starts with the folder's name")
	}
	return strings.Join(out, "/"), nil
}

func (sv *Server) readGroup(g string) (uploadGroup, bool) {
	var ug uploadGroup
	b, err := os.ReadFile(sv.groupFile(g))
	return ug, err == nil && json.Unmarshal(b, &ug) == nil
}

// groupHas says whether a file of a group is in already (finished before,
// say, the tab closed), and if not, records the group, as this upload
// starts. (Called with the start lock held.)
func (sv *Server) groupHas(m uploadMeta) (uploadView, bool) {
	p := filepath.Join(sv.groupDir(m.Group), filepath.FromSlash(m.Name))
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() == m.Size {
		v := uploadView{uploadMeta: m, Offset: m.Size, Updated: st.ModTime(), Done: true}
		v.GroupDone = sv.groupCount(m.Group)
		return v, true
	}
	ug, ok := sv.readGroup(m.Group)
	if !ok {
		ug = uploadGroup{Group: m.Group, Kind: m.Kind, Started: time.Now()}
	}
	ug.GroupFiles = max(ug.GroupFiles, m.GroupFiles)
	b, _ := json.Marshal(ug)
	os.MkdirAll(sv.uploadDir(), 0o777)
	os.WriteFile(sv.groupFile(m.Group), b, 0o666)
	return uploadView{}, false
}

// groupCount is how many of a group's files are in.
func (sv *Server) groupCount(g string) int {
	n := 0
	filepath.WalkDir(sv.groupDir(g), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// finishInGroup puts a finished file by, at its path in the group's
// folder, and once the group is all in, moves the folder into the import
// folder. The path is the folder's in the import folder, once it's there.
func (sv *Server) finishInGroup(v uploadView) (string, error) {
	defer sv.ups.lock("group " + v.Group)()
	dst := filepath.Join(sv.groupDir(v.Group), filepath.FromSlash(v.Name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(sv.uploadDir(), v.ID+".part"), dst); err != nil {
		return "", err
	}
	os.Remove(filepath.Join(sv.uploadDir(), v.ID+".json"))
	ug, ok := sv.readGroup(v.Group)
	if !ok {
		ug = uploadGroup{Group: v.Group, Kind: v.Kind, GroupFiles: v.GroupFiles}
	}
	if sv.groupCount(v.Group) < ug.GroupFiles {
		return "", nil
	}
	dir := sv.targetDir(ug.Kind)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	ents, err := os.ReadDir(sv.groupDir(v.Group))
	if err != nil {
		return "", err
	}
	var last string
	// So two finishing can't both find a name free:
	defer sv.ups.lock("finish")()
	for _, e := range ents { // the folder (one, unless the client sent several)
		to := freeName(dir, e.Name(), e.IsDir())
		if err := os.Rename(filepath.Join(sv.groupDir(v.Group), e.Name()), to); err != nil {
			return "", err
		}
		sv.uploaded(to)
		last = to
	}
	os.Remove(sv.groupDir(v.Group))
	os.Remove(sv.groupFile(v.Group))
	sv.markDone(v.uploadMeta, last)
	return last, nil
}

// addGroups adds to a list of unfinished uploads how far each folder has
// got, and lists folders with nothing going right now, leaving out those
// untouched for uploadMaxAge.
func (sv *Server) addGroups(out []uploadView) []uploadView {
	ents, _ := os.ReadDir(sv.uploadDir())
	for _, e := range ents {
		g, ok := strings.CutSuffix(e.Name(), ".group")
		if !ok || !validUploadID(g) {
			continue
		}
		ug, ok := sv.readGroup(g)
		if !ok {
			continue
		}
		done, newest := 0, ug.Started
		filepath.WalkDir(sv.groupDir(g), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				done++
				if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
					newest = info.ModTime()
				}
			}
			return nil
		})
		going := false
		for i := range out {
			if out[i].Group == g {
				out[i].GroupDone, going = done, true
				if out[i].Updated.After(newest) {
					newest = out[i].Updated
				}
			}
		}
		if !going && time.Since(newest) > uploadMaxAge {
			continue // sweepGroups removes it
		}
		if !going {
			name := g
			if fs, _ := os.ReadDir(sv.groupDir(g)); len(fs) > 0 {
				name = fs[0].Name()
			}
			out = append(out, uploadView{uploadMeta: uploadMeta{Name: name, Kind: ug.Kind, Started: ug.Started,
				Group: g, GroupFiles: ug.GroupFiles}, Updated: newest, GroupDone: done})
		}
	}
	return out
}

// sweepGroups removes the folders listUploads leaves out: untouched for
// uploadMaxAge. (Called by sweepUploads, with the start lock held.)
func (sv *Server) sweepGroups() {
	live := map[string]bool{}
	for _, v := range sv.listUploads() {
		live[v.Group] = true
	}
	ents, _ := os.ReadDir(sv.uploadDir())
	for _, e := range ents {
		if g, ok := strings.CutSuffix(e.Name(), ".group"); ok && validUploadID(g) && !live[g] {
			sv.cancelGroup(g)
		}
	}
}

// cancelGroup gives up on a folder: what's in, and what's still going.
func (sv *Server) cancelGroup(g string) bool {
	if !validUploadID(g) {
		return false
	}
	if _, ok := sv.readGroup(g); !ok {
		return false
	}
	ents, _ := os.ReadDir(sv.uploadDir())
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			if v, err := sv.readUpload(id); err == nil && v.Group == g {
				sv.removeUpload(id)
			}
		}
	}
	os.RemoveAll(sv.groupDir(g))
	os.Remove(sv.groupFile(g))
	return true
}
