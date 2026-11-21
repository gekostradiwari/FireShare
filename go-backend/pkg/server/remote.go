package server

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Accesso da fuori casa. Il server prova, in modo automatico e senza mai bloccarsi:
//   1. ad aprire la porta sul router con UPnP (se il router lo permette);
//   2. ad aggiornare un nome DuckDNS con il proprio IP pubblico (se configurato);
//   3. a riconoscere Tailscale (l'indirizzo 100.x entra già tra gli indirizzi del QR);
//   4. a capire se la rete è dietro CGNAT (in quel caso nessuna porta si può aprire
//      e serve una VPN come Tailscale).
// Il risultato (e i consigli in italiano) è letto dalla GUI desktop con RemoteStatus().

// PortMapStatus descrive l'apertura automatica della porta sul router.
type PortMapStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"` // off | searching | mapped | failed
	Message string `json:"message"`
	Gateway string `json:"gateway,omitempty"`
}

// DuckDNSStatus descrive l'aggiornamento del nome DuckDNS.
type DuckDNSStatus struct {
	Enabled    bool      `json:"enabled"`
	State      string    `json:"state"` // off | ok | failed
	Message    string    `json:"message"`
	Domain     string    `json:"domain,omitempty"`
	LastUpdate time.Time `json:"lastUpdate"`
}

// RemoteStatus è lo stato complessivo dell'accesso da fuori casa.
type RemoteStatus struct {
	PortMap    PortMapStatus `json:"portMap"`
	ExternalIP string        `json:"externalIp"`
	CGNAT      bool          `json:"cgnat"`
	DuckDNS    DuckDNSStatus `json:"duckdns"`
	Tailscale  []string      `json:"tailscale"`
	PublicURLs []string      `json:"publicUrls"` // indirizzi scoperti automaticamente
	Hints      []string      `json:"hints"`
	CheckedAt  time.Time     `json:"checkedAt"`
}

type remoteSettings struct {
	AutoPortMap   bool
	DuckDNSDomain string
	DuckDNSToken  string
}

type remoteManager struct {
	settings remoteSettings
	port     func() int
	logger   interface{ Printf(string, ...any) }

	// punti di iniezione (i test li sostituiscono)
	discover   func(ctx context.Context) (*igdClient, error)
	publicIP   func(ctx context.Context) (string, error)
	duckBase   string
	httpClient *http.Client

	mu     sync.Mutex
	status RemoteStatus
	igd    *igdClient
	mapped int
	cancel context.CancelFunc
	done   chan struct{}
}

func newRemoteManager(s remoteSettings, port func() int, logger interface{ Printf(string, ...any) }) *remoteManager {
	m := &remoteManager{
		settings: s, port: port, logger: logger,
		discover:   discoverIGD,
		duckBase:   "https://www.duckdns.org",
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
	m.publicIP = m.lookupPublicIP
	m.status = RemoteStatus{
		PortMap: PortMapStatus{Enabled: s.AutoPortMap, State: map[bool]string{true: "searching", false: "off"}[s.AutoPortMap]},
		DuckDNS: DuckDNSStatus{Enabled: s.DuckDNSDomain != "" && s.DuckDNSToken != "", State: "off", Domain: s.DuckDNSDomain},
	}
	if !m.status.DuckDNS.Enabled {
		m.status.DuckDNS.Domain = ""
	}
	m.status.Tailscale = tailscaleIPs()
	return m
}

// start avvia il controllo periodico (ogni 10 minuti).
func (m *remoteManager) start() {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.mu.Unlock()
	go func() {
		defer close(m.done)
		m.refresh(ctx, false)
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.refresh(ctx, false)
			}
		}
	}()
}

// stop ferma il controllo e chiude la porta aperta sul router.
func (m *remoteManager) stop() {
	m.mu.Lock()
	cancel, done, igd, port := m.cancel, m.done, m.igd, m.mapped
	m.cancel, m.igd, m.mapped = nil, nil, 0
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	if igd != nil && port != 0 {
		ctx, c := context.WithTimeout(context.Background(), 4*time.Second)
		defer c()
		_ = igd.DeletePortMapping(ctx, port)
	}
}

