package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Device è un telefono accoppiato. Del token si conserva solo l'hash SHA-256.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

// DeviceInfo è la vista pubblica di un Device (senza hash del token).
type DeviceInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
}

func (d Device) info() DeviceInfo {
	return DeviceInfo{ID: d.ID, Name: d.Name, CreatedAt: d.CreatedAt, LastSeen: d.LastSeen}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type deviceStore struct {
	mu     sync.Mutex
	path   string
	byID   map[string]*Device
	byHash map[string]*Device
}

func loadDeviceStore(path string) (*deviceStore, error) {
	ds := &deviceStore{path: path, byID: map[string]*Device{}, byHash: map[string]*Device{}}
	data, err := os.ReadFile(path)
	if isNotExist(err) {
		return ds, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Device
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("devices.json non valido: %w", err)
	}
	for _, d := range list {
		ds.byID[d.ID] = d
		ds.byHash[d.TokenHash] = d
	}
	return ds, nil
}

func (ds *deviceStore) add(name string) (Device, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return Device{}, "", err
	}
	id, err := randomHex(6)
	if err != nil {
		return Device{}, "", err
	}
	now := time.Now().UTC()
	d := &Device{ID: id, Name: name, TokenHash: hashToken(token), CreatedAt: now, LastSeen: now}

	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.byID[d.ID] = d
	ds.byHash[d.TokenHash] = d
	if err := ds.saveLocked(); err != nil {
		delete(ds.byID, d.ID)
		delete(ds.byHash, d.TokenHash)
		return Device{}, "", err
	}
	return *d, token, nil
}

func (ds *deviceStore) authenticate(token string) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	h := hashToken(token)
	ds.mu.Lock()
	defer ds.mu.Unlock()
	d, ok := ds.byHash[h]
	if !ok {
		return Device{}, false
	}
	d.LastSeen = time.Now().UTC()
	return *d, true
}

func (ds *deviceStore) revoke(id string) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	d, ok := ds.byID[id]
	if !ok {
		return false
	}
	delete(ds.byID, id)
	delete(ds.byHash, d.TokenHash)
	if err := ds.saveLocked(); err != nil {
		// meglio tenere il dispositivo revocato in memoria che riabilitarlo
		return true
	}
	return true
}

func (ds *deviceStore) list() []DeviceInfo {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	out := make([]DeviceInfo, 0, len(ds.byID))
	for _, d := range ds.byID {
		out = append(out, d.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (ds *deviceStore) saveLocked() error {
	list := make([]*Device, 0, len(ds.byID))
	for _, d := range ds.byID {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(ds.path, data, 0o600)
}

// --- pairing -----------------------------------------------------------------

const (
	pairingAlphabet    = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // senza I, L, O, 0, 1
	pairingCodeLen     = 8
	pairingMaxAttempts = 5
)

// PairingInfo è quello che il desktop mostra all'utente per accoppiare un telefono.
type PairingInfo struct {
	Code        string    `json:"code"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Payload     string    `json:"payload"` // contenuto del QR code
	URLs        []string  `json:"urls"`
	Fingerprint string    `json:"fingerprint"`
}

type pairingManager struct {
	mu       sync.Mutex
	code     string
	expires  time.Time
	attempts int
}

func newPairingCode() (string, error) {
	max := big.NewInt(int64(len(pairingAlphabet)))
	out := make([]byte, pairingCodeLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = pairingAlphabet[n.Int64()]
	}
	return string(out), nil
}

func formatCode(code string) string {
	if len(code) == pairingCodeLen {
		return code[:4] + "-" + code[4:]
	}
	return code
}

func normalizeCode(code string) string {
	code = strings.ToUpper(code)
	code = strings.NewReplacer("-", "", " ", "").Replace(code)
	return code
}

func (p *pairingManager) create(ttl time.Duration) (string, time.Time, error) {
	code, err := newPairingCode()
	if err != nil {
		return "", time.Time{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code = code
	p.expires = time.Now().Add(ttl)
	p.attempts = 0
	return code, p.expires, nil
}

// consume verifica il codice. Un codice vale una sola volta, scade, e dopo
// pairingMaxAttempts tentativi sbagliati viene invalidato.
func (p *pairingManager) consume(code string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.code == "" || time.Now().After(p.expires) {
		p.code = ""
		return false
	}
	if subtle.ConstantTimeCompare([]byte(normalizeCode(code)), []byte(p.code)) == 1 {
		p.code = ""
		return true
	}
	p.attempts++
	if p.attempts >= pairingMaxAttempts {
		p.code = ""
	}
	return false
}

// buildPairingPayload costruisce il contenuto del QR code:
//
//	fireshare://pair?v=1&c=CODICE&fp=IMPRONTA&n=NOME&u=URL1&u=URL2
func buildPairingPayload(code, fingerprint, serverName string, urls []string) string {
	v := url.Values{}
	v.Set("v", "1")
	v.Set("c", code)
	v.Set("fp", fingerprint)
	v.Set("n", serverName)
	for _, u := range urls {
		v.Add("u", u)
	}
	return "fireshare://pair?" + v.Encode()
}
