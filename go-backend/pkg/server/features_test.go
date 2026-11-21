package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- galleria: indice media e miniature -------------------------------------------------

func solidImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// metà sinistra rossa, metà destra blu: si vede se l'immagine viene ruotata
			if x < w/2 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	return img
}

// jpegWithOrientation produce un JPEG con il tag EXIF Orientation indicato.
func jpegWithOrientation(t *testing.T, img image.Image, orientation int, littleEndian bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()

	var bo interface {
		binary.ByteOrder
		binary.AppendByteOrder
	} = binary.BigEndian
	hdr := "MM"
	if littleEndian {
		bo, hdr = binary.LittleEndian, "II"
	}
	tiff := []byte(hdr)
	tiff = bo.AppendUint16(tiff, 42)
	tiff = bo.AppendUint32(tiff, 8)
	tiff = bo.AppendUint16(tiff, 1)      // una voce nella IFD0
	tiff = bo.AppendUint16(tiff, 0x0112) // Orientation
	tiff = bo.AppendUint16(tiff, 3)      // tipo SHORT
	tiff = bo.AppendUint32(tiff, 1)      // conteggio
	tiff = bo.AppendUint16(tiff, uint16(orientation))
	tiff = bo.AppendUint16(tiff, 0) // padding del campo valore
	tiff = bo.AppendUint32(tiff, 0) // nessuna IFD successiva

	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	app1 = append(app1, seg...)

	out := append([]byte{}, raw[:2]...)
	out = append(out, app1...)
	out = append(out, raw[2:]...)
	return out
}

func TestJPEGOrientationParsing(t *testing.T) {
	img := solidImage(40, 20)
	for _, le := range []bool{false, true} {
		for o := 1; o <= 8; o++ {
			if got := jpegOrientation(jpegWithOrientation(t, img, o, le)); got != o {
				t.Errorf("orientamento %d (littleEndian=%v): letto %d", o, le, got)
			}
		}
	}
	var plain bytes.Buffer
	_ = jpeg.Encode(&plain, img, nil)
	if got := jpegOrientation(plain.Bytes()); got != 1 {
		t.Errorf("JPEG senza EXIF: atteso 1, letto %d", got)
	}
	if got := jpegOrientation([]byte("non è un jpeg")); got != 1 {
		t.Errorf("dati casuali: atteso 1, letto %d", got)
	}
	if got := jpegOrientation(nil); got != 1 {
		t.Errorf("vuoto: atteso 1, letto %d", got)
	}
}

