package icons

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"path/filepath"
	"slices"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
)

const (
	maxPicture = 32 << 20
	maxReply   = 4 << 20
)

type engine interface {
	run(tensor []float32) (out []float32, shape []int64, err error)
}

var openEngine = openORT

type hello struct {
	Ready   bool   `json:"ready"`
	Classes int    `json:"classes,omitempty"`
	Input   [2]int `json:"input"`
	Reason  string `json:"reason,omitempty"`
}

type detection struct {
	Class string   `json:"class"`
	Label string   `json:"label"`
	Score float32  `json:"score"`
	Apps  []string `json:"apps,omitempty"`
	X     float64  `json:"x"`
	Y     float64  `json:"y"`
	W     float64  `json:"w"`
	H     float64  `json:"h"`
	Why   string   `json:"why,omitempty"`
}

type reply struct {
	Seq   uint32      `json:"seq"`
	Found []detection `json:"found,omitempty"`
	MS    int64       `json:"ms"`
	Error string      `json:"error,omitempty"`
}

func writeFrame(w io.Writer, parts ...[]byte) error {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	if _, err := w.Write(binary.BigEndian.AppendUint32(nil, uint32(n))); err != nil {
		return err
	}
	for _, p := range parts {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

func readFrame(r io.Reader, limit int) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if uint64(n) > uint64(limit) {
		return nil, fmt.Errorf("a frame of %d bytes is over the limit of %d", n, limit)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, io.ErrUnexpectedEOF
	}
	return b, nil
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, b)
}

func openDetector(dir string) (*manifest, engine, error) {
	m, err := load(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range []installed{m.model(), m.runtime()} {
		if err := verify(dir, f); err != nil {
			return nil, nil, err
		}
	}
	e, err := openEngine(filepath.Join(dir, m.runtime().name), filepath.Join(dir, m.model().name), m)
	return m, e, err
}

// Serve is the detector process's main: it loads the install in dir and
// answers each picture read from in with what it found, written to out,
// until in ends.
func Serve(dir string, in io.Reader, out io.Writer) error {
	m, e, err := openDetector(dir)
	if err != nil {
		return errors.Join(err, writeJSON(out, hello{Reason: err.Error()}))
	}
	if err := writeJSON(out, hello{Ready: true, Classes: len(m.Classes), Input: [2]int{m.Input.Height, m.Input.Width}}); err != nil {
		return err
	}
	for {
		b, err := readFrame(in, maxPicture+4)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(b) < 4 {
			return errors.New("a request without a number")
		}
		start := time.Now()
		r := reply{Seq: binary.BigEndian.Uint32(b)}
		if r.Found, err = detect(m, e, b[4:]); err != nil {
			r.Error = err.Error()
		}
		r.MS = time.Since(start).Milliseconds()
		if err := writeJSON(out, r); err != nil {
			return err
		}
	}
}

func detect(m *manifest, e engine, pic []byte) ([]detection, error) {
	img, _, err := image.Decode(bytes.NewReader(pic))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	size := img.Bounds()
	tensor, scale, padX, padY := letterbox(img, m.Input.Width, m.Input.Height, uint8(m.Input.Pad))
	out, shape, err := e.run(tensor)
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return decodeYOLO(out, shape, m, scale, padX, padY, size.Dx(), size.Dy())
}

func letterbox(img image.Image, w, h int, pad uint8) (tensor []float32, scale, padX, padY float64) {
	b := img.Bounds()
	scale = math.Min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	nw, nh := int(math.Round(float64(b.Dx())*scale)), int(math.Round(float64(b.Dy())*scale))
	padX, padY = float64(w-nw)/2, float64(h-nh)/2
	left, top := int(math.Round(padX-0.1)), int(math.Round(padY-0.1))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{pad, pad, pad, 255}), image.Point{}, draw.Src)
	draw.BiLinear.Scale(dst, image.Rect(left, top, left+nw, top+nh), img, b, draw.Src, nil)
	tensor = make([]float32, 3*w*h)
	plane := w * h
	for y := range h {
		for x := range w {
			i, p := dst.PixOffset(x, y), y*w+x
			tensor[p] = float32(dst.Pix[i]) / 255
			tensor[plane+p] = float32(dst.Pix[i+1]) / 255
			tensor[2*plane+p] = float32(dst.Pix[i+2]) / 255
		}
	}
	return tensor, scale, padX, padY
}

type candidate struct {
	class          int
	score          float32
	x1, y1, x2, y2 float64
}

func decodeYOLO(out []float32, shape []int64, m *manifest, scale, padX, padY float64, pw, ph int) ([]detection, error) {
	classes := len(m.Classes)
	if len(shape) != 3 || shape[0] != 1 || shape[1] != int64(4+classes) || shape[2] < 0 || int64(len(out)) != shape[1]*shape[2] {
		return nil, fmt.Errorf("an answer of shape %v with %d values for %d classes", shape, len(out), classes)
	}
	n := int(shape[2])
	var all []candidate
	for j := range n {
		best, score := -1, float32(0)
		for k := range classes {
			if v := out[(4+k)*n+j]; v > score {
				best, score = k, v
			}
		}
		if best < 0 || score < min(m.MinScore, m.minScore(m.Classes[best])) {
			continue
		}
		cx, cy := (float64(out[j])-padX)/scale, (float64(out[n+j])-padY)/scale
		w, h := float64(out[2*n+j])/scale, float64(out[3*n+j])/scale
		inside := func(v float64, size int) float64 { return math.Max(0, math.Min(float64(size), v)) / float64(size) }
		all = append(all, candidate{best, score, inside(cx-w/2, pw), inside(cy-h/2, ph), inside(cx+w/2, pw), inside(cy+h/2, ph)})
	}
	slices.SortStableFunc(all, func(a, b candidate) int { return cmp.Compare(b.score, a.score) })
	var kept []candidate
	var found []detection
	for _, c := range all {
		if slices.ContainsFunc(kept, func(k candidate) bool {
			return k.class == c.class && iou(k.x1, k.y1, k.x2, k.y2, c.x1, c.y1, c.x2, c.y2) > m.NMSIoU
		}) {
			continue
		}
		kept = append(kept, c)
		cl := m.Classes[c.class]
		d := detection{Class: cl.Name, Label: cl.Label, Score: c.score, Apps: cl.Apps, X: c.x1, Y: c.y1, W: c.x2 - c.x1, H: c.y2 - c.y1}
		switch floor := m.minScore(cl); {
		case cl.Off:
			d.Why = "off"
		case c.score < floor:
			d.Why = fmt.Sprintf("below %.2f", floor)
		}
		found = append(found, d)
	}
	return found, nil
}

func iou(ax1, ay1, ax2, ay2, bx1, by1, bx2, by2 float64) float64 {
	both := math.Max(0, math.Min(ax2, bx2)-math.Max(ax1, bx1)) * math.Max(0, math.Min(ay2, by2)-math.Max(ay1, by1))
	either := (ax2-ax1)*(ay2-ay1) + (bx2-bx1)*(by2-by1) - both
	if either <= 0 {
		return 0
	}
	return both / either
}
