package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configura il server.
type Options struct {
	ConfigDir   string // certificato TLS, dispositivi accoppiati, config.json
	RootDir     string // cartella che diventa lo spazio cloud
	ServerName  string
	Port        int // 0 = porta casuale (utile nei test)
	PublicURLs  []string
	MaxFileSize int64
	Hooks       Hooks
	Logger      *log.Logger

	// Link di condivisione: indirizzo da usare nei link (meglio se con certificato valido).
	ShareBaseURL string
	// Accesso da fuori casa (vedi remote.go).
	AutoPortMap   bool
	DuckDNSDomain string
	DuckDNSToken  string
}

// Server è il backend HTTPS di FireShare.
type Server struct {
	opts        Options
	root        string
	logger      *log.Logger
	devices     *deviceStore
	pairing     *pairingManager
	uploads     *uploadManager
	media       *mediaIndex
	shares      *shareStore
	remote      *remoteManager
	cert        tls.Certificate
	fingerprint string
	hooks       *Hooks
	handler     http.Handler

	mu       sync.Mutex
	listener net.Listener
	httpSrv  *http.Server
}

// New prepara il server (cartelle, certificato, dispositivi) senza metterlo in ascolto.
func New(opts Options) (*Server, error) {
	if opts.ConfigDir == "" || opts.RootDir == "" {
		return nil, errors.New("ConfigDir e RootDir sono obbligatori")
	}
	if err := os.MkdirAll(opts.ConfigDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.RootDir, 0o755); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(opts.RootDir)
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	if opts.ServerName == "" {
		opts.ServerName, _ = os.Hostname()
		if opts.ServerName == "" {
			opts.ServerName = "FireShare"
		}
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "[fireshare] ", log.LstdFlags)
	}

	cert, fp, err := loadOrCreateCert(opts.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("certificato TLS: %w", err)
	}
	devices, err := loadDeviceStore(filepath.Join(opts.ConfigDir, "devices.json"))
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts: opts, root: root, logger: logger,
		devices: devices, pairing: &pairingManager{},
		cert: cert, fingerprint: fp,
	}
	hooks := opts.Hooks
	s.hooks = &hooks
	s.uploads, err = newUploadManager(root, opts.MaxFileSize, s.hooks)
	if err != nil {
		return nil, err
	}
	s.uploads.cleanStale(7 * 24 * time.Hour)
	s.media = newMediaIndex(root)
	s.uploads.onChange = s.media.invalidate
	s.shares, err = loadShareStore(filepath.Join(opts.ConfigDir, "shares.json"))
	if err != nil {
		return nil, err
	}
	s.remote = newRemoteManager(remoteSettings{
		AutoPortMap: opts.AutoPortMap, DuckDNSDomain: strings.TrimSpace(opts.DuckDNSDomain), DuckDNSToken: strings.TrimSpace(opts.DuckDNSToken),
	}, s.Port, logger)
	s.handler = s.routes()
	return s, nil
}

// Handler restituisce l'http.Handler (utile nei test).
func (s *Server) Handler() http.Handler { return s.handler }

// Fingerprint è l'impronta SHA-256 del certificato (da pinnare sul telefono).
func (s *Server) Fingerprint() string { return s.fingerprint }

// RootDir è la cartella dello spazio cloud (percorso reale, senza symlink).
func (s *Server) RootDir() string { return s.root }

// ServerName è il nome mostrato sul telefono.
func (s *Server) ServerName() string { return s.opts.ServerName }

// Devices elenca i telefoni accoppiati.
func (s *Server) Devices() []DeviceInfo { return s.devices.list() }

// RevokeDevice disconnette un telefono: il suo token smette subito di funzionare.
func (s *Server) RevokeDevice(id string) bool { return s.devices.revoke(id) }

// Port è la porta in ascolto (o quella configurata se non ancora avviato).
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		if a, ok := s.listener.Addr().(*net.TCPAddr); ok {
			return a.Port
		}
	}
	return s.opts.Port
}

// URLs elenca gli indirizzi con cui il telefono può raggiungere il server.
func (s *Server) URLs() []string {
	port := strconv.Itoa(s.Port())
	var urls []string
	for _, ip := range localIPv4s() {
		urls = append(urls, "https://"+net.JoinHostPort(ip, port))
	}
	for _, u := range s.opts.PublicURLs {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u != "" {
			urls = append(urls, u)
		}
	}
	for _, u := range s.remote.publicURLs() {
		dup := false
		for _, x := range urls {
			if x == u {
				dup = true
				break
			}
		}
		if !dup {
			urls = append(urls, u)
		}
	}
	return urls
}

// RemoteStatus è lo stato dell'accesso da fuori casa (UPnP, DuckDNS, Tailscale, CGNAT).
func (s *Server) RemoteStatus() RemoteStatus { return s.remote.Status() }

// CheckRemote rifà subito tutti i controlli, compresa la ricerca dell'IP pubblico.
func (s *Server) CheckRemote(ctx context.Context) RemoteStatus { return s.remote.refresh(ctx, true) }

// NewPairing crea un codice di accoppiamento valido per ttl e il contenuto del QR.
func (s *Server) NewPairing(ttl time.Duration) (PairingInfo, error) {
	code, exp, err := s.pairing.create(ttl)
	if err != nil {
		return PairingInfo{}, err
	}
	urls := s.URLs()
	return PairingInfo{
		Code:        formatCode(code),
		ExpiresAt:   exp,
		Payload:     buildPairingPayload(code, s.fingerprint, s.opts.ServerName, urls),
		URLs:        urls,
		Fingerprint: s.fingerprint,
	}, nil
}