func TestMediaListingAndThumbs(t *testing.T) {
	e := newEnv(t, Hooks{})

	write := func(rel string, data []byte, mod time.Time) {
		p := filepath.Join(e.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, mod, mod)
	}
	now := time.Now()
	// 800x400 con orientamento 6 (ruotata di 90°): la miniatura deve risultare più alta che larga
	write("Foto/vecchia.jpg", jpegWithOrientation(t, solidImage(800, 400), 6, false), now.Add(-48*time.Hour))
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, solidImage(100, 100))
	write("Foto/nuova.png", pngBuf.Bytes(), now.Add(-time.Hour))
	write("Video/clip.mp4", []byte("non è un vero video"), now)
	write("note.txt", []byte("ciao"), now)
	write(".fireshare/thumbs/finto.jpg", []byte("x"), now) // non deve comparire

	// senza token: 401
	if _, st := e.doJSON("GET", "/api/v1/media", "", nil); st != 401 {
		t.Fatalf("media senza token: %d", st)
	}
	if resp, _ := e.req("GET", "/api/v1/files/thumb?path=Foto/nuova.png", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("thumb senza token: %d", resp.StatusCode)
	}

	list, st := e.doJSON("GET", "/api/v1/media", e.token, nil)
	if st != 200 {
		t.Fatalf("media: %d", st)
	}
	if list["total"].(float64) != 3 {
		t.Fatalf("attesi 3 elementi media (jpg, png, mp4), trovati %v: %v", list["total"], list["items"])
	}
	items := list["items"].([]any)
	order := []string{}
	for _, it := range items {
		order = append(order, it.(map[string]any)["path"].(string))
	}
	if strings.Join(order, ",") != "Video/clip.mp4,Foto/nuova.png,Foto/vecchia.jpg" {
		t.Fatalf("ordine errato (dal più recente): %v", order)
	}
	if k := items[0].(map[string]any)["kind"]; k != "video" {
		t.Errorf("kind del video: %v", k)
	}

	// filtri e paginazione
	list, _ = e.doJSON("GET", "/api/v1/media?kind=image", e.token, nil)
	if list["total"].(float64) != 2 {
		t.Errorf("kind=image: %v", list["total"])
	}
	list, _ = e.doJSON("GET", "/api/v1/media?dir=Foto&limit=1&offset=1", e.token, nil)
	if list["total"].(float64) != 2 || len(list["items"].([]any)) != 1 ||
		list["items"].([]any)[0].(map[string]any)["path"] != "Foto/vecchia.jpg" {
		t.Errorf("dir+paginazione: %v", list)
	}
	if _, st := e.doJSON("GET", "/api/v1/media?dir=../fuori", e.token, nil); st != 400 {
		t.Errorf("dir con traversal: %d", st)
	}

	// una modifica sul disco compare dopo l'invalidazione (qui via eliminazione dall'API)
	if _, st := e.doJSON("DELETE", "/api/v1/files?path=Foto/nuova.png", e.token, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	list, _ = e.doJSON("GET", "/api/v1/media", e.token, nil)
	if list["total"].(float64) != 2 {
		t.Errorf("dopo l'eliminazione: %v", list["total"])
	}

	// miniatura con orientamento corretto
	resp, body := e.req("GET", "/api/v1/files/thumb?path=Foto/vecchia.jpg&size=320", e.token, nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 160 || b.Dy() != 320 {
		t.Fatalf("miniatura %dx%d: attesa 160x320 (800x400 ruotata di 90°)", b.Dx(), b.Dy())
	}
	// dopo la rotazione oraria di 90° la metà sinistra rossa finisce in alto
	top := color.RGBAModel.Convert(img.At(80, 20)).(color.RGBA)
	bottom := color.RGBAModel.Convert(img.At(80, 300)).(color.RGBA)
	if top.R < 180 || top.B > 90 || bottom.B < 180 || bottom.R > 90 {
		t.Errorf("rotazione sbagliata: alto=%v basso=%v", top, bottom)
	}

	// la miniatura è in cache e una seconda richiesta restituisce gli stessi byte
	cached, _ := os.ReadDir(filepath.Join(e.root, internalDirName, "thumbs"))
	foundCache := false
	for _, c := range cached {
		if c.Name() != "finto.jpg" {
			foundCache = true
		}
	}
	if !foundCache {
		t.Error("miniatura non salvata in cache")
	}
	_, body2 := e.req("GET", "/api/v1/files/thumb?path=Foto/vecchia.jpg&size=320", e.token, nil, nil)
	if !bytes.Equal(body, body2) {
		t.Error("la miniatura in cache differisce da quella generata")
	}

	// una dimensione non ammessa torna a 320; file non-media e video non decodificabili: 404 no_thumb
	if resp, _ := e.req("GET", "/api/v1/files/thumb?path=Foto/vecchia.jpg&size=99999", e.token, nil, nil); resp.StatusCode != 200 {
		t.Errorf("size anomalo: %d", resp.StatusCode)
	}
	for _, p := range []string{"note.txt", "Video/clip.mp4", "Foto/non-esiste.jpg"} {
		resp, _ := e.req("GET", "/api/v1/files/thumb?path="+url.QueryEscape(p), e.token, nil, nil)
		if resp.StatusCode != 404 {
			t.Errorf("thumb di %s: atteso 404, ottenuto %d", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"../x.jpg", ".fireshare/thumbs/finto.jpg", ""} {
		resp, _ := e.req("GET", "/api/v1/files/thumb?path="+url.QueryEscape(p), e.token, nil, nil)
		if resp.StatusCode != 400 {
			t.Errorf("thumb con percorso %q: atteso 400, ottenuto %d", p, resp.StatusCode)
		}
	}
}

func TestScaleDownKeepsAspectAndSmallImages(t *testing.T) {
	big := scaleDown(solidImage(1000, 500), 320)
	if b := big.Bounds(); b.Dx() != 320 || b.Dy() != 160 {
		t.Errorf("1000x500 -> %dx%d", b.Dx(), b.Dy())
	}
	tall := scaleDown(solidImage(300, 1200), 160)
	if b := tall.Bounds(); b.Dx() != 40 || b.Dy() != 160 {
		t.Errorf("300x1200 -> %dx%d", b.Dx(), b.Dy())
	}
	small := scaleDown(solidImage(50, 30), 320)
	if b := small.Bounds(); b.Dx() != 50 || b.Dy() != 30 {
		t.Errorf("immagine piccola non deve essere ingrandita: %dx%d", b.Dx(), b.Dy())
	}
}

// --- link di condivisione --------------------------------------------------------------------

func tokenFromURL(t *testing.T, u string) string {
	t.Helper()
	i := strings.LastIndex(u, "/s/")
	if i < 0 {
		t.Fatalf("URL di condivisione inatteso: %s", u)
	}
	return u[i+3:]
}

func TestShareLinks(t *testing.T) {
	e := newEnv(t, Hooks{})
	content := payload(100_000)
	if err := os.WriteFile(filepath.Join(e.root, "documento.pdf"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(e.root, "Cartella"), 0o755)

	// serve il token del telefono; cartelle e file inesistenti vengono rifiutati
	if _, st := e.doJSON("POST", "/api/v1/shares", "", map[string]any{"path": "documento.pdf"}); st != 401 {
		t.Fatalf("share senza token: %d", st)
	}
	if res, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "Cartella"}); st != 400 {
		t.Fatalf("share di una cartella: %d %v", st, res)
	}
	if _, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "manca.pdf"}); st != 404 {
		t.Fatalf("share di file inesistente: %d", st)
	}
	if _, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "../x"}); st != 400 {
		t.Fatalf("share con traversal: %d", st)
	}
	if _, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "documento.pdf", "expiresInSeconds": 5}); st != 400 {
		t.Fatalf("durata troppo breve accettata: %d", st)
	}
	if _, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "documento.pdf", "expiresInSeconds": 40 * 24 * 3600}); st != 400 {
		t.Fatalf("durata oltre 30 giorni accettata: %d", st)
	}

	// link semplice
	res, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "documento.pdf", "expiresInSeconds": 3600})
	if st != 201 {
		t.Fatalf("create share: %d %v", st, res)
	}
	tok := tokenFromURL(t, res["url"].(string))
	if len(tok) < 20 {
		t.Fatalf("token troppo corto: %q", tok)
	}
	if res["trustedUrl"] != false || res["hasPassword"] != false {
		t.Errorf("flag inattesi: %v", res)
	}

	// la pagina pubblica non richiede il token del telefono, mostra il nome e NON conta come download
	resp, body := e.req("GET", "/s/"+tok, "", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "documento.pdf") {
		t.Fatalf("pagina: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("intestazioni di sicurezza mancanti: %v", resp.Header)
	}
	if list := e.srv.Shares(); len(list) != 1 || list[0].Downloads != 0 {
		t.Fatalf("la pagina non deve contare come download: %+v", list)
	}

	// download completo, poi ripresa con Range (non conta di nuovo)
	resp, body = e.req("GET", "/s/"+tok+"/download", "", nil, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, content) {
		t.Fatalf("download: %d (%d byte)", resp.StatusCode, len(body))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "documento.pdf") {
		t.Errorf("Content-Disposition: %q", cd)
	}
	resp, body = e.req("GET", "/s/"+tok+"/download", "", nil, map[string]string{"Range": "bytes=500-999"})
	if resp.StatusCode != 206 || !bytes.Equal(body, content[500:1000]) {
		t.Fatalf("range: %d", resp.StatusCode)
	}
	if list := e.srv.Shares(); list[0].Downloads != 1 {
		t.Errorf("download contati: %d (atteso 1)", list[0].Downloads)
	}

	// token sbagliato: pagina neutra 404
	for _, bad := range []string{"xxxxxxxxxxxxxxxxxxxxxxxx", tok + "x", ""} {
		resp, body = e.req("GET", "/s/"+bad+"/download", "", nil, nil)
		if resp.StatusCode != 404 {
			t.Errorf("token %q: %d", bad, resp.StatusCode)
		}
		_ = body
	}

	// l'elenco e la revoca richiedono il token del telefono
	if _, st := e.doJSON("GET", "/api/v1/shares", "", nil); st != 401 {
		t.Errorf("elenco senza token: %d", st)
	}
	list, st := e.doJSON("GET", "/api/v1/shares", e.token, nil)
	if st != 200 || len(list["shares"].([]any)) != 1 {
		t.Fatalf("elenco: %d %v", st, list)
	}
	id := list["shares"].([]any)[0].(map[string]any)["id"].(string)
	if _, st := e.doJSON("DELETE", "/api/v1/shares/"+id, "", nil); st != 401 {
		t.Errorf("revoca senza token: %d", st)
	}
	if _, st := e.doJSON("DELETE", "/api/v1/shares/"+id, e.token, nil); st != 204 {
		t.Fatalf("revoca: %d", st)
	}
	if resp, _ := e.req("GET", "/s/"+tok+"/download", "", nil, nil); resp.StatusCode != 404 {
		t.Errorf("link revocato ancora attivo: %d", resp.StatusCode)
	}
	if _, st := e.doJSON("DELETE", "/api/v1/shares/"+id, e.token, nil); st != 404 {
		t.Errorf("seconda revoca: %d", st)
	}
}