// Status restituisce una copia dello stato corrente.
func (m *remoteManager) Status() RemoteStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	st.Tailscale = append([]string(nil), st.Tailscale...)
	st.PublicURLs = append([]string(nil), st.PublicURLs...)
	st.Hints = append([]string(nil), st.Hints...)
	return st
}

func (m *remoteManager) publicURLs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.status.PublicURLs...)
}

// refresh esegue tutti i controlli. Con lookup=true consulta anche un servizio
// esterno per conoscere l'IP pubblico anche se UPnP e DuckDNS sono spenti
// (serve al pulsante «Verifica» della GUI).
func (m *remoteManager) refresh(ctx context.Context, lookup bool) RemoteStatus {
	port := m.port()
	st := RemoteStatus{CheckedAt: time.Now().UTC(), Tailscale: tailscaleIPs()}
	st.PortMap = PortMapStatus{Enabled: m.settings.AutoPortMap, State: "off"}
	st.DuckDNS = DuckDNSStatus{Enabled: m.settings.DuckDNSDomain != "" && m.settings.DuckDNSToken != "", State: "off"}

	// 1) UPnP
	var routerIP string
	if m.settings.AutoPortMap && port > 0 {
		st.PortMap.State = "searching"
		igd, err := m.discover(ctx)
		if err != nil {
			st.PortMap.State, st.PortMap.Message = "failed",
				"Nessun router con UPnP trovato (UPnP spento o non supportato)."
		} else {
			st.PortMap.Gateway = igd.host()
			if err := igd.AddPortMapping(ctx, port); err != nil {
				st.PortMap.State = "failed"
				st.PortMap.Message = "Il router ha rifiutato l'apertura della porta: " + err.Error()
			} else {
				st.PortMap.State = "mapped"
				st.PortMap.Message = fmt.Sprintf("Porta %d aperta sul router %s.", port, igd.host())
				m.mu.Lock()
				m.igd, m.mapped = igd, port
				m.mu.Unlock()
			}
			if ip, err := igd.GetExternalIP(ctx); err == nil {
				routerIP = ip
			}
		}
	}

	// 2) IP pubblico visto da Internet
	var seenIP string
	if lookup || st.PortMap.Enabled || st.DuckDNS.Enabled {
		if ip, err := m.publicIP(ctx); err == nil {
			seenIP = ip
		}
	}
	switch {
	case seenIP != "":
		st.ExternalIP = seenIP
	case routerIP != "":
		st.ExternalIP = routerIP
	}
	// CGNAT / doppio NAT: l'IP del router è privato o condiviso, oppure diverso da
	// quello visto da Internet.
	if routerIP != "" && (isNonPublicIP(routerIP) || (seenIP != "" && routerIP != seenIP)) {
		st.CGNAT = true
	}

	// 3) DuckDNS
	if st.DuckDNS.Enabled {
		st.DuckDNS.Domain = m.settings.DuckDNSDomain
		if err := m.updateDuckDNS(ctx); err != nil {
			st.DuckDNS.State, st.DuckDNS.Message = "failed", err.Error()
		} else {
			st.DuckDNS.State = "ok"
			st.DuckDNS.Message = fmt.Sprintf("%s.duckdns.org punta all'IP pubblico di casa.", m.settings.DuckDNSDomain)
			st.DuckDNS.LastUpdate = time.Now().UTC()
		}
	}

	// 4) indirizzi pubblici da annunciare ai telefoni
	if !st.CGNAT && port > 0 {
		if st.DuckDNS.State == "ok" {
			st.PublicURLs = append(st.PublicURLs, fmt.Sprintf("https://%s.duckdns.org:%d", m.settings.DuckDNSDomain, port))
		}
		if st.PortMap.State == "mapped" && st.ExternalIP != "" && !isNonPublicIP(st.ExternalIP) {
			st.PublicURLs = append(st.PublicURLs, fmt.Sprintf("https://%s:%d", st.ExternalIP, port))
		}
	}
	st.Hints = buildHints(st, port, m.settings)

	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
	return st
}

