package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile = "tls-cert.pem"
	keyFile  = "tls-key.pem"
)

// Fingerprint calcola l'impronta SHA-256 (esadecimale minuscola) del certificato
// foglia. È il valore che il telefono "pinna" leggendo il QR di pairing.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// loadOrCreateCert carica il certificato TLS di FireShare dalla cartella di
// configurazione, oppure ne genera uno autofirmato (ECDSA P-256, validità 10
// anni). Se il certificato sta per scadere viene rigenerato: in quel caso i
// telefoni già accoppiati devono rifare il pairing.
func loadOrCreateCert(dir string) (tls.Certificate, string, error) {
	certPath := filepath.Join(dir, certFile)
	keyPath := filepath.Join(dir, keyFile)

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && len(cert.Certificate) > 0 {
		if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil &&
			time.Now().Add(30*24*time.Hour).Before(leaf.NotAfter) {
			cert.Leaf = leaf
			return cert, Fingerprint(cert.Certificate[0]), nil
		}
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return tls.Certificate{}, "", err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "FireShare", Organization: []string{"FireShare"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if host, herr := os.Hostname(); herr == nil && host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	for _, ip := range localIPv4s() {
		if p := net.ParseIP(ip); p != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, p)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := atomicWriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := atomicWriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, Fingerprint(der), nil
}