func TestShareLimitsExpiryAndFileRemoval(t *testing.T) {
	e := newEnv(t, Hooks{})
	_ = os.WriteFile(filepath.Join(e.root, "a.txt"), []byte("contenuto"), 0o644)

	// limite di download
	res, _ := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "a.txt", "maxDownloads": 2})
	tok := tokenFromURL(t, res["url"].(string))
	for i := 0; i < 2; i++ {
		if resp, _ := e.req("GET", "/s/"+tok+"/download", "", nil, nil); resp.StatusCode != 200 {
			t.Fatalf("download %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := e.req("GET", "/s/"+tok+"/download", "", nil, nil); resp.StatusCode != 404 {
		t.Errorf("oltre il limite: %d", resp.StatusCode)
	}

	// scadenza: si porta indietro la data nel negozio
	res, _ = e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "a.txt", "expiresInSeconds": 3600})
	tok = tokenFromURL(t, res["url"].(string))
	e.srv.shares.mu.Lock()
	for _, sh := range e.srv.shares.items {
		if sh.Token == tok {
			sh.ExpiresAt = time.Now().Add(-time.Minute)
		}
	}
	e.srv.shares.mu.Unlock()
	if resp, _ := e.req("GET", "/s/"+tok, "", nil, nil); resp.StatusCode != 404 {
		t.Errorf("link scaduto (pagina): %d", resp.StatusCode)
	}
	if resp, _ := e.req("GET", "/s/"+tok+"/download", "", nil, nil); resp.StatusCode != 404 {
		t.Errorf("link scaduto (download): %d", resp.StatusCode)
	}

	// file eliminato dopo la creazione del link
	res, _ = e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "a.txt"})
	tok = tokenFromURL(t, res["url"].(string))
	_ = os.Remove(filepath.Join(e.root, "a.txt"))
	if resp, _ := e.req("GET", "/s/"+tok+"/download", "", nil, nil); resp.StatusCode != 404 {
		t.Errorf("file rimosso: %d", resp.StatusCode)
	}
}

