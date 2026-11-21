package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Il protocollo di upload è ispirato a TUS:
//
//	POST   /api/v1/uploads        crea (o riprende, se clientKey è già noto) un upload
//	GET    /api/v1/uploads/{id}   stato e offset attuale
//	PATCH  /api/v1/uploads/{id}   aggiunge byte a partire da "Upload-Offset"
//	DELETE /api/v1/uploads/{id}   annulla
//
// La fonte di verità per l'offset è la dimensione del file .part sul disco: così
// non esiste un contatore che possa andare fuori sincrono.

type createUploadRequest struct {
	Name       string     `json:"name"`
	Dir        string     `json:"dir"`
	Size       int64      `json:"size"`
	SHA256     string     `json:"sha256"`
	ClientKey  string     `json:"clientKey"`
	ModifiedAt *time.Time `json:"modifiedAt"`
	Overwrite  bool       `json:"overwrite"`
}

// UploadStatus è la risposta degli endpoint /uploads.
type UploadStatus struct {
	ID     string    `json:"id"`
	Status string    `json:"status"` // "incomplete" | "complete"
	Offset int64     `json:"offset"`
	Size   int64     `json:"size"`
	File   *FileInfo `json:"file,omitempty"`
}

type uploadMeta struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Dir        string     `json:"dir"`
	Size       int64      `json:"size"`
	SHA256     string     `json:"sha256,omitempty"`
	ClientKey  string     `json:"clientKey,omitempty"`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
	Overwrite  bool       `json:"overwrite,omitempty"`
	DeviceID   string     `json:"deviceId"`
	DeviceName string     `json:"deviceName"`
	CreatedAt  time.Time  `json:"createdAt"`
}

type completedRecord struct {
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256,omitempty"`
	CompletedAt time.Time `json:"completedAt"`
}

type uploadManager struct {
	root      string
	dir       string // <root>/.fireshare/uploads
	indexPath string
	maxSize   int64
	hooks     *Hooks
	onChange  func() // chiamata dopo ogni upload completato (indice media)

	mu        sync.Mutex
	active    map[string]*uploadMeta
	byKey     map[string]string
	locks     map[string]*sync.Mutex
	completed map[string]completedRecord
}

