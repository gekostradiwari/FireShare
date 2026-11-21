package server

import (
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// listDir elenca il contenuto di una cartella dello spazio cloud.
func (s *Server) listDir(rel string) ([]FileInfo, error) {
	abs, err := resolveInRoot(s.root, rel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if isNotExist(err) {
			return nil, errNotFound
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, newErr(http.StatusBadRequest, "not_a_dir", "non è una cartella")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if rel == "" && strings.EqualFold(e.Name(), internalDirName) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue // i link simbolici non vengono esposti
		}
		out = append(out, makeFileInfo(filepath.Join(abs, e.Name()), path.Join(rel, e.Name()), info))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request, _ Device) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	entries, err := s.listDir(rel)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "entries": entries})
}

// handleContent serve un file con supporto a Range (download ripristinabile).
func (s *Server) handleContent(w http.ResponseWriter, r *http.Request, _ Device) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		writeError(w, s.logger, errBadPath)
		return
	}
	abs, err := resolveInRoot(s.root, rel)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		if isNotExist(err) {
			writeError(w, s.logger, errNotFound)
		} else {
			writeError(w, s.logger, err)
		}
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if st.IsDir() {
		writeError(w, s.logger, newErr(http.StatusBadRequest, "is_a_dir", "è una cartella"))
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": st.Name()}))
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request, _ Device) {
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil || rel == "" {
		writeError(w, s.logger, errBadPath)
		return
	}
	abs, err := resolveInRoot(s.root, rel)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	st, err := os.Lstat(abs)
	if err != nil {
		if isNotExist(err) {
			writeError(w, s.logger, errNotFound)
		} else {
			writeError(w, s.logger, err)
		}
		return
	}
	if st.IsDir() {
		if r.URL.Query().Get("recursive") == "true" {
			err = os.RemoveAll(abs)
		} else {
			err = os.Remove(abs)
			if err != nil {
				writeError(w, s.logger, newErr(http.StatusConflict, "not_empty", "la cartella non è vuota"))
				return
			}
		}
	} else {
		err = os.Remove(abs)
	}
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	s.media.invalidate()
	w.WriteHeader(http.StatusNoContent)
}

type mkdirRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request, _ Device) {
	var req mkdirRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}
	rel, err := sanitizeDir(req.Path)
	if err != nil || rel == "" {
		writeError(w, s.logger, errBadPath)
		return
	}
	abs, err := resolveInRoot(s.root, rel)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if _, err := resolveInRoot(s.root, rel); err != nil {
		writeError(w, s.logger, err)
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, makeFileInfo(abs, rel, st))
}

type moveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request, _ Device) {
	var req moveRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}
	from, err := cleanRel(req.From)
	if err != nil || from == "" {
		writeError(w, s.logger, errBadPath)
		return
	}
	to, err := sanitizeDir(req.To)
	if err != nil || to == "" {
		writeError(w, s.logger, errBadPath)
		return
	}
	absFrom, err := resolveInRoot(s.root, from)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	absTo, err := resolveInRoot(s.root, to)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if _, err := os.Lstat(absFrom); err != nil {
		if isNotExist(err) {
			writeError(w, s.logger, errNotFound)
		} else {
			writeError(w, s.logger, err)
		}
		return
	}
	if _, err := os.Lstat(absTo); err == nil {
		writeError(w, s.logger, newErr(http.StatusConflict, "exists", "esiste già un elemento con questo nome"))
		return
	}
	if err := os.MkdirAll(filepath.Dir(absTo), 0o755); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if err := os.Rename(absFrom, absTo); err != nil {
		writeError(w, s.logger, err)
		return
	}
	st, err := os.Stat(absTo)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	s.media.invalidate()
	writeJSON(w, http.StatusOK, makeFileInfo(absTo, to, st))
}