func buildHints(st RemoteStatus, port int, cfg remoteSettings) []string {
	var h []string
	if st.CGNAT {
		h = append(h, "La tua connessione internet sembra dietro CGNAT (IP condiviso dal provider): da fuori casa non si può aprire nessuna porta. "+
			"Usa Tailscale: installalo su PC e telefono, poi accoppia il telefono mentre è connesso a Tailscale.")
	}
	if cfg.AutoPortMap && st.PortMap.State == "failed" && !st.CGNAT {
		h = append(h, fmt.Sprintf("Apri a mano la porta TCP %d sul router verso l'IP di questo PC (inoltro/«port forwarding»), oppure usa Tailscale.", port))
	}
	if !cfg.AutoPortMap && !st.CGNAT && len(st.Tailscale) == 0 {
		h = append(h, "Attiva «Apri la porta sul router automaticamente» oppure installa Tailscale per usare FireShare fuori casa.")
	}
	if st.DuckDNS.Enabled && st.DuckDNS.State == "failed" {
		h = append(h, "DuckDNS non ha accettato l'aggiornamento: controlla il nome e il token.")
	}
	if !st.DuckDNS.Enabled && st.PortMap.State == "mapped" {
		h = append(h, "L'IP pubblico di casa può cambiare: con un nome gratuito su duckdns.org il telefono ritrova sempre il PC.")
	}
	if len(st.Tailscale) > 0 {
		h = append(h, "Tailscale rilevato: gli indirizzi 100.x sono già inclusi nel QR. Anche il telefono deve avere Tailscale attivo.")
	}
	if len(st.PublicURLs) > 0 {
		h = append(h, "I telefoni già accoppiati ricevono i nuovi indirizzi appena si collegano una volta al PC. Per verificare l'accesso, prova da rete mobile (Wi-Fi spento) con «Test connessione» nelle Impostazioni dell'app.")
	}
	return h
}

func tailscaleIPs() []string {
	var out []string
	for _, ip := range localIPv4s() {
		if p := net.ParseIP(ip); p != nil && cgnatNet.Contains(p) {
			out = append(out, ip)
		}
	}
	return out
}

var cgnatNet = func() *net.IPNet { _, n, _ := net.ParseCIDR("100.64.0.0/10"); return n }()

// isNonPublicIP è vero per indirizzi privati, condivisi (CGNAT), locali o non validi.
func isNonPublicIP(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return true
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || cgnatNet.Contains(ip)
}

func (m *remoteManager) lookupPublicIP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return "", err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(b))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("risposta inattesa")
	}
	return ip, nil
}

func (m *remoteManager) updateDuckDNS(ctx context.Context) error {
	q := url.Values{}
	q.Set("domains", m.settings.DuckDNSDomain)
	q.Set("token", m.settings.DuckDNSToken)
	q.Set("ip", "")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.duckBase+"/update?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DuckDNS non raggiungibile")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "OK") {
		return fmt.Errorf("DuckDNS ha risposto KO")
	}
	return nil
}

// --- UPnP (Internet Gateway Device) --------------------------------------------------------

type igdClient struct {
	location    string
	controlURL  string
	serviceType string
	http        *http.Client
}

func (c *igdClient) host() string {
	u, err := url.Parse(c.controlURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

type upnpDevice struct {
	Services []upnpService `xml:"serviceList>service"`
	Children []upnpDevice  `xml:"deviceList>device"`
}

type upnpRoot struct {
	URLBase string     `xml:"URLBase"`
	Device  upnpDevice `xml:"device"`
}

func findWANService(d upnpDevice) (upnpService, bool) {
	for _, s := range d.Services {
		if strings.Contains(s.ServiceType, "WANIPConnection") || strings.Contains(s.ServiceType, "WANPPPConnection") {
			return s, true
		}
	}
	for _, c := range d.Children {
		if s, ok := findWANService(c); ok {
			return s, true
		}
	}
	return upnpService{}, false
}

// newIGDClient legge la descrizione del router e trova il servizio di port mapping.
func newIGDClient(ctx context.Context, location string) (*igdClient, error) {
	hc := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var root upnpRoot
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&root); err != nil {
		return nil, err
	}
	svc, ok := findWANService(root.Device)
	if !ok || svc.ControlURL == "" {
		return nil, fmt.Errorf("il router non espone il port mapping")
	}
	base, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	if root.URLBase != "" {
		if b, err := url.Parse(root.URLBase); err == nil {
			base = b
		}
	}
	ctl, err := base.Parse(svc.ControlURL)
	if err != nil {
		return nil, err
	}
	// un router non deve mai poterci mandare fuori dalla rete locale
	if ip := net.ParseIP(ctl.Hostname()); ip == nil || !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return nil, fmt.Errorf("indirizzo del router non locale")
	}
	return &igdClient{location: location, controlURL: ctl.String(), serviceType: svc.ServiceType, http: hc}, nil
}

