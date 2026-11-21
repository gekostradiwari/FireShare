package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- helper ------------------------------------------------------------------

type env struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	token string
	cfg   string
	root  string
}

func newEnv(t *testing.T, hooks Hooks) *env {
	t.Helper()
	cfg := t.TempDir()
	root := filepath.Join(t.TempDir(), "cloud")
	return newEnvAt(t, cfg, root, hooks, true)
}

func newEnvAt(t *testing.T, cfg, root string, hooks Hooks, pair bool) *env {
	t.Helper()
	srv, err := New(Options{ConfigDir: cfg, RootDir: root, ServerName: "Test PC", Hooks: hooks,
		Logger: nil})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	e := &env{t: t, srv: srv, ts: ts, cfg: cfg, root: srv.RootDir()}
	if pair {
		e.token = e.pair("Pixel Test")
	}
	return e
}

func (e *env) pair(name string) string {
	e.t.Helper()
	info, err := e.srv.NewPairing(time.Minute)
	if err != nil {
		e.t.Fatal(err)
	}
	code, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": info.Code, "deviceName": name})
	if st != 200 {
		e.t.Fatalf("pair: status %d %v", st, code)
	}
	return code["token"].(string)
}

func (e *env) req(method, path, token string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	r, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) doJSON(method, path, token string, v any) (map[string]any, int) {
	e.t.Helper()
	var body io.Reader
	hdr := map[string]string{}
	if v != nil {
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	resp, b := e.req(method, path, token, body, hdr)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out, resp.StatusCode
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

// upload esegue un upload completo a pezzi di chunk byte e restituisce la risposta finale.
func (e *env) upload(name, dir string, data []byte, chunk int, extra map[string]any) map[string]any {
	e.t.Helper()
	req := map[string]any{"name": name, "dir": dir, "size": len(data), "sha256": sum(data)}
	for k, v := range extra {
		req[k] = v
	}
	st, code := e.doJSON("POST", "/api/v1/uploads", e.token, req)
	if code != 201 && code != 200 {
		e.t.Fatalf("create upload: %d %v", code, st)
	}
	if st["status"] == "complete" {
		return st
	}
	id := st["id"].(string)
	off := int(st["offset"].(float64))
	for {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		resp, b := e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(data[off:end]),
			map[string]string{"Upload-Offset": fmt.Sprint(off)})
		if resp.StatusCode != 200 {
			e.t.Fatalf("patch: %d %s", resp.StatusCode, b)
		}
		out := map[string]any{}
		_ = json.Unmarshal(b, &out)
		off = int(out["offset"].(float64))
		if out["status"] == "complete" {
			return out
		}
	}
}

// --- test --------------------------------------------------------------------

func TestPairingFlow(t *testing.T) {
	var paired []DeviceInfo
	e := newEnv(t, Hooks{OnPaired: func(d DeviceInfo) { paired = append(paired, d) }})
	if len(paired) != 1 || paired[0].Name != "Pixel Test" {
		t.Fatalf("hook OnPaired non chiamato: %+v", paired)
	}
	// senza token
	if _, st := e.doJSON("GET", "/api/v1/info", "", nil); st != 401 {
		t.Fatalf("senza token atteso 401, ottenuto %d", st)
	}
	// token sbagliato
	if _, st := e.doJSON("GET", "/api/v1/info", "abc", nil); st != 401 {
		t.Fatalf("token errato atteso 401, ottenuto %d", st)
	}
	// token buono
	if info, st := e.doJSON("GET", "/api/v1/info", e.token, nil); st != 200 || info["deviceName"] != "Pixel Test" {
		t.Fatalf("info: %d %v", st, info)
	}
	// il codice è monouso: senza un nuovo NewPairing non si può accoppiare
	if _, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": "ABCD-EFGH", "deviceName": "x"}); st != 401 {
		t.Fatalf("pairing senza codice attivo atteso 401, ottenuto %d", st)
	}
	// health è pubblico
	if _, st := e.doJSON("GET", "/api/v1/health", "", nil); st != 200 {
		t.Fatalf("health: %d", st)
	}
}