func TestSharePasswordAndLockout(t *testing.T) {
	e := newEnv(t, Hooks{})
	_ = os.WriteFile(filepath.Join(e.root, "segreto.txt"), []byte("dati riservati"), 0o644)
	res, st := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "segreto.txt", "password": "gatto-123"})
	if st != 201 || res["hasPassword"] != true {
		t.Fatalf("create: %d %v", st, res)
	}
	if _, leaked := res["passHash"]; leaked {
		t.Fatal("l'hash della password non deve uscire dall'API")
	}
	tok := tokenFromURL(t, res["url"].(string))

	// il GET diretto del download rimanda alla pagina con la password
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Get(e.ts.URL + "/s/" + tok + "/download")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET senza password: %d", r.StatusCode)
	}

	post := func(pw string) (*http.Response, string) {
		resp, body := e.req("POST", "/s/"+tok+"/download", "", strings.NewReader(url.Values{"p": {pw}}.Encode()),
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		return resp, string(body)
	}
	if resp, body := post("sbagliata"); resp.StatusCode != 403 || strings.Contains(body, "dati riservati") {
		t.Fatalf("password errata: %d", resp.StatusCode)
	}
	if resp, body := post("gatto-123"); resp.StatusCode != 200 || body != "dati riservati" {
		t.Fatalf("password giusta: %d %q", resp.StatusCode, body)
	}

	// dopo troppi errori il link si blocca anche per la password corretta
	var last int
	for i := 0; i < maxShareFails+1; i++ {
		resp, _ := post(fmt.Sprintf("tentativo-%d", i))
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("blocco dopo %d errori: %d", maxShareFails, last)
	}
	if resp, _ := post("gatto-123"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("password giusta durante il blocco: %d", resp.StatusCode)
	}
}

