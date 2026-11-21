package server

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config è la configurazione persistente del server (config.json).
type Config struct {
	ServerName  string   `json:"serverName"`
	Port        int      `json:"port"`
	RootDir     string   `json:"rootDir"`
	PublicURLs  []string `json:"publicUrls"`  // es. https://miopc.duckdns.org:8443
	MaxFileSize int64    `json:"maxFileSize"` // 0 = nessun limite

	ShareBaseURL  string `json:"shareBaseUrl"`  // indirizzo da usare nei link di condivisione
	AutoPortMap   bool   `json:"autoPortMap"`   // apre la porta sul router (UPnP)
	DuckDNSDomain string `json:"duckdnsDomain"` // es. "miopc" per miopc.duckdns.org
	DuckDNSToken  string `json:"duckdnsToken"`
}

// DefaultConfigDir è la cartella dove il server tiene certificato, dispositivi e config.
func DefaultConfigDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	return filepath.Join(base, "FireShare")
}

// DefaultConfig restituisce i valori iniziali.
func DefaultConfig() Config {
	name, _ := os.Hostname()
	if name == "" {
		name = "FireShare"
	}
	root := "FireShare"
	if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, "FireShare")
	}
	return Config{ServerName: name, Port: 8443, RootDir: root}
}

// LoadConfig legge config.json; se manca restituisce i valori di default.
func LoadConfig(configDir string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if isNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig(), err
	}
	def := DefaultConfig()
	if cfg.ServerName == "" {
		cfg.ServerName = def.ServerName
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = def.Port
	}
	if cfg.RootDir == "" {
		cfg.RootDir = def.RootDir
	}
	return cfg, nil
}

// Save scrive config.json.
func (c Config) Save(configDir string) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(configDir, "config.json"), data, 0o600)
}

// Options costruisce le opzioni del server a partire dalla configurazione.
func (c Config) Options(configDir string, hooks Hooks) Options {
	return Options{
		ConfigDir:   configDir,
		RootDir:     c.RootDir,
		ServerName:  c.ServerName,
		Port:        c.Port,
		PublicURLs:  c.PublicURLs,
		MaxFileSize: c.MaxFileSize,
		Hooks:       hooks,

		ShareBaseURL:  c.ShareBaseURL,
		AutoPortMap:   c.AutoPortMap,
		DuckDNSDomain: c.DuckDNSDomain,
		DuckDNSToken:  c.DuckDNSToken,
	}
}