func TestPairingCodeSingleUseAndAttempts(t *testing.T) {
	e := newEnv(t, Hooks{})
	info, _ := e.srv.NewPairing(time.Minute)
	good := info.Code
	// 5 tentativi sbagliati invalidano il codice, anche quello giusto
	for i := 0; i < 5; i++ {
		if _, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": "AAAA-AAAA", "deviceName": "x"}); st != 401 {
			t.Fatalf("tentativo %d: atteso 401, ottenuto %d", i, st)
		}
	}
	if _, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": good, "deviceName": "x"}); st != 401 {
		t.Fatalf("dopo 5 errori il codice giusto non deve funzionare, ottenuto %d", st)
	}
	// codice monouso
	info, _ = e.srv.NewPairing(time.Minute)
	if _, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": strings.ToLower(info.Code), "deviceName": "uno"}); st != 200 {
		t.Fatalf("codice (minuscolo, con trattino) deve funzionare: %d", st)
	}
	if _, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": info.Code, "deviceName": "due"}); st != 401 {
		t.Fatalf("riuso del codice atteso 401, ottenuto %d", st)
	}
	// scadenza
	info, _ = e.srv.NewPairing(time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if _, st := e.doJSON("POST", "/api/v1/pair", "", map[string]string{"code": info.Code, "deviceName": "x"}); st != 401 {
		t.Fatalf("codice scaduto atteso 401, ottenuto %d", st)
	}
}

func TestPairingPayload(t *testing.T) {
	e := newEnv(t, Hooks{})
	info, _ := e.srv.NewPairing(time.Minute)
	u, err := url.Parse(info.Payload)
	if err != nil || u.Scheme != "fireshare" || u.Host != "pair" {
		t.Fatalf("payload non valido: %q (%v)", info.Payload, err)
	}
	q := u.Query()
	if q.Get("fp") != e.srv.Fingerprint() || len(q.Get("fp")) != 64 {
		t.Fatalf("impronta nel payload errata: %q", q.Get("fp"))
	}
	if normalizeCode(info.Code) != q.Get("c") {
		t.Fatalf("codice nel payload errato")
	}
	if q.Get("n") != "Test PC" || q.Get("v") != "1" {
		t.Fatalf("nome/versione errati: %v", q)
	}
}