// discoverIGD cerca il router con SSDP (multicast UDP) e ne legge la descrizione.
func discoverIGD(ctx context.Context) (*igdClient, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	dst := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for _, st := range []string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
	} {
		msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + st + "\r\n\r\n"
		_, _ = conn.WriteTo([]byte(msg), dst)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	re := regexp.MustCompile(`(?im)^location:\s*(\S+)`)
	buf := make([]byte, 4096)
	seen := map[string]bool{}
	for ctx.Err() == nil {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		m := re.FindSubmatch(buf[:n])
		if m == nil || seen[string(m[1])] {
			continue
		}
		seen[string(m[1])] = true
		if c, err := newIGDClient(ctx, string(m[1])); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("nessun router UPnP")
}

func (c *igdClient) soap(ctx context.Context, action string, args [][2]string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	fmt.Fprintf(&b, `<u:%s xmlns:u="%s">`, action, html.EscapeString(c.serviceType))
	for _, a := range args {
		fmt.Fprintf(&b, "<%s>%s</%s>", a[0], html.EscapeString(a[1]), a[0])
	}
	fmt.Fprintf(&b, `</u:%s></s:Body></s:Envelope>`, action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.controlURL, &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, c.serviceType, action))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return data, fmt.Errorf("%s: HTTP %d%s", action, resp.StatusCode, upnpErrorText(data))
	}
	return data, nil
}

var upnpErrRe = regexp.MustCompile(`<errorDescription>([^<]*)</errorDescription>`)

func upnpErrorText(b []byte) string {
	if m := upnpErrRe.FindSubmatch(b); m != nil {
		return " (" + string(m[1]) + ")"
	}
	return ""
}

var extIPRe = regexp.MustCompile(`<NewExternalIPAddress>([^<]*)</NewExternalIPAddress>`)

// GetExternalIP chiede al router il suo indirizzo lato Internet.
func (c *igdClient) GetExternalIP(ctx context.Context) (string, error) {
	data, err := c.soap(ctx, "GetExternalIPAddress", nil)
	if err != nil {
		return "", err
	}
	m := extIPRe.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("risposta inattesa dal router")
	}
	return strings.TrimSpace(string(m[1])), nil
}

func (c *igdClient) localAddr() (string, error) {
	u, err := url.Parse(c.controlURL)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	conn, err := net.Dial("udp4", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// AddPortMapping apre la porta TCP (stessa porta fuori e dentro) verso questo PC.
func (c *igdClient) AddPortMapping(ctx context.Context, port int) error {
	local, err := c.localAddr()
	if err != nil {
		return err
	}
	for _, lease := range []string{"3600", "0"} { // alcuni router accettano solo mappature permanenti
		_, err = c.soap(ctx, "AddPortMapping", [][2]string{
			{"NewRemoteHost", ""},
			{"NewExternalPort", strconv.Itoa(port)},
			{"NewProtocol", "TCP"},
			{"NewInternalPort", strconv.Itoa(port)},
			{"NewInternalClient", local},
			{"NewEnabled", "1"},
			{"NewPortMappingDescription", "FireShare"},
			{"NewLeaseDuration", lease},
		})
		if err == nil {
			return nil
		}
	}
	return err
}

// DeletePortMapping richiude la porta.
func (c *igdClient) DeletePortMapping(ctx context.Context, port int) error {
	_, err := c.soap(ctx, "DeletePortMapping", [][2]string{
		{"NewRemoteHost", ""},
		{"NewExternalPort", strconv.Itoa(port)},
		{"NewProtocol", "TCP"},
	})
	return err
}
