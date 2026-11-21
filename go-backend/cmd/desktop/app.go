package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fireshare/go-backend/pkg/qr"
	"github.com/fireshare/go-backend/pkg/server"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App è l'oggetto esposto al frontend Vue: avvia il server HTTPS di FireShare
// dentro l'applicazione desktop e ne permette il controllo dalla GUI.
type App struct {
	ctx       context.Context
	configDir string

	mu       sync.Mutex
	srv      *server.Server
	startErr string
}

// NewApp crea l'applicazione.
func NewApp() *App {
	return &App{configDir: server.DefaultConfigDir()}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.start()
}

func (a *App) shutdown(ctx context.Context) {
	a.stop()
}

func (a *App) emit(event string, data any) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, event, data)
	}
}

func (a *App) start() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startErr = ""

	cfg, err := server.LoadConfig(a.configDir)
	if err != nil {
		a.startErr = fmt.Sprintf("config.json non valido: %v", err)
		return
	}
	srv, err := server.New(cfg.Options(a.configDir, server.Hooks{
		OnPaired:         func(d server.DeviceInfo) { a.emit("device:paired", toDeviceView(d)) },
		OnUploadProgress: func(p server.UploadProgress) { a.emit("upload:progress", p) },
		OnUploadComplete: func(c server.CompletedUpload) { a.emit("upload:complete", c) },
	}))
	if err != nil {
		a.startErr = fmt.Sprintf("avvio del server fallito: %v", err)
		return
	}
	a.srv = srv
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			a.mu.Lock()
			if a.srv == srv {
				a.startErr = fmt.Sprintf("il server si è fermato: %v (porta già in uso?)", err)
			}
			a.mu.Unlock()
			a.emit("server:error", err.Error())
		}
	}()
}

func (a *App) stop() {
	a.mu.Lock()
	srv := a.srv
	a.srv = nil
	a.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// --- tipi esposti al frontend ----------------------------------------------------

// DeviceView è un telefono accoppiato (date in formato RFC 3339).
type DeviceView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	LastSeen  string `json:"lastSeen"`
}

func toDeviceView(d server.DeviceInfo) DeviceView {
	return DeviceView{
		ID: d.ID, Name: d.Name,
		CreatedAt: d.CreatedAt.Format(time.RFC3339),
		LastSeen:  d.LastSeen.Format(time.RFC3339),
	}
}

// Status è lo stato del server mostrato nella GUI.
type Status struct {
	Running     bool         `json:"running"`
	Error       string       `json:"error"`
	Version     string       `json:"version"`
	ServerName  string       `json:"serverName"`
	Port        int          `json:"port"`
	RootDir     string       `json:"rootDir"`
	Fingerprint string       `json:"fingerprint"`
	URLs        []string     `json:"urls"`
	Devices     []DeviceView `json:"devices"`
}

// PairingData è quanto serve alla GUI per mostrare il QR di accoppiamento.
type PairingData struct {
	Code        string   `json:"code"`
	ExpiresAt   string   `json:"expiresAt"`
	Payload     string   `json:"payload"`
	QRBase64    string   `json:"qrBase64"`
	URLs        []string `json:"urls"`
	Fingerprint string   `json:"fingerprint"`
}

// Settings sono le impostazioni modificabili dalla GUI.
type Settings struct {
	ServerName string   `json:"serverName"`
	Port       int      `json:"port"`
	RootDir    string   `json:"rootDir"`
	PublicURLs []string `json:"publicUrls"`

	ShareBaseURL  string `json:"shareBaseUrl"`
	AutoPortMap   bool   `json:"autoPortMap"`
	DuckDNSDomain string `json:"duckdnsDomain"`
	// DuckDNSToken è solo in ingresso: se vuoto con DuckDNSTokenSet vero si
	// mantiene quello già salvato (il token non torna mai alla GUI).
	DuckDNSToken    string `json:"duckdnsToken"`
	DuckDNSTokenSet bool   `json:"duckdnsTokenSet"`
}