func TestSharesSurviveRestart(t *testing.T) {
	cfg := t.TempDir()
	root := filepath.Join(t.TempDir(), "cloud")
	e := newEnvAt(t, cfg, root, Hooks{}, true)
	_ = os.WriteFile(filepath.Join(e.root, "f.txt"), []byte("x"), 0o644)
	res, _ := e.doJSON("POST", "/api/v1/shares", e.token, map[string]any{"path": "f.txt", "password": "pw"})
	tok := tokenFromURL(t, res["url"].(string))

	e2 := newEnvAt(t, cfg, root, Hooks{}, false)
	resp, body := e2.req("GET", "/s/"+tok, "", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "f.txt") {
		t.Fatalf("dopo il riavvio: %d", resp.StatusCode)
	}
	shares := e2.srv.Shares()
	if len(shares) != 1 || !shares[0].HasPassword {
		t.Fatalf("link persi o password persa: %+v", shares)
	}
	raw, _ := os.ReadFile(filepath.Join(cfg, "shares.json"))
	if strings.Contains(string(raw), `"pw"`) {
		t.Error("la password non deve essere salvata in chiaro")
	}
}

func TestShareBaseURLPreference(t *testing.T) {
	e := newEnv(t, Hooks{})
	if _, trusted := e.srv.shareBase(); trusted {
		t.Error("senza ShareBaseURL il link usa il certificato autofirmato")
	}
	e.srv.opts.PublicURLs = []string{"https://casa.example.org:8443/"}
	if base, _ := e.srv.shareBase(); base != "https://casa.example.org:8443" {
		t.Errorf("preferenza agli indirizzi pubblici: %q", base)
	}
	e.srv.opts.ShareBaseURL = "https://link.example.org/"
	if base, trusted := e.srv.shareBase(); base != "https://link.example.org" || !trusted {
		t.Errorf("ShareBaseURL: %q trusted=%v", base, trusted)
	}
}

// --- /info e indirizzi ---------------------------------------------------------------------