func newUploadManager(root string, maxSize int64, hooks *Hooks) (*uploadManager, error) {
	m := &uploadManager{
		root:      root,
		dir:       filepath.Join(root, internalDirName, "uploads"),
		indexPath: filepath.Join(root, internalDirName, "index.json"),
		maxSize:   maxSize,
		hooks:     hooks,
		active:    map[string]*uploadMeta{},
		byKey:     map[string]string{},
		locks:     map[string]*sync.Mutex{},
		completed: map[string]completedRecord{},
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(m.indexPath); err == nil {
		_ = json.Unmarshal(data, &m.completed)
		if m.completed == nil {
			m.completed = map[string]completedRecord{}
		}
	} else if !isNotExist(err) {
		return nil, err
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var meta uploadMeta
		if json.Unmarshal(data, &meta) != nil || meta.ID == "" {
			continue
		}
		if _, err := os.Stat(m.partPath(meta.ID)); err != nil {
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
			continue
		}
		mc := meta
		m.active[mc.ID] = &mc
		if mc.ClientKey != "" {
			m.byKey[mc.ClientKey] = mc.ID
		}
	}
	return m, nil
}

func (m *uploadManager) partPath(id string) string { return filepath.Join(m.dir, id+".part") }
func (m *uploadManager) metaPath(id string) string { return filepath.Join(m.dir, id+".json") }

func (m *uploadManager) lockFor(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	return l
}

func (m *uploadManager) saveIndexLocked() {
	data, err := json.Marshal(m.completed)
	if err == nil {
		_ = atomicWriteFile(m.indexPath, data, 0o600)
	}
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (m *uploadManager) create(req createUploadRequest, dev Device) (UploadStatus, bool, error) {
	if req.Size < 0 {
		return UploadStatus{}, false, newErr(http.StatusBadRequest, "bad_size", "dimensione non valida")
	}
	if m.maxSize > 0 && req.Size > m.maxSize {
		return UploadStatus{}, false, newErr(http.StatusRequestEntityTooLarge, "too_large", "file troppo grande")
	}
	sha := strings.ToLower(strings.TrimSpace(req.SHA256))
	if sha != "" && !isHexSHA256(sha) {
		return UploadStatus{}, false, newErr(http.StatusBadRequest, "bad_sha256", "SHA-256 non valido")
	}
	dir, err := sanitizeDir(req.Dir)
	if err != nil {
		return UploadStatus{}, false, err
	}
	name := sanitizeName(req.Name)
	if len(req.ClientKey) > 512 {
		return UploadStatus{}, false, newErr(http.StatusBadRequest, "bad_key", "clientKey troppo lunga")
	}

	if req.ClientKey != "" {
		if st, ok := m.lookupCompleted(req.ClientKey, req.Size); ok {
			return st, false, nil
		}
		m.mu.Lock()
		id, ok := m.byKey[req.ClientKey]
		var existing *uploadMeta
		if ok {
			existing = m.active[id]
		}
		m.mu.Unlock()
		if existing != nil {
			if existing.Size == req.Size {
				st, err := m.status(existing.ID)
				return st, false, err
			}
			// stessa chiave ma file cambiato: riparte da zero
			if err := m.discard(existing.ID); err != nil {
				return UploadStatus{}, false, err
			}
		}
	}

	id, err := randomHex(12)
	if err != nil {
		return UploadStatus{}, false, err
	}
	meta := &uploadMeta{
		ID: id, Name: name, Dir: dir, Size: req.Size, SHA256: sha, ClientKey: req.ClientKey,
		ModifiedAt: req.ModifiedAt, Overwrite: req.Overwrite,
		DeviceID: dev.ID, DeviceName: dev.Name, CreatedAt: time.Now().UTC(),
	}
	f, err := os.OpenFile(m.partPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return UploadStatus{}, false, err
	}
	f.Close()
	data, err := json.Marshal(meta)
	if err == nil {
		err = atomicWriteFile(m.metaPath(id), data, 0o600)
	}
	if err != nil {
		_ = os.Remove(m.partPath(id))
		return UploadStatus{}, false, err
	}
	m.mu.Lock()
	m.active[id] = meta
	if meta.ClientKey != "" {
		m.byKey[meta.ClientKey] = id
	}
	m.mu.Unlock()

	if req.Size == 0 {
		// niente da trasferire: si chiude subito
		lk := m.lockFor(id)
		lk.Lock()
		defer lk.Unlock()
		fi, err := m.finalize(meta)
		if err != nil {
			return UploadStatus{}, false, err
		}
		return UploadStatus{ID: id, Status: "complete", Offset: 0, Size: 0, File: &fi}, true, nil
	}
	return UploadStatus{ID: id, Status: "incomplete", Offset: 0, Size: req.Size}, true, nil
}

// lookupCompleted dice se la clientKey corrisponde a un file già caricato e
// ancora presente sul disco con la stessa dimensione.
func (m *uploadManager) lookupCompleted(key string, size int64) (UploadStatus, bool) {
	m.mu.Lock()
	rec, ok := m.completed[key]
	m.mu.Unlock()
	if !ok || rec.Size != size {
		return UploadStatus{}, false
	}
	abs, err := resolveInRoot(m.root, rec.Path)
	if err != nil {
		return UploadStatus{}, false
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() || st.Size() != size {
		m.mu.Lock()
		delete(m.completed, key)
		m.saveIndexLocked()
		m.mu.Unlock()
		return UploadStatus{}, false
	}
	fi := makeFileInfo(abs, rec.Path, st)
	return UploadStatus{ID: "", Status: "complete", Offset: size, Size: size, File: &fi}, true
}

// checkComplete restituisce quali clientKey risultano già caricate.
func (m *uploadManager) checkComplete(keys []string) []string {
	out := []string{}
	m.mu.Lock()
	recs := make(map[string]completedRecord, len(keys))
	for _, k := range keys {
		if r, ok := m.completed[k]; ok {
			recs[k] = r
		}
	}
	m.mu.Unlock()
	for k, r := range recs {
		if _, ok := m.lookupCompleted(k, r.Size); ok {
			out = append(out, k)
		}
	}
	return out
}

func (m *uploadManager) get(id string) (*uploadMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, ok := m.active[id]
	if !ok {
		return nil, errNotFound
	}
	return meta, nil
}

func (m *uploadManager) status(id string) (UploadStatus, error) {
	meta, err := m.get(id)
	if err != nil {
		return UploadStatus{}, err
	}
	st, err := os.Stat(m.partPath(id))
	if err != nil {
		return UploadStatus{}, errNotFound
	}
	return UploadStatus{ID: id, Status: "incomplete", Offset: st.Size(), Size: meta.Size}, nil
}

// patch aggiunge i byte di body all'upload, a partire da offset.
func (m *uploadManager) patch(id string, offset int64, body io.Reader) (UploadStatus, error) {
	meta, err := m.get(id)
	if err != nil {
		return UploadStatus{}, err
	}
	lk := m.lockFor(id)
	if !lk.TryLock() {
		return UploadStatus{}, newErr(http.StatusLocked, "locked", "upload già in corso su un'altra connessione")
	}
	defer lk.Unlock()

	// riletto dopo aver preso il lock: potrebbe essere stato annullato nel frattempo
	if _, err := m.get(id); err != nil {
		return UploadStatus{}, err
	}
	part := m.partPath(id)
	st, err := os.Stat(part)
	if err != nil {
		return UploadStatus{}, errNotFound
	}
	cur := st.Size()
	if offset != cur {
		return UploadStatus{}, &apiError{Status: http.StatusConflict, Code: "offset_mismatch",
			Message: "offset non coincidente", Offset: &cur}
	}
	remaining := meta.Size - cur
	if remaining > 0 {
		f, err := os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return UploadStatus{}, err
		}
		pw := &progressWriter{w: f, base: cur, total: meta.Size, meta: meta, hooks: m.hooks}
		n, copyErr := io.Copy(pw, io.LimitReader(body, remaining))
		cur += n
		if copyErr == nil && cur == meta.Size {
			copyErr = f.Sync()
		}
		closeErr := f.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if m.hooks != nil && m.hooks.OnUploadProgress != nil {
			m.hooks.OnUploadProgress(progressOf(meta, cur))
		}
		if copyErr != nil {
			return UploadStatus{}, newErr(http.StatusBadRequest, "io", "trasferimento interrotto")
		}
		if cur == meta.Size {
			// controllo che il client non abbia mandato più byte del dichiarato
			var probe [1]byte
			if k, _ := body.Read(probe[:]); k > 0 {
				return UploadStatus{}, newErr(http.StatusRequestEntityTooLarge, "too_much_data",
					"ricevuti più byte della dimensione dichiarata")
			}
		}
	} else {
		// già completo sul disco ma non finalizzato (crash prima del rename):
		// un PATCH vuoto è il modo per chiuderlo.
		var probe [1]byte
		if k, _ := body.Read(probe[:]); k > 0 {
			return UploadStatus{}, newErr(http.StatusRequestEntityTooLarge, "too_much_data",
				"ricevuti più byte della dimensione dichiarata")
		}
	}

	if cur < meta.Size {
		return UploadStatus{ID: id, Status: "incomplete", Offset: cur, Size: meta.Size}, nil
	}
	fi, err := m.finalize(meta)
	if err != nil {
		return UploadStatus{}, err
	}
	return UploadStatus{ID: id, Status: "complete", Offset: meta.Size, Size: meta.Size, File: &fi}, nil
}

// finalize verifica l'hash, sceglie il nome definitivo e sposta il .part.
// Va chiamata con il lock dell'upload già preso.
func (m *uploadManager) finalize(meta *uploadMeta) (FileInfo, error) {
	part := m.partPath(meta.ID)

	if meta.SHA256 != "" {
		actual, err := hashFile(part)
		if err != nil {
			return FileInfo{}, err
		}
		if actual != meta.SHA256 {
			m.forget(meta)
			return FileInfo{}, newErr(http.StatusUnprocessableEntity, "hash_mismatch",
				"il file ricevuto non corrisponde all'hash SHA-256 atteso: caricamento da ripetere")
		}
	}

	dirAbs, err := resolveInRoot(m.root, meta.Dir)
	if err != nil {
		return FileInfo{}, err
	}
	if err := os.MkdirAll(dirAbs, 0o755); err != nil {
		return FileInfo{}, err
	}
	// dopo mkdir si ricontrolla che nessun symlink porti fuori dalla radice
	if _, err := resolveInRoot(m.root, meta.Dir); err != nil {
		return FileInfo{}, err
	}
	target, err := reserveTarget(dirAbs, meta.Name, meta.Overwrite)
	if err != nil {
		return FileInfo{}, err
	}
	if err := os.Rename(part, target); err != nil {
		return FileInfo{}, err
	}
	// il .part nasce 0600: il file definitivo deve essere leggibile come un file normale
	_ = os.Chmod(target, 0o644)
	if meta.ModifiedAt != nil && !meta.ModifiedAt.IsZero() {
		_ = os.Chtimes(target, *meta.ModifiedAt, *meta.ModifiedAt)
	}
	st, err := os.Stat(target)
	if err != nil {
		return FileInfo{}, err
	}
	rel := path.Join(meta.Dir, filepath.Base(target))
	fi := makeFileInfo(target, rel, st)

	_ = os.Remove(m.metaPath(meta.ID))
	m.mu.Lock()
	delete(m.active, meta.ID)
	if meta.ClientKey != "" {
		if m.byKey[meta.ClientKey] == meta.ID {
			delete(m.byKey, meta.ClientKey)
		}
		m.completed[meta.ClientKey] = completedRecord{
			Path: rel, Size: meta.Size, SHA256: meta.SHA256, CompletedAt: time.Now().UTC(),
		}
		m.saveIndexLocked()
	}
	delete(m.locks, meta.ID)
	m.mu.Unlock()

	if m.onChange != nil {
		m.onChange()
	}
	if m.hooks != nil && m.hooks.OnUploadComplete != nil {
		m.hooks.OnUploadComplete(CompletedUpload{File: fi, DeviceName: meta.DeviceName})
	}
	return fi, nil
}

// forget elimina file e metadati di un upload.
func (m *uploadManager) forget(meta *uploadMeta) {
	_ = os.Remove(m.partPath(meta.ID))
	_ = os.Remove(m.metaPath(meta.ID))
	m.mu.Lock()
	delete(m.active, meta.ID)
	if meta.ClientKey != "" && m.byKey[meta.ClientKey] == meta.ID {
		delete(m.byKey, meta.ClientKey)
	}
	delete(m.locks, meta.ID)
	m.mu.Unlock()
}

// discard annulla un upload (se nessun PATCH è in corso).
func (m *uploadManager) discard(id string) error {
	meta, err := m.get(id)
	if err != nil {
		return err
	}
	lk := m.lockFor(id)
	if !lk.TryLock() {
		return newErr(http.StatusLocked, "locked", "upload in corso")
	}
	defer lk.Unlock()
	m.forget(meta)
	return nil
}

// cleanStale elimina gli upload incompleti non toccati da più di maxAge.
func (m *uploadManager) cleanStale(maxAge time.Duration) int {
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	removed := 0
	for _, id := range ids {
		st, err := os.Stat(m.partPath(id))
		if err != nil {
			continue
		}
		if time.Since(st.ModTime()) > maxAge {
			if m.discard(id) == nil {
				removed++
			}
		}
	}
	return removed
}

// --- utilità ---------------------------------------------------------------

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// reserveTarget sceglie un nome libero ("foto.jpg", "foto (1).jpg", ...) creando
// il file in modo esclusivo, così due upload concorrenti non si sovrascrivono.
func reserveTarget(dirAbs, name string, overwrite bool) (string, error) {
	if overwrite {
		return filepath.Join(dirAbs, name), nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base, ext = name, ""
	}
	for i := 0; i < 10000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		p := filepath.Join(dirAbs, candidate)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return p, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return "", err
	}
	return "", errors.New("troppi file con lo stesso nome")
}

func makeFileInfo(abs, rel string, st os.FileInfo) FileInfo {
	fi := FileInfo{
		Name:       st.Name(),
		Path:       rel,
		IsDir:      st.IsDir(),
		ModifiedAt: st.ModTime().UTC(),
	}
	if !st.IsDir() {
		fi.Size = st.Size()
		fi.Mime = mime.TypeByExtension(strings.ToLower(filepath.Ext(abs)))
	}
	return fi
}

func progressOf(meta *uploadMeta, offset int64) UploadProgress {
	return UploadProgress{
		ID: meta.ID, Name: meta.Name, Dir: meta.Dir, Size: meta.Size,
		Offset: offset, DeviceName: meta.DeviceName,
	}
}

// progressWriter segnala l'avanzamento al massimo ogni 250 ms.
type progressWriter struct {
	w     io.Writer
	base  int64
	n     int64
	total int64
	meta  *uploadMeta
	hooks *Hooks
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if p.hooks != nil && p.hooks.OnUploadProgress != nil && time.Since(p.last) > 250*time.Millisecond {
		p.last = time.Now()
		p.hooks.OnUploadProgress(progressOf(p.meta, p.base+p.n))
	}
	return n, err
}