// RemoteView è lo stato dell'accesso da fuori casa mostrato nella GUI.
type RemoteView struct {
	PortMapEnabled bool     `json:"portMapEnabled"`
	PortMapState   string   `json:"portMapState"` // off | searching | mapped | failed
	PortMapMessage string   `json:"portMapMessage"`
	Gateway        string   `json:"gateway"`
	ExternalIP     string   `json:"externalIp"`
	CGNAT          bool     `json:"cgnat"`
	DuckDNSEnabled bool     `json:"duckdnsEnabled"`
	DuckDNSState   string   `json:"duckdnsState"` // off | ok | failed
	DuckDNSMessage string   `json:"duckdnsMessage"`
	DuckDNSDomain  string   `json:"duckdnsDomain"`
	Tailscale      []string `json:"tailscale"`
	PublicURLs     []string `json:"publicUrls"`
	Hints          []string `json:"hints"`
	CheckedAt      string   `json:"checkedAt"`
}

// ShareView è un link di condivisione attivo.
type ShareView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	URL          string `json:"url"`
	CreatedBy    string `json:"createdBy"`
	CreatedAt    string `json:"createdAt"`
	ExpiresAt    string `json:"expiresAt"`
	MaxDownloads int    `json:"maxDownloads"`
	Downloads    int    `json:"downloads"`
	HasPassword  bool   `json:"hasPassword"`
	TrustedURL   bool   `json:"trustedUrl"`
}

func toRemoteView(r server.RemoteStatus) RemoteView {
	v := RemoteView{
		PortMapEnabled: r.PortMap.Enabled,
		PortMapState:   r.PortMap.State,
		PortMapMessage: r.PortMap.Message,
		Gateway:        r.PortMap.Gateway,
		ExternalIP:     r.ExternalIP,
		CGNAT:          r.CGNAT,
		DuckDNSEnabled: r.DuckDNS.Enabled,
		DuckDNSState:   r.DuckDNS.State,
		DuckDNSMessage: r.DuckDNS.Message,
		DuckDNSDomain:  r.DuckDNS.Domain,
		Tailscale:      r.Tailscale,
		PublicURLs:     r.PublicURLs,
		Hints:          r.Hints,
	}
	if !r.CheckedAt.IsZero() {
		v.CheckedAt = r.CheckedAt.Format(time.RFC3339)
	}
	if v.Tailscale == nil {
		v.Tailscale = []string{}
	}
	if v.PublicURLs == nil {
		v.PublicURLs = []string{}
	}
	if v.Hints == nil {
		v.Hints = []string{}
	}
	return v
}

// --- metodi esposti al frontend --------------------------------------------------

// GetStatus restituisce lo stato corrente del server.
func (a *App) GetStatus() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := Status{Error: a.startErr, Version: server.Version, URLs: []string{}, Devices: []DeviceView{}}
	if a.srv == nil {
		return st
	}
	st.Running = a.startErr == ""
	st.ServerName = a.srv.ServerName()
	st.Port = a.srv.Port()
	st.RootDir = a.srv.RootDir()
	st.Fingerprint = a.srv.Fingerprint()
	st.URLs = a.srv.URLs()
	for _, d := range a.srv.Devices() {
		st.Devices = append(st.Devices, toDeviceView(d))
	}
	return st
}

// StartPairing genera un nuovo codice (valido 10 minuti) e il relativo QR.
func (a *App) StartPairing() (PairingData, error) {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return PairingData{}, errors.New("il server non è in esecuzione")
	}
	info, err := srv.NewPairing(10 * time.Minute)
	if err != nil {
		return PairingData{}, err
	}
	png, err := qr.PNG(info.Payload, 320)
	if err != nil {
		return PairingData{}, err
	}
	return PairingData{
		Code:        info.Code,
		ExpiresAt:   info.ExpiresAt.Format(time.RFC3339),
		Payload:     info.Payload,
		QRBase64:    "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		URLs:        info.URLs,
		Fingerprint: info.Fingerprint,
	}, nil
}

// RevokeDevice disconnette un telefono accoppiato.
func (a *App) RevokeDevice(id string) bool {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	return srv != nil && srv.RevokeDevice(id)
}

// GetSettings legge le impostazioni salvate.
func (a *App) GetSettings() (Settings, error) {
	cfg, err := server.LoadConfig(a.configDir)
	if err != nil {
		return Settings{}, err
	}
	if cfg.PublicURLs == nil {
		cfg.PublicURLs = []string{}
	}
	return Settings{
		ServerName: cfg.ServerName, Port: cfg.Port, RootDir: cfg.RootDir, PublicURLs: cfg.PublicURLs,
		ShareBaseURL: cfg.ShareBaseURL, AutoPortMap: cfg.AutoPortMap,
		DuckDNSDomain: cfg.DuckDNSDomain, DuckDNSTokenSet: cfg.DuckDNSToken != "",
	}, nil
}