func TestInfoReturnsUpdatedURLs(t *testing.T) {
	e := newEnv(t, Hooks{})
	e.srv.remote.mu.Lock()
	e.srv.remote.status.PublicURLs = []string{"https://miopc.duckdns.org:8443"}
	e.srv.remote.mu.Unlock()
	info, st := e.doJSON("GET", "/api/v1/info", e.token, nil)
	if st != 200 {
		t.Fatal(st)
	}
	found := false
	for _, u := range info["urls"].([]any) {
		if u == "https://miopc.duckdns.org:8443" {
			found = true
		}
	}
	if !found {
		t.Errorf("urls in /info: %v", info["urls"])
	}
}

// --- accesso da fuori casa -----------------------------------------------------------------

// fakeRouter simula la descrizione UPnP e il servizio di port mapping di un router.
type fakeRouter struct {
	mu          sync.Mutex
	calls       []string
	args        map[string]string
	permanentOK bool // accetta solo mappature con lease 0 (errore 725 altrimenti)
	externalIP  string
	mapped      map[string]bool
}

func (f *fakeRouter) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/desc.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<deviceType>urn:schemas-upnp-org:device:InternetGatewayDevice:1</deviceType>
<deviceList><device><deviceList><device><serviceList>
<service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/ctl</controlURL></service>
</serviceList></device></deviceList></device></deviceList></device></root>`)
	})
	mux.HandleFunc("/ctl", func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(strings.SplitN(r.Header.Get("SOAPAction"), "#", 2)[1], `"`)
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		body := buf.String()
		get := func(tag string) string {
			i := strings.Index(body, "<"+tag+">")
			j := strings.Index(body, "</"+tag+">")
			if i < 0 || j < 0 {
				return ""
			}
			return body[i+len(tag)+2 : j]
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, action)
		switch action {
		case "GetExternalIPAddress":
			fmt.Fprintf(w, `<s:Envelope xmlns:s="x"><s:Body><u:R xmlns:u="y"><NewExternalIPAddress>%s</NewExternalIPAddress></u:R></s:Body></s:Envelope>`, f.externalIP)
		case "AddPortMapping":
			if f.permanentOK && get("NewLeaseDuration") != "0" {
				w.WriteHeader(500)
				fmt.Fprint(w, `<s:Envelope><s:Body><s:Fault><detail><UPnPError><errorCode>725</errorCode><errorDescription>OnlyPermanentLeasesSupported</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
				return
			}
			f.args = map[string]string{
				"port": get("NewExternalPort"), "proto": get("NewProtocol"), "internal": get("NewInternalPort"),
				"client": get("NewInternalClient"), "lease": get("NewLeaseDuration"),
			}
			f.mapped[get("NewExternalPort")] = true
			fmt.Fprint(w, `<s:Envelope><s:Body><u:AddPortMappingResponse xmlns:u="y"/></s:Body></s:Envelope>`)
		case "DeletePortMapping":
			delete(f.mapped, get("NewExternalPort"))
			fmt.Fprint(w, `<s:Envelope><s:Body><u:DeletePortMappingResponse xmlns:u="y"/></s:Body></s:Envelope>`)
		default:
			w.WriteHeader(500)
		}
	})
	return mux
}

func newFakeRouter(t *testing.T, f *fakeRouter) *httptest.Server {
	f.mapped = map[string]bool{}
	ts := httptest.NewServer(f.handler(t))
	t.Cleanup(ts.Close)
	return ts
}

func TestIGDClientPortMapping(t *testing.T) {
	f := &fakeRouter{externalIP: "93.184.216.34", permanentOK: true}
	ts := newFakeRouter(t, f)
	ctx := context.Background()

	c, err := newIGDClient(ctx, ts.URL+"/desc.xml")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := c.GetExternalIP(ctx)
	if err != nil || ip != "93.184.216.34" {
		t.Fatalf("IP esterno: %q %v", ip, err)
	}
	// il router accetta solo mappature permanenti: il client deve riprovare con lease 0
	if err := c.AddPortMapping(ctx, 8443); err != nil {
		t.Fatal(err)
	}
	if f.args["port"] != "8443" || f.args["internal"] != "8443" || f.args["proto"] != "TCP" || f.args["lease"] != "0" || f.args["client"] == "" {
		t.Fatalf("argomenti di AddPortMapping: %v", f.args)
	}
	if err := c.DeletePortMapping(ctx, 8443); err != nil {
		t.Fatal(err)
	}
	if f.mapped["8443"] {
		t.Error("la porta doveva essere chiusa")
	}
}

func TestIGDRejectsNonLocalRouterAndGarbage(t *testing.T) {
	// descrizione che punta a un indirizzo pubblico: va rifiutata (SSRF)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<root><URLBase>http://8.8.8.8:80/</URLBase><device><serviceList><service>
<serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/ctl</controlURL></service></serviceList></device></root>`)
	}))
	defer ts.Close()
	if _, err := newIGDClient(context.Background(), ts.URL); err == nil {
		t.Error("un router con controlURL pubblico non deve essere accettato")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "non xml") }))
	defer bad.Close()
	if _, err := newIGDClient(context.Background(), bad.URL); err == nil {
		t.Error("XML non valido accettato")
	}
}

