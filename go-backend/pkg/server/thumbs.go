package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Le miniature vengono generate una volta sola e conservate in .fireshare/thumbs.
// Immagini JPEG/PNG/GIF: solo libreria standard (con correzione dell'orientamento EXIF).
// Altri formati (HEIC, WebP...) e video: servono il programma ffmpeg installato sul PC;
// senza ffmpeg il server risponde 404 "no_thumb" e l'app mostra un'icona.

var thumbSizes = map[int]bool{160: true, 320: true, 640: true}

// thumbSlots limita le miniature generate contemporaneamente (decodificare una
// foto da 48 megapixel occupa centinaia di MB di RAM).
var thumbSlots = make(chan struct{}, 2)

func (s *Server) thumbPath(abs string, info os.FileInfo, size int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%d", abs, info.Size(), info.ModTime().UnixNano(), size)))
	return filepath.Join(s.root, internalDirName, "thumbs", hex.EncodeToString(h[:12])+".jpg")
}

// handleThumb: GET /api/v1/files/thumb?path=...&size=160|320|640
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request, _ Device) {
	abs, info, err := s.mediaFile(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if !thumbSizes[size] {
		size = 320
	}
	if mediaKind(info.Name()) == "" {
		writeError(w, s.logger, newErr(http.StatusNotFound, "no_thumb", "anteprima non disponibile"))
		return
	}

	cache := s.thumbPath(abs, info, size)
	if _, err := os.Stat(cache); err != nil {
		select {
		case thumbSlots <- struct{}{}:
			defer func() { <-thumbSlots }()
		case <-r.Context().Done():
			return
		}
		// un'altra richiesta potrebbe aver finito nel frattempo
		if _, err := os.Stat(cache); err != nil {
			data, gerr := generateThumb(r.Context(), abs, mediaKind(info.Name()), size)
			if gerr != nil {
				writeError(w, s.logger, newErr(http.StatusNotFound, "no_thumb", "anteprima non disponibile"))
				return
			}
			if err := os.MkdirAll(filepath.Dir(cache), 0o755); err == nil {
				_ = atomicWriteFile(cache, data, 0o644)
			}
			serveThumbBytes(w, r, data, info.ModTime())
			return
		}
	}
	f, err := os.Open(cache)
	if err != nil {
		writeError(w, s.logger, err)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "thumb.jpg", st.ModTime(), f)
}

func serveThumbBytes(w http.ResponseWriter, r *http.Request, data []byte, mod time.Time) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "thumb.jpg", mod, bytes.NewReader(data))
}

func generateThumb(ctx context.Context, abs, kind string, size int) ([]byte, error) {
	if kind == "image" {
		if data, err := thumbFromImage(abs, size); err == nil {
			return data, nil
		}
	}
	return thumbFromFFmpeg(ctx, abs, size)
}

// thumbFromImage decodifica con la libreria standard (JPEG, PNG, GIF).
func thumbFromImage(abs string, size int) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	orientation := 1
	if strings.HasSuffix(strings.ToLower(abs), ".jpg") || strings.HasSuffix(strings.ToLower(abs), ".jpeg") {
		head := make([]byte, 128*1024)
		n, _ := io.ReadFull(f, head)
		orientation = jpegOrientation(head[:n])
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	small := scaleDown(img, size)
	small = applyOrientation(small, orientation)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// thumbFromFFmpeg usa ffmpeg (se installato) per video e formati non supportati.
func thumbFromFFmpeg(ctx context.Context, abs string, size int) ([]byte, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg non installato")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	vf := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", size, size)
	var lastErr error
	for _, ss := range []string{"1", "0"} {
		cmd := exec.CommandContext(ctx, bin, "-v", "error", "-ss", ss, "-i", abs,
			"-frames:v", "1", "-vf", vf, "-f", "image2pipe", "-c:v", "mjpeg", "-q:v", "4", "-")
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err == nil && out.Len() > 0 {
			return out.Bytes(), nil
		} else {
			lastErr = fmt.Errorf("ffmpeg: %v %s", err, errb.String())
		}
	}
	return nil, lastErr
}

// scaleDown riduce l'immagine in modo che il lato maggiore sia al massimo maxDim,
// mediando alcuni campioni per pixel di destinazione (veloce e di qualità adeguata).
func scaleDown(src image.Image, maxDim int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	dw, dh := sw, sh
	if sw >= sh && sw > maxDim {
		dw, dh = maxDim, max1(sh*maxDim/sw)
	} else if sh > sw && sh > maxDim {
		dw, dh = max1(sw*maxDim/sh), maxDim
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	if dw == sw && dh == sh {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
		return dst
	}
	for y := 0; y < dh; y++ {
		y0 := b.Min.Y + y*sh/dh
		y1 := b.Min.Y + (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0 := b.Min.X + x*sw/dw
			x1 := b.Min.X + (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			stepX, stepY := (x1-x0+3)/4, (y1-y0+3)/4
			var rs, gs, bs, as, n uint32
			for yy := y0; yy < y1; yy += stepY {
				for xx := x0; xx < x1; xx += stepX {
					r, g, bl, a := src.At(xx, yy).RGBA()
					rs, gs, bs, as, n = rs+r, gs+g, bs+bl, as+a, n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(rs / n >> 8), uint8(gs / n >> 8), uint8(bs / n >> 8), uint8(as / n >> 8)})
		}
	}
	return dst
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

// applyOrientation ruota/specchia secondo il valore EXIF (1..8).
func applyOrientation(src *image.RGBA, o int) *image.RGBA {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2: // specchio orizzontale
				nx, ny = w-1-x, y
			case 3: // 180°
				nx, ny = w-1-x, h-1-y
			case 4: // specchio verticale
				nx, ny = x, h-1-y
			case 5: // trasposta
				nx, ny = y, x
			case 6: // 90° orario
				nx, ny = h-1-y, x
			case 7: // trasversa
				nx, ny = h-1-y, w-1-x
			case 8: // 90° antiorario
				nx, ny = y, w-1-x
			}
			dst.SetRGBA(nx, ny, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// jpegOrientation legge il tag EXIF Orientation (0x0112) dall'inizio di un JPEG.
// Restituisce 1 (nessuna rotazione) se il tag manca o il file è anomalo.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xD9 || marker == 0xDA { // fine immagine / inizio dati
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(b) {
			return 1
		}
		if marker == 0xE1 && segLen >= 16 && string(b[i+4:i+10]) == "Exif\x00\x00" {
			return tiffOrientation(b[i+10 : i+2+segLen])
		}
		i += 2 + segLen
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			v := int(bo.Uint16(t[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
