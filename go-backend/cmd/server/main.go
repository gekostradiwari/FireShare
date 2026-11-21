// Comando fireshare-server: il backend HTTPS di FireShare da riga di comando.
//
//	go run ./cmd/server                  avvia il server (e mostra il QR di pairing al primo avvio)
//	go run ./cmd/server -pair            mostra subito un nuovo QR di pairing
//	go run ./cmd/server -devices         elenca i telefoni accoppiati
//	go run ./cmd/server -revoke <id>     disconnette un telefono
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fireshare/go-backend/pkg/qr"
	"github.com/fireshare/go-backend/pkg/server"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var publicURLs listFlag
	configDir := flag.String("config", server.DefaultConfigDir(), "cartella di configurazione (certificato, dispositivi, config.json)")
	dataDir := flag.String("data", "", "cartella dello spazio cloud (default: quella salvata in config.json)")
	port := flag.Int("port", 0, "porta HTTPS (default: quella salvata in config.json, altrimenti 8443)")
	name := flag.String("name", "", "nome del server mostrato sul telefono")
	flag.Var(&publicURLs, "public-url", "indirizzo raggiungibile da Internet, es. https://miopc.duckdns.org:8443 (ripetibile)")
	upnp := flag.Bool("upnp", false, "apre da solo la porta sul router (UPnP); usa -upnp=false per spegnere")
	duckDomain := flag.String("duckdns-domain", "", "nome DuckDNS da tenere aggiornato, es. miopc (per miopc.duckdns.org)")
	duckToken := flag.String("duckdns-token", "", "token DuckDNS (salvato in config.json)")
	shareBase := flag.String("share-base-url", "", "indirizzo base dei link di condivisione, es. https://link.miodominio.it")
	pair := flag.Bool("pair", false, "mostra un QR di pairing all'avvio")
	listDevices := flag.Bool("devices", false, "elenca i dispositivi accoppiati ed esce")
	revoke := flag.String("revoke", "", "revoca il dispositivo con questo id ed esce")
	flag.Parse()

	cfg, err := server.LoadConfig(*configDir)
	if err != nil {
		log.Fatalf("config.json non valido: %v", err)
	}
	if *dataDir != "" {
		cfg.RootDir = *dataDir
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *name != "" {
		cfg.ServerName = *name
	}
	if len(publicURLs) > 0 {
		cfg.PublicURLs = publicURLs
	}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "upnp":
			cfg.AutoPortMap = *upnp
		case "duckdns-domain":
			cfg.DuckDNSDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(*duckDomain)), ".duckdns.org")
		case "duckdns-token":
			cfg.DuckDNSToken = strings.TrimSpace(*duckToken)
		case "share-base-url":
			cfg.ShareBaseURL = strings.TrimRight(strings.TrimSpace(*shareBase), "/")
		}
	})
	if err := cfg.Save(*configDir); err != nil {
		log.Printf("attenzione: impossibile salvare config.json: %v", err)
	}

	srv, err := server.New(cfg.Options(*configDir, server.Hooks{
		OnPaired: func(d server.DeviceInfo) {
			fmt.Printf("\n✔ Nuovo dispositivo accoppiato: %s (id %s)\n", d.Name, d.ID)
		},
		OnUploadComplete: func(c server.CompletedUpload) {
			fmt.Printf("✔ Ricevuto da %s: %s (%d byte)\n", c.DeviceName, c.File.Path, c.File.Size)
		},
	}))
	if err != nil {
		log.Fatalf("avvio fallito: %v", err)
	}

	if *listDevices {
		devs := srv.Devices()
		if len(devs) == 0 {
			fmt.Println("Nessun dispositivo accoppiato.")
		}
		for _, d := range devs {
			fmt.Printf("%s  %-24s accoppiato il %s, ultimo accesso %s\n", d.ID, d.Name,
				d.CreatedAt.Local().Format("02/01/2006 15:04"), d.LastSeen.Local().Format("02/01/2006 15:04"))
		}
		return
	}
	if *revoke != "" {
		if srv.RevokeDevice(*revoke) {
			fmt.Println("Dispositivo revocato.")
		} else {
			fmt.Println("Nessun dispositivo con questo id.")
			os.Exit(1)
		}
		return
	}

	fmt.Printf("FireShare %s — spazio cloud: %s\n", server.Version, srv.RootDir())
	fmt.Printf("Impronta certificato (SHA-256): %s\n", srv.Fingerprint())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	time.Sleep(300 * time.Millisecond) // lascia il tempo di aprire la porta (o di fallire)
	select {
	case err := <-errCh:
		log.Fatalf("server fermato: %v", err)
	default:
	}
	fmt.Printf("In ascolto su https://0.0.0.0:%d\n", srv.Port())
	for _, u := range srv.URLs() {
		fmt.Println("  ", u)
	}

	if *pair || len(srv.Devices()) == 0 {
		showPairing(srv, *configDir)
	} else {
		fmt.Println("\nPremi INVIO per mostrare un nuovo QR di pairing, Ctrl+C per uscire.")
	}
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			showPairing(srv, *configDir)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	case err := <-errCh:
		log.Fatalf("server fermato: %v", err)
	}
}

func showPairing(srv *server.Server, configDir string) {
	info, err := srv.NewPairing(10 * time.Minute)
	if err != nil {
		log.Printf("pairing non disponibile: %v", err)
		return
	}
	fmt.Println("\nScansiona questo QR con l'app FireShare (valido 10 minuti):")
	if txt, err := qr.Terminal(info.Payload); err == nil {
		fmt.Println(txt)
	}
	if png, err := qr.PNG(info.Payload, 512); err == nil {
		p := filepath.Join(configDir, "pairing.png")
		if os.WriteFile(p, png, 0o600) == nil {
			fmt.Println("Se il QR non si legge bene nel terminale, apri:", p)
		}
	}
	fmt.Println("Codice manuale:", info.Code)
	fmt.Println("Link manuale:  ", info.Payload)
}