// ListenAndServe ascolta su tutte le interfacce alla porta configurata (HTTPS).
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.opts.Port))
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve avvia il server HTTPS sul listener dato. Ritorna nil dopo Shutdown.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          s.logger,
	}
	s.mu.Lock()
	s.listener = ln
	s.httpSrv = srv
	s.mu.Unlock()
	s.remote.start()
	err := srv.ServeTLS(ln, "", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown ferma il server in modo ordinato.
func (s *Server) Shutdown(ctx context.Context) error {
	s.remote.stop()
	s.mu.Lock()
	srv := s.httpSrv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// --- routing ----------------------------------------------------------------

type authedHandler func(w http.ResponseWriter, r *http.Request, dev Device)

func (s *Server) auth(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, ok := s.devices.authenticate(bearerToken(r))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fireshare"`)
			writeError(w, s.logger, newErr(http.StatusUnauthorized, "unauthorized", "autenticazione richiesta"))
			return
		}
		h(w, r, dev)
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/pair", s.handlePair)

	mux.HandleFunc("GET /api/v1/info", s.auth(s.handleInfo))
	mux.HandleFunc("DELETE /api/v1/me", s.auth(s.handleUnpair))

	mux.HandleFunc("POST /api/v1/uploads", s.auth(s.handleCreateUpload))
	mux.HandleFunc("POST /api/v1/uploads/check", s.auth(s.handleCheckUploads))
	mux.HandleFunc("GET /api/v1/uploads/{id}", s.auth(s.handleGetUpload))
	mux.HandleFunc("PATCH /api/v1/uploads/{id}", s.auth(s.handlePatchUpload))
	mux.HandleFunc("DELETE /api/v1/uploads/{id}", s.auth(s.handleDeleteUpload))

	mux.HandleFunc("GET /api/v1/files", s.auth(s.handleList))
	mux.HandleFunc("GET /api/v1/files/content", s.auth(s.handleContent))
	mux.HandleFunc("DELETE /api/v1/files", s.auth(s.handleDeleteFile))
	mux.HandleFunc("POST /api/v1/files/mkdir", s.auth(s.handleMkdir))
	mux.HandleFunc("POST /api/v1/files/move", s.auth(s.handleMove))
	mux.HandleFunc("GET /api/v1/files/thumb", s.auth(s.handleThumb))
	mux.HandleFunc("GET /api/v1/media", s.auth(s.handleMedia))

	mux.HandleFunc("GET /api/v1/shares", s.auth(s.handleListShares))
	mux.HandleFunc("POST /api/v1/shares", s.auth(s.handleCreateShare))
	mux.HandleFunc("DELETE /api/v1/shares/{id}", s.auth(s.handleDeleteShare))

	// pagina pubblica dei link di condivisione (nessun token richiesto)
	mux.HandleFunc("GET /s/{token}", s.handleSharePage)
	mux.HandleFunc("GET /s/{token}/download", s.handleShareDownload)
	mux.HandleFunc("POST /s/{token}/download", s.handleShareDownload)

	return s.recoverer(s.secureHeaders(mux))
}

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Printf("panic su %s %s: %v", r.Method, r.URL.Path, rec)
				writeError(w, s.logger, newErr(http.StatusInternalServerError, "internal", "errore interno del server"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- handler ------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": s.opts.ServerName, "version": Version, "apiVersion": APIVersion,
	})
}

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if !s.pairing.consume(req.Code) {
		writeError(w, s.logger, newErr(http.StatusUnauthorized, "bad_code", "codice di accoppiamento non valido o scaduto"))
		return
	}
	name := sanitizeName(req.DeviceName)
	if name == "file" || len(strings.TrimSpace(req.DeviceName)) == 0 {
		name = "Telefono"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	dev, token, err := s.devices.add(name)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	if s.hooks.OnPaired != nil {
		s.hooks.OnPaired(dev.info())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "deviceId": dev.ID,
		"serverName": s.opts.ServerName, "apiVersion": APIVersion,
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request, dev Device) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": s.opts.ServerName, "version": Version, "apiVersion": APIVersion,
		"deviceId": dev.ID, "deviceName": dev.Name,
		"maxFileSize": s.opts.MaxFileSize,
		"urls":        s.URLs(),
	})
}

func (s *Server) handleUnpair(w http.ResponseWriter, r *http.Request, dev Device) {
	s.devices.revoke(dev.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request, dev Device) {
	var req createUploadRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}
	st, created, err := s.uploads.create(req, dev)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, st)
}

type checkRequest struct {
	ClientKeys []string `json:"clientKeys"`
}

func (s *Server) handleCheckUploads(w http.ResponseWriter, r *http.Request, _ Device) {
	var req checkRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, s.logger, err)
		return
	}
	if len(req.ClientKeys) > 5000 {
		writeError(w, s.logger, newErr(http.StatusBadRequest, "too_many", "massimo 5000 chiavi per richiesta"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"complete": s.uploads.checkComplete(req.ClientKeys)})
}

func (s *Server) handleGetUpload(w http.ResponseWriter, r *http.Request, _ Device) {
	st, err := s.uploads.status(r.PathValue("id"))
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(st.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(st.Size, 10))
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handlePatchUpload(w http.ResponseWriter, r *http.Request, _ Device) {
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, s.logger, newErr(http.StatusBadRequest, "bad_offset", "header Upload-Offset mancante o non valido"))
		return
	}
	st, err := s.uploads.patch(r.PathValue("id"), offset, r.Body)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(st.Offset, 10))
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleDeleteUpload(w http.ResponseWriter, r *http.Request, _ Device) {
	if err := s.uploads.discard(r.PathValue("id")); err != nil {
		writeError(w, s.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
