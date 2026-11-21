package server

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// cleanRel normalizza un percorso relativo ricevuto da un client (lettura,
// cancellazione, spostamento di file già esistenti). Rifiuta ".." , caratteri di
// controllo, ":" (drive e stream NTFS), segmenti che Windows normalizzerebbe
// (terminano con "." o spazio) e la cartella interna del server.
func cleanRel(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	var parts []string
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", errBadPath
		}
		if hasControl(seg) || strings.ContainsRune(seg, ':') {
			return "", errBadPath
		}
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return "", errBadPath
		}
		if strings.EqualFold(seg, internalDirName) {
			return "", errBadPath
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "/"), nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// sanitizeName rende sicuro un nome di file scelto dal client: toglie eventuali
// cartelle, sostituisce i caratteri non validi su Windows, evita i nomi riservati.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		case r == utf8.RuneError:
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimLeft(strings.TrimRight(b.String(), " ."), " ")
	if s == "" {
		s = "file"
	}
	if isReservedName(s) || strings.EqualFold(s, internalDirName) {
		s = "_" + s
	}
	return truncateName(s, 200)
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func isReservedName(s string) bool {
	base := s
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return reservedNames[strings.ToUpper(base)]
}

func truncateName(s string, max int) string {
	if len(s) <= max {
		return s
	}
	ext := filepath.Ext(s)
	if len(ext) > 20 {
		ext = ""
	}
	base := s[:len(s)-len(ext)]
	cut := max - len(ext)
	for cut > 0 && !utf8.RuneStart(base[cut]) {
		cut--
	}
	return base[:cut] + ext
}

// sanitizeDir ripulisce ogni segmento di una cartella di destinazione scelta dal
// client. ".." è un errore (non viene "corretto" silenziosamente).
func sanitizeDir(dir string) (string, error) {
	dir = strings.ReplaceAll(dir, "\\", "/")
	var parts []string
	for _, seg := range strings.Split(dir, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", errBadPath
		}
		parts = append(parts, sanitizeName(seg))
	}
	return strings.Join(parts, "/"), nil
}

// isWithin dice se p sta dentro root (entrambi già assoluti).
func isWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveInRoot trasforma un percorso relativo già pulito in un percorso assoluto
// e verifica che, anche seguendo i link simbolici esistenti, resti dentro root.
func resolveInRoot(root, rel string) (string, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if !isWithin(root, abs) {
		return "", errBadPath
	}
	cur := abs
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if !isWithin(root, real) {
				return "", errBadPath
			}
			return abs, nil
		}
		if !isNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errBadPath
		}
		cur = parent
	}
}
