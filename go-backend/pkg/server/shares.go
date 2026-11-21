package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Link di condivisione temporanei: permettono di dare un singolo file a chi non
// ha l'app. Il link porta a una pagina pubblica (/s/<token>) che mostra il nome
// del file e il pulsante «Scarica»; la pagina non scarica nulla da sola, così le
// anteprime automatiche dei messaggi non consumano il limite di download.

const (
	minShareTTL     = time.Minute
	maxShareTTL     = 30 * 24 * time.Hour
	defaultShareTTL = 7 * 24 * time.Hour
	maxShareFails   = 10
	shareLockTime   = 15 * time.Minute
)

// Share è un link di condivisione (come salvato su disco).
type Share struct {
	ID           string    `json:"id"`
	Token        string    `json:"token"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	MaxDownloads int       `json:"maxDownloads"` // 0 = illimitati
	Downloads    int       `json:"downloads"`
	PassSalt     string    `json:"passSalt,omitempty"`
	PassHash     string    `json:"passHash,omitempty"`
}

// ShareInfo è la vista di un link mostrata a app e GUI (senza hash della password).
type ShareInfo struct {
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	MaxDownloads int       `json:"maxDownloads"`
	Downloads    int       `json:"downloads"`
	HasPassword  bool      `json:"hasPassword"`
	// TrustedURL è falso quando il link usa il certificato autofirmato di FireShare:
	// il browser di chi lo riceve mostrerà un avviso di sicurezza.
	TrustedURL bool `json:"trustedUrl"`
}

type shareFail struct {
	count int
	until time.Time
}

type shareStore struct {
	mu    sync.Mutex
	path  string
	items map[string]*Share // per ID
	fails map[string]*shareFail
}

func loadShareStore(path string) (*shareStore, error) {
	st := &shareStore{path: path, items: map[string]*Share{}, fails: map[string]*shareFail{}}
	data, err := os.ReadFile(path)
	if isNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Share
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("shares.json non valido: %w", err)
	}
	for _, sh := range list {
		st.items[sh.ID] = sh
	}
	return st, nil
}

func (st *shareStore) saveLocked() error {
	list := make([]*Share, 0, len(st.items))
	for _, sh := range st.items {
		list = append(list, sh)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(st.path, data, 0o600)
}

// purgeLocked elimina i link scaduti da più di un giorno (per non far crescere il file).
func (st *shareStore) purgeLocked(now time.Time) {
	for id, sh := range st.items {
		if now.After(sh.ExpiresAt.Add(24 * time.Hour)) {
			delete(st.items, id)
		}
	}
}

func hashSharePassword(salt, password string) string {
	h := sha256.Sum256([]byte(salt + ":" + password))
	for i := 0; i < 100000; i++ { // rallenta i tentativi a forza bruta
		h = sha256.Sum256(append(h[:], salt...))
	}
	return hex.EncodeToString(h[:])
}

func (st *shareStore) create(rel, name, createdBy string, ttl time.Duration, password string, maxDownloads int) (*Share, error) {
	if ttl <= 0 {
		ttl = defaultShareTTL
	}
	if ttl < minShareTTL || ttl > maxShareTTL {
		return nil, newErr(http.StatusBadRequest, "bad_ttl", "la durata deve essere tra 1 minuto e 30 giorni")
	}
	if maxDownloads < 0 || maxDownloads > 100000 {
		return nil, newErr(http.StatusBadRequest, "bad_limit", "limite di download non valido")
	}
	if len(password) > 128 {
		return nil, newErr(http.StatusBadRequest, "bad_password", "password troppo lunga")
	}
	id, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	token, err := randomToken(18) // 144 bit
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sh := &Share{
		ID: id, Token: token, Path: rel, Name: name, CreatedBy: createdBy,
		CreatedAt: now, ExpiresAt: now.Add(ttl), MaxDownloads: maxDownloads,
	}
	if password != "" {
		salt, err := randomHex(8)
		if err != nil {
			return nil, err
		}
		sh.PassSalt, sh.PassHash = salt, hashSharePassword(salt, password)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.purgeLocked(now)
	st.items[id] = sh
	if err := st.saveLocked(); err != nil {
		delete(st.items, id)
		return nil, err
	}
	return sh, nil
}

func (st *shareStore) list() []*Share {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.purgeLocked(time.Now())
	out := make([]*Share, 0, len(st.items))
	for _, sh := range st.items {
		c := *sh
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (st *shareStore) revoke(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.items[id]; !ok {
		return false
	}
	delete(st.items, id)
	_ = st.saveLocked()
	return true
}

// findActive cerca un link per token. Scaduto, esaurito o inesistente sono
// indistinguibili dall'esterno (nessuna informazione a chi indovina i token).
func (st *shareStore) findActive(token string) (Share, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for _, sh := range st.items {
		if subtle.ConstantTimeCompare([]byte(sh.Token), []byte(token)) == 1 {
			if now.After(sh.ExpiresAt) || (sh.MaxDownloads > 0 && sh.Downloads >= sh.MaxDownloads) {
				return Share{}, false
			}
			return *sh, true
		}
	}
	return Share{}, false
}

// checkPassword verifica la password con blocco temporaneo dopo troppi errori.
func (st *shareStore) checkPassword(sh Share, password string) (ok bool, locked bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f := st.fails[sh.ID]
	if f != nil && time.Now().Before(f.until) {
		return false, true
	}
	got := hashSharePassword(sh.PassSalt, password)
	if subtle.ConstantTimeCompare([]byte(got), []byte(sh.PassHash)) == 1 {
		delete(st.fails, sh.ID)
		return true, false
	}
	if f == nil {
		f = &shareFail{}
		st.fails[sh.ID] = f
	}
	f.count++
	if f.count >= maxShareFails {
		f.count = 0
		f.until = time.Now().Add(shareLockTime)
		return false, true
	}
	return false, false
}

// consumeDownload registra un download; restituisce false se il limite è esaurito.
func (st *shareStore) consumeDownload(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	sh, ok := st.items[id]
	if !ok || time.Now().After(sh.ExpiresAt) {
		return false
	}
	if sh.MaxDownloads > 0 && sh.Downloads >= sh.MaxDownloads {
		return false
	}
	sh.Downloads++
	_ = st.saveLocked()
	return true
}

// --- API autenticata -----------------------------------------------------------------

// shareBase è l'indirizzo da usare nei link: ShareBaseURL se configurato (meglio se
// con certificato valido), altrimenti il primo indirizzo pubblico, altrimenti il primo
// indirizzo di rete.
func (s *Server) shareBase() (base string, trusted bool) {
	if u := strings.TrimRight(strings.TrimSpace(s.opts.ShareBaseURL), "/"); u != "" {
		return u, true
	}
	urls := s.URLs()
	for _, u := range s.opts.PublicURLs {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u != "" {
			return u, false
		}
	}
	if len(urls) > 0 {
		return urls[0], false
	}
	return "", false
}

func (s *Server) shareInfo(sh *Share) ShareInfo {
	base, trusted := s.shareBase()
	return ShareInfo{
		ID: sh.ID, Path: sh.Path, Name: sh.Name, URL: base + "/s/" + sh.Token,
		CreatedBy: sh.CreatedBy, CreatedAt: sh.CreatedAt, ExpiresAt: sh.ExpiresAt,
		MaxDownloads: sh.MaxDownloads, Downloads: sh.Downloads,
		HasPassword: sh.PassHash != "", TrustedURL: trusted,
	}
}

// Shares elenca i link attivi (per la GUI desktop).
func (s *Server) Shares() []ShareInfo {
	list := s.shares.list()
	out := make([]ShareInfo, 0, len(list))
	for _, sh := range list {
		out = append(out, s.shareInfo(sh))
	}
	return out
}

// RevokeShare annulla un link: smette subito di funzionare.
func (s *Server) RevokeShare(id string) bool { return s.shares.revoke(id) }

type createShareRequest struct {
	Path             string `json:"path"`
	ExpiresInSeconds int64  `json:"expiresInSeconds"`
	Password         string `json:"password"`
	MaxDownloads     int    `json:"maxDownloads"`
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request, dev Device) {
	var req createShareRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}
	abs, info, err := s.mediaFile(req.Path)
	if err != nil {
		if ae, ok := asAPIError(err); ok && ae.Code == "is_a_dir" {
			err = newErr(http.StatusBadRequest, "is_a_dir", "si possono condividere solo file, non cartelle")
		}
		writeError(w, s.logger, err)
		return
	}
	_ = abs
	rel, _ := cleanRel(req.Path)
	sh, err := s.shares.create(rel, info.Name(), dev.Name, time.Duration(req.ExpiresInSeconds)*time.Second, req.Password, req.MaxDownloads)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.shareInfo(sh))
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request, _ Device) {
	writeJSON(w, http.StatusOK, map[string]any{"shares": s.Shares()})
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request, _ Device) {
	if !s.shares.revoke(r.PathValue("id")) {
		writeError(w, s.logger, errNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- pagina pubblica -------------------------------------------------------------------

func publicHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
}

const pageStyle = `body{font-family:system-ui,sans-serif;background:#111827;color:#f3f4f6;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
.box{background:#1f2937;border-radius:14px;padding:28px 32px;max-width:420px;width:calc(100% - 48px)}
h1{font-size:20px;margin:0 0 6px}p{color:#9ca3af;margin:6px 0}
button,input{font:inherit;border-radius:8px;border:0;padding:10px 14px;width:100%;box-sizing:border-box;margin-top:12px}
input{background:#111827;color:#fff;border:1px solid #374151}button{background:#ea580c;color:#fff;cursor:pointer}`

func writePage(w http.ResponseWriter, status int, body string) {
	publicHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="it"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>FireShare</title><style>%s</style></head><body><div class="box">%s</div></body></html>`, pageStyle, body)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func (s *Server) sharePageNotFound(w http.ResponseWriter) {
	writePage(w, http.StatusNotFound, `<h1>Link non valido</h1><p>Il link è scaduto, è stato annullato oppure non esiste.</p>`)
}

// handleSharePage: GET /s/{token}: pagina con il nome del file (non scarica nulla).
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.shares.findActive(r.PathValue("token"))
	if !ok {
		s.sharePageNotFound(w)
		return
	}
	_, info, err := s.mediaFile(sh.Path)
	if err != nil {
		s.sharePageNotFound(w)
		return
	}
	var pw string
	if sh.PassHash != "" {
		pw = `<input type="password" name="p" placeholder="Password" required autofocus autocomplete="off">`
	}
	writePage(w, http.StatusOK, fmt.Sprintf(
		`<h1>%s</h1><p>%s · disponibile fino al %s</p><form method="post" action="/s/%s/download">%s<button type="submit">Scarica</button></form>`,
		html.EscapeString(sh.Name), humanSize(info.Size()),
		sh.ExpiresAt.Local().Format("02/01/2006 15:04"), html.EscapeString(sh.Token), pw))
}

// handleShareDownload: GET|POST /s/{token}/download.
// Con password serve il POST della form; senza password basta anche il GET diretto.
func (s *Server) handleShareDownload(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.shares.findActive(r.PathValue("token"))
	if !ok {
		s.sharePageNotFound(w)
		return
	}
	if sh.PassHash != "" {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/s/"+sh.Token, http.StatusSeeOther)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			s.sharePageNotFound(w)
			return
		}
		okPw, locked := s.shares.checkPassword(sh, r.PostForm.Get("p"))
		if locked {
			writePage(w, http.StatusTooManyRequests, `<h1>Troppi tentativi</h1><p>Riprova tra qualche minuto.</p>`)
			return
		}
		if !okPw {
			writePage(w, http.StatusForbidden, fmt.Sprintf(
				`<h1>Password errata</h1><form method="post" action="/s/%s/download"><input type="password" name="p" placeholder="Password" required autofocus autocomplete="off"><button type="submit">Riprova</button></form>`,
				html.EscapeString(sh.Token)))
			return
		}
	}
	abs, info, err := s.mediaFile(sh.Path)
	if err != nil {
		s.sharePageNotFound(w)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		s.sharePageNotFound(w)
		return
	}
	defer f.Close()

	// conta solo l'inizio di un download (non le riprese con Range, né HEAD)
	rng := r.Header.Get("Range")
	if r.Method != http.MethodHead && (rng == "" || strings.HasPrefix(rng, "bytes=0-")) {
		if !s.shares.consumeDownload(sh.ID) {
			s.sharePageNotFound(w)
			return
		}
	}
	publicHeaders(w)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(info.Name())}))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