// SaveSettings salva le impostazioni e riavvia il server.
func (a *App) SaveSettings(s Settings) error {
	if strings.TrimSpace(s.RootDir) == "" {
		return errors.New("scegli la cartella dello spazio cloud")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("porta non valida (1-65535)")
	}
	cfg, err := server.LoadConfig(a.configDir)
	if err != nil {
		return err
	}
	cfg.ServerName = strings.TrimSpace(s.ServerName)
	cfg.Port = s.Port
	cfg.RootDir = strings.TrimSpace(s.RootDir)
	cfg.PublicURLs = nil
	for _, u := range s.PublicURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("l'indirizzo pubblico deve iniziare con https:// (%q)", u)
		}
		cfg.PublicURLs = append(cfg.PublicURLs, u)
	}

	base := strings.TrimSpace(s.ShareBaseURL)
	if base != "" && !strings.HasPrefix(base, "https://") {
		return fmt.Errorf("l'indirizzo base dei link deve iniziare con https:// (%q)", base)
	}
	cfg.ShareBaseURL = strings.TrimRight(base, "/")
	cfg.AutoPortMap = s.AutoPortMap

	domain := strings.ToLower(strings.TrimSpace(s.DuckDNSDomain))
	domain = strings.TrimSuffix(domain, ".duckdns.org")
	if domain != "" && !duckDomainRe.MatchString(domain) {
		return fmt.Errorf("nome DuckDNS non valido (%q): usa solo lettere, numeri e trattini, per esempio «miopc»", domain)
	}
	cfg.DuckDNSDomain = domain
	switch token := strings.TrimSpace(s.DuckDNSToken); {
	case domain == "":
		cfg.DuckDNSToken = ""
	case token != "":
		cfg.DuckDNSToken = token
	case !s.DuckDNSTokenSet:
		cfg.DuckDNSToken = ""
	}
	if domain != "" && cfg.DuckDNSToken == "" {
		return errors.New("per DuckDNS serve anche il token (lo trovi nella pagina di duckdns.org)")
	}

	if err := cfg.Save(a.configDir); err != nil {
		return err
	}
	a.stop()
	a.start()
	return nil
}

var duckDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,58}[a-z0-9])?$`)

// GetRemoteStatus restituisce l'ultimo stato noto dell'accesso da fuori casa
// (apertura della porta, DuckDNS, Tailscale) con i consigli in italiano.
func (a *App) GetRemoteStatus() RemoteView {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return toRemoteView(server.RemoteStatus{})
	}
	return toRemoteView(srv.RemoteStatus())
}

// CheckRemote rifà subito tutte le verifiche (router, IP pubblico, DuckDNS).
func (a *App) CheckRemote() RemoteView {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return toRemoteView(server.RemoteStatus{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return toRemoteView(srv.CheckRemote(ctx))
}

// GetShares elenca i link di condivisione attivi.
func (a *App) GetShares() []ShareView {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	out := []ShareView{}
	if srv == nil {
		return out
	}
	for _, sh := range srv.Shares() {
		out = append(out, ShareView{
			ID: sh.ID, Name: sh.Name, Path: sh.Path, URL: sh.URL, CreatedBy: sh.CreatedBy,
			CreatedAt: sh.CreatedAt.Format(time.RFC3339), ExpiresAt: sh.ExpiresAt.Format(time.RFC3339),
			MaxDownloads: sh.MaxDownloads, Downloads: sh.Downloads,
			HasPassword: sh.HasPassword, TrustedURL: sh.TrustedURL,
		})
	}
	return out
}

// RevokeShare annulla un link di condivisione.
func (a *App) RevokeShare(id string) bool {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	return srv != nil && srv.RevokeShare(id)
}

// OpenCloudFolder apre la cartella dello spazio cloud in Esplora file / Finder.
func (a *App) OpenCloudFolder() {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", srv.RootDir())
	case "darwin":
		cmd = exec.Command("open", srv.RootDir())
	default:
		cmd = exec.Command("xdg-open", srv.RootDir())
	}
	_ = cmd.Start() // explorer restituisce un codice di uscita diverso da 0 anche se funziona
}