func TestUploadHappyPath(t *testing.T) {
	var progress, complete int
	var mu sync.Mutex
	e := newEnv(t, Hooks{
		OnUploadProgress: func(UploadProgress) { mu.Lock(); progress++; mu.Unlock() },
		OnUploadComplete: func(CompletedUpload) { mu.Lock(); complete++; mu.Unlock() },
	})
	data := payload(300_000)
	out := e.upload("video.mp4", "Foto/2026", data, 100_000, nil)
	if out["status"] != "complete" {
		t.Fatalf("atteso complete: %v", out)
	}
	got, err := os.ReadFile(filepath.Join(e.root, "Foto", "2026", "video.mp4"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("contenuto file errato (err=%v)", err)
	}
	// nessun residuo nella cartella interna
	entries, _ := os.ReadDir(filepath.Join(e.root, internalDirName, "uploads"))
	if len(entries) != 0 {
		t.Fatalf("residui negli upload: %v", entries)
	}
	if complete != 1 || progress < 3 {
		t.Fatalf("hook: complete=%d progress=%d", complete, progress)
	}
	// listing
	list, st := e.doJSON("GET", "/api/v1/files?path=Foto/2026", e.token, nil)
	entriesAny := list["entries"].([]any)
	if st != 200 || len(entriesAny) != 1 || entriesAny[0].(map[string]any)["name"] != "video.mp4" {
		t.Fatalf("listing: %d %v", st, list)
	}
}

func TestUploadResumeAndOffsetMismatch(t *testing.T) {
	e := newEnv(t, Hooks{})
	data := payload(10_000)
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "a.bin", "size": len(data), "sha256": sum(data)})
	id := st["id"].(string)

	// primo pezzo
	resp, _ := e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(data[:4000]), map[string]string{"Upload-Offset": "0"})
	if resp.StatusCode != 200 {
		t.Fatalf("patch 1: %d", resp.StatusCode)
	}
	// stato dopo una "caduta di rete": il client chiede dov'è arrivato il server
	cur, _ := e.doJSON("GET", "/api/v1/uploads/"+id, e.token, nil)
	if int(cur["offset"].(float64)) != 4000 {
		t.Fatalf("offset atteso 4000: %v", cur)
	}
	// offset sbagliato (ad esempio un chunk ripetuto): 409 con l'offset corretto
	resp, b := e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(data[:4000]), map[string]string{"Upload-Offset": "0"})
	var conflict map[string]any
	_ = json.Unmarshal(b, &conflict)
	if resp.StatusCode != 409 || int(conflict["offset"].(float64)) != 4000 {
		t.Fatalf("atteso 409 con offset 4000: %d %s", resp.StatusCode, b)
	}
	// il contenuto non è stato alterato dal chunk duplicato
	part, _ := os.ReadFile(filepath.Join(e.root, internalDirName, "uploads", id+".part"))
	if len(part) != 4000 {
		t.Fatalf("dimensione .part dopo il 409: %d", len(part))
	}
	// ripresa corretta
	resp, b = e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(data[4000:]), map[string]string{"Upload-Offset": "4000"})
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"complete"`) {
		t.Fatalf("completamento: %d %s", resp.StatusCode, b)
	}
	got, _ := os.ReadFile(filepath.Join(e.root, "a.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("contenuto finale errato dopo la ripresa")
	}
}

func TestUploadResumeAfterServerRestart(t *testing.T) {
	cfg := t.TempDir()
	root := filepath.Join(t.TempDir(), "cloud")
	e := newEnvAt(t, cfg, root, Hooks{}, true)
	data := payload(9000)
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "r.bin", "size": len(data), "sha256": sum(data), "clientKey": "k1"})
	id := st["id"].(string)
	e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(data[:5000]), map[string]string{"Upload-Offset": "0"})
	e.ts.Close()

	// nuovo processo server sulla stessa cartella: il token resta valido e l'upload è ripreso
	e2 := newEnvAt(t, cfg, root, Hooks{}, false)
	e2.token = e.token
	again, code := e2.doJSON("POST", "/api/v1/uploads", e2.token, map[string]any{"name": "r.bin", "size": len(data), "sha256": sum(data), "clientKey": "k1"})
	if code != 200 || again["id"] != id || int(again["offset"].(float64)) != 5000 {
		t.Fatalf("ripresa dopo riavvio: %d %v", code, again)
	}
	resp, b := e2.req("PATCH", "/api/v1/uploads/"+id, e2.token, bytes.NewReader(data[5000:]), map[string]string{"Upload-Offset": "5000"})
	if resp.StatusCode != 200 {
		t.Fatalf("patch dopo riavvio: %d %s", resp.StatusCode, b)
	}
}

func TestUploadClientKeyIdempotent(t *testing.T) {
	e := newEnv(t, Hooks{})
	data := payload(5000)
	out := e.upload("p.jpg", "Foto", data, 5000, map[string]any{"clientKey": "ph:42"})
	if out["status"] != "complete" {
		t.Fatalf("%v", out)
	}
	// stessa chiave: il server risponde "complete" senza riceverlo di nuovo
	again, code := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "p.jpg", "dir": "Foto", "size": len(data), "clientKey": "ph:42"})
	if code != 200 || again["status"] != "complete" {
		t.Fatalf("atteso complete idempotente: %d %v", code, again)
	}
	// check bulk
	chk, _ := e.doJSON("POST", "/api/v1/uploads/check", e.token, map[string]any{"clientKeys": []string{"ph:42", "ph:43"}})
	keys := chk["complete"].([]any)
	if len(keys) != 1 || keys[0] != "ph:42" {
		t.Fatalf("check: %v", chk)
	}
	// se il file viene cancellato dal server, la chiave non vale più
	if err := os.Remove(filepath.Join(e.root, "Foto", "p.jpg")); err != nil {
		t.Fatal(err)
	}
	again, code = e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "p.jpg", "dir": "Foto", "size": len(data), "clientKey": "ph:42"})
	if code != 201 || again["status"] != "incomplete" {
		t.Fatalf("file rimosso: l'upload deve ripartire: %d %v", code, again)
	}
}

func TestUploadHashMismatch(t *testing.T) {
	e := newEnv(t, Hooks{})
	data := payload(2000)
	bad := sum([]byte("altro"))
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "x.bin", "size": len(data), "sha256": bad})
	id := st["id"].(string)
	resp, b := e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(data), map[string]string{"Upload-Offset": "0"})
	if resp.StatusCode != 422 || !strings.Contains(string(b), "hash_mismatch") {
		t.Fatalf("atteso 422 hash_mismatch: %d %s", resp.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(e.root, "x.bin")); err == nil {
		t.Fatal("un file corrotto non deve comparire nello spazio cloud")
	}
	if _, code := e.doJSON("GET", "/api/v1/uploads/"+id, e.token, nil); code != 404 {
		t.Fatalf("l'upload fallito deve sparire: %d", code)
	}
}

func TestUploadZeroAndOversize(t *testing.T) {
	e := newEnv(t, Hooks{})
	out, code := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "vuoto.txt", "size": 0})
	if code != 201 || out["status"] != "complete" {
		t.Fatalf("file vuoto: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(e.root, "vuoto.txt")); err != nil {
		t.Fatal(err)
	}
	// più byte del dichiarato
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "pochi.bin", "size": 10})
	id := st["id"].(string)
	resp, b := e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(payload(50)), map[string]string{"Upload-Offset": "0"})
	if resp.StatusCode != 413 {
		t.Fatalf("atteso 413: %d %s", resp.StatusCode, b)
	}
	// header mancante
	resp, _ = e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(nil), nil)
	if resp.StatusCode != 400 {
		t.Fatalf("atteso 400 senza Upload-Offset: %d", resp.StatusCode)
	}
}

func TestUploadPathTraversalIsContained(t *testing.T) {
	base := t.TempDir()
	cfg := t.TempDir()
	root := filepath.Join(base, "cloud")
	e := newEnvAt(t, cfg, root, Hooks{}, true)

	// ".." nella cartella: rifiutato
	for _, dir := range []string{"../fuori", "a/../../fuori", `..\fuori`} {
		if _, code := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "x.txt", "dir": dir, "size": 1}); code != 400 {
			t.Fatalf("dir %q: atteso 400, ottenuto %d", dir, code)
		}
	}
	// ".." nel nome: viene ridotto al solo nome del file
	data := []byte("x")
	e.upload("../../ESCAPED.txt", "", data, 1, nil)
	if _, err := os.Stat(filepath.Join(base, "ESCAPED.txt")); err == nil {
		t.Fatal("il file è uscito dalla cartella cloud")
	}
	if _, err := os.Stat(filepath.Join(e.root, "ESCAPED.txt")); err != nil {
		t.Fatalf("il file doveva finire come ESCAPED.txt dentro la radice: %v", err)
	}
	// nomi riservati e caratteri non validi
	e.upload("CON.txt", "", data, 1, nil)
	e.upload(`a:b*c?.txt`, "", data, 1, nil)
	if _, err := os.Stat(filepath.Join(e.root, "_CON.txt")); err != nil {
		t.Fatalf("nome riservato non rinominato: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "a_b_c_.txt")); err != nil {
		t.Fatalf("caratteri non validi non sostituiti: %v", err)
	}
	// la cartella interna non è usabile come destinazione
	e.upload("evil.txt", ".fireshare", data, 1, nil)
	if _, err := os.Stat(filepath.Join(e.root, "_.fireshare", "evil.txt")); err != nil {
		t.Fatalf("la cartella interna doveva essere rinominata: %v", err)
	}
}

func TestUploadNameCollisionAutoRename(t *testing.T) {
	e := newEnv(t, Hooks{})
	e.upload("IMG_1.jpg", "Foto", []byte("uno"), 10, nil)
	e.upload("IMG_1.jpg", "Foto", []byte("due"), 10, nil)
	e.upload("IMG_1.jpg", "Foto", []byte("tre"), 10, nil)
	for name, want := range map[string]string{"IMG_1.jpg": "uno", "IMG_1 (1).jpg": "due", "IMG_1 (2).jpg": "tre"} {
		b, err := os.ReadFile(filepath.Join(e.root, "Foto", name))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q (%v)", name, b, err)
		}
	}
	// overwrite esplicito
	e.upload("IMG_1.jpg", "Foto", []byte("nuovo"), 10, map[string]any{"overwrite": true})
	if b, _ := os.ReadFile(filepath.Join(e.root, "Foto", "IMG_1.jpg")); string(b) != "nuovo" {
		t.Fatalf("overwrite: %q", b)
	}
}

func TestUploadModifiedAtPreserved(t *testing.T) {
	e := newEnv(t, Hooks{})
	when := time.Date(2020, 5, 17, 10, 30, 0, 0, time.UTC)
	e.upload("old.jpg", "", []byte("1"), 10, map[string]any{"modifiedAt": when.Format(time.RFC3339)})
	st, err := os.Stat(filepath.Join(e.root, "old.jpg"))
	if err != nil || !st.ModTime().UTC().Equal(when) {
		t.Fatalf("data di modifica non conservata: %v (%v)", st.ModTime(), err)
	}
}

func TestConcurrentPatchIsLocked(t *testing.T) {
	e := newEnv(t, Hooks{})
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "c.bin", "size": 100})
	id := st["id"].(string)

	pr, pw := io.Pipe()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest("PATCH", e.ts.URL+"/api/v1/uploads/"+id, pr)
		req.Header.Set("Authorization", "Bearer "+e.token)
		req.Header.Set("Upload-Offset", "0")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	go func() {
		_, _ = pw.Write(payload(10))
		close(started)
	}()
	<-started
	time.Sleep(100 * time.Millisecond) // il primo PATCH tiene il lock, in attesa di altri byte
	resp, _ := e.req("PATCH", "/api/v1/uploads/"+id, e.token, bytes.NewReader(nil), map[string]string{"Upload-Offset": "0"})
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("atteso 423 Locked, ottenuto %d", resp.StatusCode)
	}
	pw.Close()
	<-done
}

func TestDownloadWithRange(t *testing.T) {
	e := newEnv(t, Hooks{})
	data := payload(5000)
	e.upload("d.bin", "Doc", data, 5000, nil)

	resp, body := e.req("GET", "/api/v1/files/content?path=Doc/d.bin", e.token, nil, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, data) {
		t.Fatalf("download completo: %d", resp.StatusCode)
	}
	resp, body = e.req("GET", "/api/v1/files/content?path=Doc/d.bin", e.token, nil, map[string]string{"Range": "bytes=100-199"})
	if resp.StatusCode != 206 || !bytes.Equal(body, data[100:200]) {
		t.Fatalf("range: %d len=%d", resp.StatusCode, len(body))
	}
	resp, body = e.req("GET", "/api/v1/files/content?path=Doc/d.bin", e.token, nil, map[string]string{"Range": "bytes=4900-"})
	if resp.StatusCode != 206 || !bytes.Equal(body, data[4900:]) {
		t.Fatalf("range aperto: %d len=%d", resp.StatusCode, len(body))
	}
	if resp, _ = e.req("GET", "/api/v1/files/content?path=Doc/nope", e.token, nil, nil); resp.StatusCode != 404 {
		t.Fatalf("inesistente: %d", resp.StatusCode)
	}
	if resp, _ = e.req("GET", "/api/v1/files/content?path=Doc", e.token, nil, nil); resp.StatusCode != 400 {
		t.Fatalf("cartella: %d", resp.StatusCode)
	}
	if resp, _ = e.req("GET", "/api/v1/files/content?path=Doc/d.bin", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("senza token: %d", resp.StatusCode)
	}
}

func TestFilesRejectTraversal(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "segreto.txt"), []byte("segreto"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEnvAt(t, t.TempDir(), filepath.Join(base, "cloud"), Hooks{}, true)
	e.upload("ok.txt", "", []byte("ok"), 10, nil)

	bad := []string{
		"../segreto.txt", "..%2Fsegreto.txt", "a/../../segreto.txt", `..\segreto.txt`,
		".fireshare/index.json", ".FIRESHARE", "ok.txt:stream", "C:/Windows/win.ini",
	}
	for _, p := range bad {
		for _, ep := range []string{"/api/v1/files/content?path=", "/api/v1/files?path="} {
			resp, body := e.req("GET", ep+url.QueryEscape(p), e.token, nil, nil)
			if resp.StatusCode == 200 || strings.Contains(string(body), "segreto") && !strings.Contains(string(body), "error") {
				t.Fatalf("%s%q non doveva funzionare: %d %s", ep, p, resp.StatusCode, body)
			}
		}
		resp, _ := e.req("DELETE", "/api/v1/files?path="+url.QueryEscape(p), e.token, nil, nil)
		if resp.StatusCode == 204 {
			t.Fatalf("DELETE %q non doveva funzionare", p)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "segreto.txt")); err != nil {
		t.Fatal("il file esterno è stato toccato")
	}
	// la cartella interna non compare nel listing della radice
	list, _ := e.doJSON("GET", "/api/v1/files", e.token, nil)
	for _, it := range list["entries"].([]any) {
		if it.(map[string]any)["name"] == internalDirName {
			t.Fatal("la cartella interna è visibile")
		}
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink richiedono privilegi su Windows")
	}
	base := t.TempDir()
	outside := filepath.Join(base, "fuori")
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "s.txt"), []byte("segreto"), 0o644)
	e := newEnvAt(t, t.TempDir(), filepath.Join(base, "cloud"), Hooks{}, true)
	if err := os.Symlink(outside, filepath.Join(e.root, "link")); err != nil {
		t.Skip("symlink non disponibili:", err)
	}
	for _, ep := range []string{"/api/v1/files/content?path=link/s.txt", "/api/v1/files?path=link"} {
		if resp, body := e.req("GET", ep, e.token, nil, nil); resp.StatusCode == 200 {
			t.Fatalf("%s: il symlink ha esposto file esterni: %s", ep, body)
		}
	}
	// un upload verso una cartella che è un symlink fuori radice deve fallire
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "w.txt", "dir": "link", "size": 1})
	id, _ := st["id"].(string)
	resp, _ := e.req("PATCH", "/api/v1/uploads/"+id, e.token, strings.NewReader("x"), map[string]string{"Upload-Offset": "0"})
	if resp.StatusCode == 200 {
		t.Fatal("upload attraverso symlink doveva fallire")
	}
	if _, err := os.Stat(filepath.Join(outside, "w.txt")); err == nil {
		t.Fatal("file scritto fuori dalla radice tramite symlink")
	}
}

func TestMkdirMoveDelete(t *testing.T) {
	e := newEnv(t, Hooks{})
	if _, code := e.doJSON("POST", "/api/v1/files/mkdir", e.token, map[string]string{"path": "Docs/Nuova"}); code != 201 {
		t.Fatalf("mkdir: %d", code)
	}
	if _, code := e.doJSON("POST", "/api/v1/files/mkdir", e.token, map[string]string{"path": "../x"}); code != 400 {
		t.Fatalf("mkdir traversal: %d", code)
	}
	e.upload("a.txt", "Docs", []byte("a"), 10, nil)
	if _, code := e.doJSON("POST", "/api/v1/files/move", e.token, map[string]string{"from": "Docs/a.txt", "to": "Docs/Nuova/b.txt"}); code != 200 {
		t.Fatalf("move: %d", code)
	}
	if _, err := os.Stat(filepath.Join(e.root, "Docs", "Nuova", "b.txt")); err != nil {
		t.Fatal(err)
	}
	// non si sovrascrive un elemento esistente
	e.upload("c.txt", "Docs", []byte("c"), 10, nil)
	if _, code := e.doJSON("POST", "/api/v1/files/move", e.token, map[string]string{"from": "Docs/c.txt", "to": "Docs/Nuova/b.txt"}); code != 409 {
		t.Fatalf("move su esistente: %d", code)
	}
	// cartella non vuota: serve recursive
	if resp, _ := e.req("DELETE", "/api/v1/files?path=Docs", e.token, nil, nil); resp.StatusCode != 409 {
		t.Fatalf("delete non vuota: %d", resp.StatusCode)
	}
	if resp, _ := e.req("DELETE", "/api/v1/files?path=Docs&recursive=true", e.token, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete ricorsivo: %d", resp.StatusCode)
	}
	// la radice non si cancella
	if resp, _ := e.req("DELETE", "/api/v1/files?path=", e.token, nil, nil); resp.StatusCode == 204 {
		t.Fatal("la radice non deve essere cancellabile")
	}
	if _, err := os.Stat(e.root); err != nil {
		t.Fatal("radice sparita")
	}
}

func TestRevokeDevicePersists(t *testing.T) {
	cfg := t.TempDir()
	root := filepath.Join(t.TempDir(), "cloud")
	e := newEnvAt(t, cfg, root, Hooks{}, true)
	devs := e.srv.Devices()
	if len(devs) != 1 {
		t.Fatalf("dispositivi: %v", devs)
	}
	if !e.srv.RevokeDevice(devs[0].ID) {
		t.Fatal("revoke fallita")
	}
	if _, code := e.doJSON("GET", "/api/v1/info", e.token, nil); code != 401 {
		t.Fatalf("dopo la revoca atteso 401, ottenuto %d", code)
	}
	// dopo un riavvio il token revocato resta invalido
	e2 := newEnvAt(t, cfg, root, Hooks{}, false)
	e2.token = e.token
	if _, code := e2.doJSON("GET", "/api/v1/info", e2.token, nil); code != 401 {
		t.Fatalf("dopo riavvio atteso 401, ottenuto %d", code)
	}
	// il file dei dispositivi non contiene il token in chiaro
	raw, _ := os.ReadFile(filepath.Join(cfg, "devices.json"))
	if strings.Contains(string(raw), e.token) {
		t.Fatal("il token è salvato in chiaro")
	}
}

func TestUnpairSelf(t *testing.T) {
	e := newEnv(t, Hooks{})
	if resp, _ := e.req("DELETE", "/api/v1/me", e.token, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("unpair: %d", resp.StatusCode)
	}
	if _, code := e.doJSON("GET", "/api/v1/info", e.token, nil); code != 401 {
		t.Fatalf("dopo unpair atteso 401: %d", code)
	}
}

func TestStaleUploadCleanup(t *testing.T) {
	e := newEnv(t, Hooks{})
	st, _ := e.doJSON("POST", "/api/v1/uploads", e.token, map[string]any{"name": "s.bin", "size": 10})
	id := st["id"].(string)
	old := time.Now().Add(-10 * 24 * time.Hour)
	_ = os.Chtimes(filepath.Join(e.root, internalDirName, "uploads", id+".part"), old, old)
	if n := e.srv.uploads.cleanStale(7 * 24 * time.Hour); n != 1 {
		t.Fatalf("cleanStale ha rimosso %d", n)
	}
	if _, code := e.doJSON("GET", "/api/v1/uploads/"+id, e.token, nil); code != 404 {
		t.Fatalf("upload scaduto ancora presente: %d", code)
	}
}

// --- TLS e pinning ---------------------------------------------------------------

func pinnedClient(fingerprint string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // la verifica vera è VerifyPeerCertificate
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 || Fingerprint(raw[0]) != fingerprint {
					return fmt.Errorf("impronta del certificato diversa da quella attesa")
				}
				return nil
			},
		}},
	}
}

func TestTLSWithPinning(t *testing.T) {
	cfg := t.TempDir()
	srv, err := New(Options{ConfigDir: cfg, RootDir: filepath.Join(t.TempDir(), "c"), ServerName: "TLS", Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	base := "https://" + ln.Addr().String()

	// con l'impronta giusta funziona
	resp, err := pinnedClient(srv.Fingerprint()).Get(base + "/api/v1/health")
	if err != nil {
		t.Fatalf("impronta corretta: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("risposta/TLS inattesi: %d", resp.StatusCode)
	}
	// con un'impronta diversa (attaccante o server sbagliato) la connessione è rifiutata
	if _, err := pinnedClient(strings.Repeat("0", 64)).Get(base + "/api/v1/health"); err == nil {
		t.Fatal("con impronta errata la connessione doveva fallire")
	}
	// senza pinning né CA fidata, il certificato autofirmato è rifiutato
	if _, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/api/v1/health"); err == nil {
		t.Fatal("un client normale non deve fidarsi del certificato autofirmato")
	}
	// il certificato è stabile tra i riavvii (altrimenti il pairing si romperebbe)
	srv2, err := New(Options{ConfigDir: cfg, RootDir: filepath.Join(t.TempDir(), "c2")})
	if err != nil {
		t.Fatal(err)
	}
	if srv2.Fingerprint() != srv.Fingerprint() {
		t.Fatal("il certificato è cambiato dopo il riavvio")
	}
}

func TestSanitizeAndCleanRel(t *testing.T) {
	cases := map[string]string{
		"../../x.txt": "x.txt", `C:\dir\f.txt`: "f.txt", "a<b>.txt": "a_b_.txt", "": "file", "...": "file",
		"nul": "_nul", "COM1.log": "_COM1.log", "  spazi  ": "spazi", "fine.": "fine", ".nascosto": ".nascosto",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, atteso %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 300) + ".jpg"
	if got := sanitizeName(long); len(got) > 200 || !strings.HasSuffix(got, ".jpg") {
		t.Errorf("nome lungo non troncato bene: len=%d", len(got))
	}
	for _, p := range []string{"..", "a/../b", "x/..", `a\..\b`, "a\x00b", "a:b", "dir./x", ".fireshare/x"} {
		if _, err := cleanRel(p); err == nil {
			t.Errorf("cleanRel(%q) doveva fallire", p)
		}
	}
	if got, err := cleanRel(`/a//b/./c\d/`); err != nil || got != "a/b/c/d" {
		t.Errorf("cleanRel normalizzazione: %q %v", got, err)
	}
}