func fakeDuckDNS(t *testing.T, wantDomain, wantToken string, reply string) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/update" || q.Get("domains") != wantDomain || q.Get("token") != wantToken {
			fmt.Fprint(w, "KO")
			return
		}
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newTestRemote(t *testing.T, cfg remoteSettings, igd *igdClient, discoverErr error, extIP string, duck *httptest.Server) *remoteManager {
	m := newRemoteManager(cfg, func() int { return 8443 }, nil)
	m.discover = func(context.Context) (*igdClient, error) { return igd, discoverErr }
	m.publicIP = func(context.Context) (string, error) {
		if extIP == "" {
			return "", fmt.Errorf("offline")
		}
		return extIP, nil
	}
	if duck != nil {
		m.duckBase = duck.URL
	}
	return m
}

func TestRemoteFullSetup(t *testing.T) {
	f := &fakeRouter{externalIP: "93.184.216.34"}
	ts := newFakeRouter(t, f)
	igd, err := newIGDClient(context.Background(), ts.URL+"/desc.xml")
	if err != nil {
		t.Fatal(err)
	}
	duck := fakeDuckDNS(t, "miopc", "tok-segreto", "OK")
	m := newTestRemote(t, remoteSettings{AutoPortMap: true, DuckDNSDomain: "miopc", DuckDNSToken: "tok-segreto"}, igd, nil, "93.184.216.34", duck)

	st := m.refresh(context.Background(), false)
	if st.PortMap.State != "mapped" || st.DuckDNS.State != "ok" || st.CGNAT {
		t.Fatalf("stato: %+v", st)
	}
	if st.ExternalIP != "93.184.216.34" {
		t.Errorf("IP esterno: %q", st.ExternalIP)
	}
	want := map[string]bool{"https://miopc.duckdns.org:8443": true, "https://93.184.216.34:8443": true}
	for _, u := range st.PublicURLs {
		delete(want, u)
	}
	if len(want) != 0 {
		t.Errorf("indirizzi pubblici mancanti %v (trovati %v)", want, st.PublicURLs)
	}
	if got := m.publicURLs(); len(got) != 2 {
		t.Errorf("publicURLs(): %v", got)
	}
	if !f.mapped["8443"] {
		t.Error("la porta doveva essere aperta sul router")
	}
	// lo stop richiude la porta
	m.mu.Lock()
	m.cancel, m.done = func() {}, make(chan struct{})
	close(m.done)
	m.mu.Unlock()
	m.stop()
	if f.mapped["8443"] {
		t.Error("lo stop doveva chiudere la porta")
	}
}

