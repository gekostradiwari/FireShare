// Package server implementa il backend HTTPS di FireShare: il "cloud personale"
// che gira sul PC dell'utente. Usa solo la libreria standard di Go.
package server

import (
	"errors"
	"net/http"
	"time"
)

const (
	// APIVersion è la versione del protocollo HTTP esposto ai client.
	APIVersion = 1
	// Version è la versione del backend.
	Version = "0.2.0"

	internalDirName = ".fireshare"
)

// Hooks permette alla GUI/CLI di reagire agli eventi del server.
// Tutte le funzioni sono opzionali e possono essere chiamate da goroutine diverse.
type Hooks struct {
	OnPaired         func(DeviceInfo)
	OnUploadProgress func(UploadProgress)
	OnUploadComplete func(CompletedUpload)
}

// UploadProgress descrive l'avanzamento di un upload in corso.
type UploadProgress struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	Size       int64  `json:"size"`
	Offset     int64  `json:"offset"`
	DeviceName string `json:"deviceName"`
}

// CompletedUpload descrive un upload terminato e verificato.
type CompletedUpload struct {
	File       FileInfo `json:"file"`
	DeviceName string   `json:"deviceName"`
}

// FileInfo descrive un file o una cartella nello spazio cloud.
type FileInfo struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"` // relativo alla radice, separatore "/"
	IsDir      bool      `json:"isDir"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Mime       string    `json:"mime,omitempty"`
}

// apiError è un errore che il server sa tradurre in una risposta HTTP.
type apiError struct {
	Status  int
	Code    string
	Message string
	Offset  *int64 // valorizzato negli errori di offset (409)
}

func (e *apiError) Error() string { return e.Message }

func newErr(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

var (
	errBadPath  = newErr(http.StatusBadRequest, "bad_path", "percorso non valido")
	errNotFound = newErr(http.StatusNotFound, "not_found", "non trovato")
)

func asAPIError(err error) (*apiError, bool) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
