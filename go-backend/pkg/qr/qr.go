// Package qr genera i QR code di pairing mostrati dal desktop.
package qr

import (
	"github.com/skip2/go-qrcode"
)

// PNG restituisce il QR code come immagine PNG quadrata di size pixel.
func PNG(payload string, size int) ([]byte, error) {
	return qrcode.Encode(payload, qrcode.Medium, size)
}

// Terminal restituisce il QR code come testo (due righe di pixel per carattere),
// da stampare in un terminale con font a spaziatura fissa.
func Terminal(payload string) (string, error) {
	q, err := qrcode.New(payload, qrcode.Medium)
	if err != nil {
		return "", err
	}
	return q.ToSmallString(false), nil
}
