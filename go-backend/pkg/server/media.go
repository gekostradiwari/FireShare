package server

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MediaItem è una foto o un video dello spazio cloud (per la galleria dell'app).
type MediaItem struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Kind       string    `json:"kind"` // "image" | "video"
	Mime       string    `json:"mime,omitempty"`
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
	".webp": true, ".heic": true, ".heif": true, ".avif": true, ".tif": true, ".tiff": true,
}

var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".mkv": true, ".avi": true, ".webm": true,
	".3gp": true, ".m4v": true, ".mpg": true, ".mpeg": true,
}

func mediaKind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case imageExts[ext]:
		return "image"
	case videoExts[ext]:
		return "video"
	}
	return ""
}

// mediaIndex tiene in memoria l'elenco di foto e video, ordinato dal più recente.
// Viene ricostruito quando qualcosa cambia (upload, spostamento, eliminazione)
// oppure dopo qualche secondo, così le modifiche fatte direttamente sul PC
// compaiono comunque.
type mediaIndex struct {
	root string

	mu      sync.Mutex
	items   []MediaItem
	builtAt time.Time
	dirty   bool
}

const mediaIndexTTL = 20 * time.Second

func newMediaIndex(root string) *mediaIndex { return &mediaIndex{root: root, dirty: true} }

// invalidate segnala che i file sono cambiati.
func (m *mediaIndex) invalidate() {
	m.mu.Lock()
	m.dirty = true
	m.mu.Unlock()
}

func (m *mediaIndex) snapshot() []MediaItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirty && time.Since(m.builtAt) < mediaIndexTTL {
		return m.items
	}
	items := make([]MediaItem, 0, len(m.items))
	_ = filepath.WalkDir(m.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != m.root && d.Name() == internalDirName && filepath.Dir(p) == m.root {
				return filepath.SkipDir
			}
			return nil
		}
		kind := mediaKind(d.Name())
		if kind == "" || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(m.root, p)
		if err != nil {
			return nil
		}
		it := MediaItem{
			Name: d.Name(), Path: filepath.ToSlash(rel), Size: info.Size(),
			ModifiedAt: info.ModTime().UTC(), Kind: kind,
			Mime: mime.TypeByExtension(strings.ToLower(filepath.Ext(d.Name()))),
		}
		items = append(items, it)
		return nil
	})
	sort.Slice(items, func(i, j int) bool {
		if !items[i].ModifiedAt.Equal(items[j].ModifiedAt) {
			return items[i].ModifiedAt.After(items[j].ModifiedAt)
		}
		return items[i].Path < items[j].Path
	})
	m.items, m.builtAt, m.dirty = items, time.Now(), false
	return items
}

// handleMedia: GET /api/v1/media?offset=0&limit=200&kind=all|image|video&dir=Cartella
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request, _ Device) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	kind := q.Get("kind")
	dir, err := cleanRel(q.Get("dir"))
	if err != nil {
		writeError(w, s.logger, err)
		return
	}

	all := s.media.snapshot()
	filtered := all
	if kind == "image" || kind == "video" || dir != "" {
		filtered = make([]MediaItem, 0, len(all))
		for _, it := range all {
			if (kind == "image" || kind == "video") && it.Kind != kind {
				continue
			}
			if dir != "" && !strings.HasPrefix(it.Path, dir+"/") {
				continue
			}
			filtered = append(filtered, it)
		}
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := filtered[offset:end]
	if items == nil {
		items = []MediaItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "offset": offset, "items": items})
}

// mediaDir restituisce il percorso assoluto di un file media già validato.
func (s *Server) mediaFile(rel string) (abs string, info os.FileInfo, err error) {
	rel, err = cleanRel(rel)
	if err != nil || rel == "" {
		return "", nil, errBadPath
	}
	abs, err = resolveInRoot(s.root, rel)
	if err != nil {
		return "", nil, err
	}
	info, err = os.Stat(abs)
	if err != nil {
		if isNotExist(err) {
			return "", nil, errNotFound
		}
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, newErr(http.StatusBadRequest, "is_a_dir", "è una cartella")
	}
	return abs, info, nil
}