func TestRemoteCGNATAndFailures(t *testing.T) {
	// router con IP condiviso (CGNAT): nessun indirizzo pubblico annunciato e consiglio su Tailscale
	f := &fakeRouter{externalIP: "100.72.5.9"}
	ts := newFakeRouter(t, f)
	igd, _ := newIGDClient(context.Background(), ts.URL+"/desc.xml")
	duck := fakeDuckDNS(t, "miopc", "tok", "OK")
	m := newTestRemote(t, remoteSettings{AutoPortMap: true, DuckDNSDomain: "miopc", DuckDNSToken: "tok"}, igd, nil, "93.184.216.34", duck)
	st := m.refresh(context.Background(), false)
	if !st.CGNAT || len(st.PublicURLs) != 0 {
		t.Fatalf("CGNAT non riconosciuto: %+v", st)
	}
	if !strings.Contains(strings.Join(st.Hints, " "), "Tailscale") {
		t.Errorf("manca il consiglio su Tailscale: %v", st.Hints)
	}

	// IP del router diverso da quello visto da Internet: doppio NAT
	f2 := &fakeRouter{externalIP: "93.184.216.34"}
	ts2 := newFakeRouter(t, f2)
	igd2, _ := newIGDClient(context.Background(), ts2.URL+"/desc.xml")
	m = newTestRemote(t, remoteSettings{AutoPortMap: true}, igd2, nil, "151.101.1.1", nil)
	if st := m.refresh(context.Background(), false); !st.CGNAT {
		t.Errorf("doppio NAT non riconosciuto: %+v", st)
	}

	// nessun router UPnP: stato "failed" con suggerimento, nessun crash
	m = newTestRemote(t, remoteSettings{AutoPortMap: true}, nil, fmt.Errorf("nessun router"), "", nil)
	st = m.refresh(context.Background(), false)
	if st.PortMap.State != "failed" || len(st.PublicURLs) != 0 || len(st.Hints) == 0 {
		t.Errorf("senza UPnP: %+v", st)
	}

	// DuckDNS risponde KO (token errato): errore segnalato, nessun URL DuckDNS
	duck = fakeDuckDNS(t, "miopc", "giusto", "OK")
	m = newTestRemote(t, remoteSettings{DuckDNSDomain: "miopc", DuckDNSToken: "sbagliato"}, nil, nil, "93.184.216.34", duck)
	st = m.refresh(context.Background(), false)
	if st.DuckDNS.State != "failed" || len(st.PublicURLs) != 0 {
		t.Errorf("DuckDNS KO: %+v", st)
	}

	// tutto spento: nessun accesso a Internet a meno che l'utente non prema «Verifica»
	called := false
	m = newTestRemote(t, remoteSettings{}, nil, nil, "93.184.216.34", nil)
	m.publicIP = func(context.Context) (string, error) { called = true; return "1.2.3.4", nil }
	if st := m.refresh(context.Background(), false); called || st.ExternalIP != "" {
		t.Errorf("senza funzioni attive non si deve contattare nessun servizio esterno")
	}
	if st := m.refresh(context.Background(), true); !called || st.ExternalIP != "1.2.3.4" {
		t.Errorf("con «Verifica» l'IP pubblico va cercato: %+v", st)
	}
}

func TestIsNonPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"192.168.1.5": true, "10.0.0.1": true, "172.16.3.4": true, "100.64.0.1": true, "100.127.255.1": true,
		"127.0.0.1": true, "169.254.1.1": true, "": true, "non-un-ip": true,
		"93.184.216.34": false, "8.8.8.8": false, "100.128.0.1": false,
	} {
		if got := isNonPublicIP(ip); got != want {
			t.Errorf("isNonPublicIP(%q) = %v, atteso %v", ip, got, want)
		}
	}
}

func TestDuckDNSTokenNotLeakedInStatus(t *testing.T) {
	duck := fakeDuckDNS(t, "miopc", "tok-segreto", "OK")
	m := newTestRemote(t, remoteSettings{DuckDNSDomain: "miopc", DuckDNSToken: "tok-segreto"}, nil, nil, "93.184.216.34", duck)
	m.refresh(context.Background(), false)
	st := m.Status()
	if strings.Contains(fmt.Sprintf("%+v", st), "tok-segreto") {
		t.Errorf("il token DuckDNS compare nello stato: %+v", st)
	}
}
