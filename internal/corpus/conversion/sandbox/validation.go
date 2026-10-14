package sandbox

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
)

// OutputType is a closed set. Types are checked against bytes, not extensions.
type OutputType string

const (
	TypeJSON OutputType = "json"
	TypeText OutputType = "text"
	TypeZIP  OutputType = "zip"
	TypeXML  OutputType = "xml"
	TypePNG  OutputType = "png"
	TypeJPEG OutputType = "jpeg"
)

func (t OutputType) valid() bool {
	return t == TypeJSON || t == TypeText || t == TypeZIP || t == TypeXML || t == TypePNG || t == TypeJPEG
}

// Output declares one inert staged file. No host paths or destination URLs exist.
type Output struct {
	Path string
	Type OutputType
	Size int64
}

// Manifest is written as manifest.json in the private output directory.
type Manifest struct{ Outputs []Output }

// validateStaged implements Run's supervised staged-output adapter. It is
// deliberately private: consumers cannot bypass the validation child. Ledger
// resolution, schema/transform/provenance checks belong to the later converter.
// It validates every declaration before opening any declared output file.
func validateStaged(directory string, l Limits) ([]model.AssetRef, error) {
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("validate staged limits: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open output root: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat("manifest.json")
	if err != nil || !info.Mode().IsRegular() {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	manifestBound := min(l.OutputBytes, int64(1<<20))
	if info.Size() > manifestBound {
		return nil, exceeded(BoundOutput)
	}
	f, err := root.Open("manifest.json")
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	var m Manifest
	d := json.NewDecoder(io.LimitReader(f, manifestBound+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	if len(m.Outputs) == 0 {
		return nil, &Failure{Cause: UnsafeOutput}
	}
	if len(m.Outputs) > l.TempFiles {
		return nil, exceeded(BoundFiles)
	}
	seen := map[string]bool{}
	remaining := l.OutputBytes
	images := 0
	for _, o := range m.Outputs {
		if !safePath(o.Path) || o.Path == "manifest.json" || seen[o.Path] || !o.Type.valid() || o.Size < 0 {
			return nil, &Failure{Cause: UnsafeOutput}
		}
		seen[o.Path] = true
		if o.Type == TypePNG || o.Type == TypeJPEG {
			images++
			if err := l.CheckPages(images); err != nil {
				return nil, err
			}
		}
		// Check each component before opening: Root prevents escape but permits
		// in-root symlinks, which this protocol deliberately rejects too.
		parts := strings.Split(o.Path, "/")
		for i := range parts {
			stat, err := root.Lstat(strings.Join(parts[:i+1], "/"))
			if err != nil || stat.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !stat.IsDir()) || (i == len(parts)-1 && (!stat.Mode().IsRegular() || stat.Size() != o.Size)) {
				return nil, &Failure{Cause: UnsafeOutput}
			}
		}
		if o.Size > remaining {
			return nil, exceeded(BoundOutput)
		}
		remaining -= o.Size
	}
	// Reject undeclared files and special nodes, including unused symlinks.
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." || entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || (name != "manifest.json" && !seen[name]) {
			return &Failure{Cause: UnsafeOutput}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("check output inventory: %w", err)
	}
	refs := make([]model.AssetRef, 0, len(m.Outputs))
	for _, o := range m.Outputs {
		file, err := root.Open(o.Path)
		if err != nil {
			return nil, fmt.Errorf("open declared output: %w", err)
		}
		err = preflight(file, o.Size, o.Type, l)
		if err == nil {
			_, err = file.Seek(0, io.SeekStart)
		}
		h := sha256.New()
		var size int64
		if err == nil {
			size, err = io.Copy(h, io.LimitReader(file, o.Size+1))
		}
		closeErr := file.Close()
		if err != nil {
			return nil, fmt.Errorf("validate output: %w", err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close output: %w", closeErr)
		}
		if size != o.Size {
			return nil, &Failure{Cause: UnsafeOutput}
		}
		refs = append(refs, model.AssetRef{Path: o.Path, SHA256: model.Digest(hex.EncodeToString(h.Sum(nil))), ByteSize: size})
	}
	return refs, nil
}

func safePath(p string) bool {
	if p == "." || !fs.ValidPath(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\:") {
		return false
	}
	for _, c := range p {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

// preflight checks bounded bytes inside a supervised child. ZIP and XML checks
// stream content; image dimensions are checked before and after full decoding.
// It never extracts archives or constructs legal document blocks.
func preflight(f *os.File, size int64, kind OutputType, l Limits) error {
	if err := l.Validate(); err != nil {
		return fmt.Errorf("preflight limits: %w", err)
	}
	if size < 0 || size > l.InputBytes {
		return exceeded(BoundInput)
	}
	if !kind.valid() {
		return &Failure{Cause: UnsafeOutput}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek preflight: %w", err)
	}
	var magic [8]byte
	n, err := f.ReadAt(magic[:], 0)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read signature: %w", err)
	}
	// A declared text/JSON file cannot disguise common package/image bombs.
	if n >= 4 && bytes.Equal(magic[:4], []byte{'P', 'K', 3, 4}) && kind != TypeZIP {
		return &Failure{Cause: UnsafeOutput}
	}
	if n >= 8 && bytes.Equal(magic[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) && kind != TypePNG {
		return &Failure{Cause: UnsafeOutput}
	}
	if n >= 2 && magic[0] == 255 && magic[1] == 216 && kind != TypeJPEG {
		return &Failure{Cause: UnsafeOutput}
	}
	r := io.LimitReader(f, size+1)
	switch kind {
	case TypeZIP:
		return checkZIP(f, size, l)
	case TypeXML:
		return checkXML(r, l)
	case TypePNG, TypeJPEG:
		return checkImage(f, size, kind, l)
	case TypeJSON:
		return checkJSON(r, l)
	case TypeText:
		n, err := io.Copy(io.Discard, r)
		if err != nil {
			return fmt.Errorf("stream text: %w", err)
		}
		if n > size {
			return exceeded(BoundInput)
		}
	}
	return nil
}

func checkXML(r io.Reader, l Limits) error {
	counted := &countReader{r: io.LimitReader(r, l.ExpandedBytes+1)}
	d := xml.NewDecoder(counted)
	depth := 0
	roots := 0
	for {
		t, err := d.Token()
		if counted.n > l.ExpandedBytes {
			return exceeded(BoundExpanded)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return &Failure{Cause: UnsafeOutput}
		}
		if d.InputOffset() > l.ExpandedBytes {
			return exceeded(BoundExpanded)
		}
		switch t.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots > 1 {
					return &Failure{Cause: UnsafeOutput}
				}
			}
			depth++
			if depth > l.XMLDepth {
				return exceeded(BoundXML)
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			// DTDs and entities have no place in an inert staged document.
			return &Failure{Cause: UnsafeOutput}
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(t.(xml.CharData))) != 0 {
				return &Failure{Cause: UnsafeOutput}
			}
		}
	}
	if depth != 0 || roots != 1 {
		return &Failure{Cause: UnsafeOutput}
	}
	return nil
}

func checkJSON(r io.Reader, l Limits) error {
	d := json.NewDecoder(r)
	d.UseNumber()
	depth := 0
	values := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return &Failure{Cause: UnsafeOutput}
		}
		if depth == 0 {
			values++
			if values > 1 {
				return &Failure{Cause: UnsafeOutput}
			}
		}
		if delim, ok := t.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				depth++
			} else {
				depth--
			}
			if depth > l.XMLDepth {
				return exceeded(BoundXML)
			}
		}
	}
	if depth != 0 || values != 1 {
		return &Failure{Cause: UnsafeOutput}
	}
	return nil
}

func checkZIP(f *os.File, size int64, l Limits) error {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return &Failure{Cause: UnsafeOutput}
	}
	if len(z.File) > l.Members {
		return exceeded(BoundMembers)
	}
	remaining := l.ExpandedBytes
	seen := map[string]bool{}
	images := 0
	for _, member := range z.File {
		name := strings.TrimSuffix(member.Name, "/")
		if !safePath(name) || seen[name] || member.Mode()&os.ModeSymlink != 0 || (!member.FileInfo().IsDir() && !member.Mode().IsRegular()) {
			return &Failure{Cause: UnsafeOutput}
		}
		seen[name] = true
		if member.UncompressedSize64 > uint64(remaining) {
			return exceeded(BoundExpanded)
		}
		if exceedsRatio(member.UncompressedSize64, uint64(max(size, 1)), l.ExpansionRatio) {
			return exceeded(BoundRatio)
		}
		if member.FileInfo().IsDir() {
			continue
		}
		r, err := member.Open()
		if err != nil {
			return &Failure{Cause: UnsafeOutput}
		}
		counter := &countReader{r: io.LimitReader(r, remaining+1)}
		err = checkMember(counter, name, l, &images)
		closeErr := r.Close()
		if counter.n > remaining {
			return exceeded(BoundExpanded)
		}
		remaining -= counter.n
		if exceedsRatio(uint64(l.ExpandedBytes-remaining), uint64(max(size, 1)), l.ExpansionRatio) {
			return exceeded(BoundRatio)
		}
		if err != nil {
			return fmt.Errorf("stream package member: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("close package member: %w", closeErr)
		}
	}
	return nil
}

func exceedsRatio(expanded, compressed uint64, ratio int64) bool {
	quotient, remainder := expanded/compressed, expanded%compressed
	return quotient > uint64(ratio) || (quotient == uint64(ratio) && remainder != 0)
}

func checkMember(r io.Reader, name string, l Limits, images *int) error {
	var prefix [512]byte
	n, err := io.ReadFull(r, prefix[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return &Failure{Cause: UnsafeOutput}
	}
	all := io.MultiReader(bytes.NewReader(prefix[:n]), r)
	// Nested archives require a separately frozen recipe. They cannot bypass
	// this attempt's aggregate member/expansion accounting by nesting packages.
	if n >= 4 && bytes.Equal(prefix[:4], []byte{'P', 'K', 3, 4}) {
		return &Failure{Cause: UnsafeOutput}
	}
	if strings.HasSuffix(strings.ToLower(name), ".xml") || bytes.HasPrefix(bytes.TrimSpace(prefix[:n]), []byte{'<'}) {
		return checkXML(all, l)
	}
	var kind OutputType
	if n >= 8 && bytes.Equal(prefix[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		kind = TypePNG
	}
	if n >= 2 && prefix[0] == 255 && prefix[1] == 216 {
		kind = TypeJPEG
	}
	if kind != "" {
		*images = *images + 1
		if err := l.CheckPages(*images); err != nil {
			return err
		}
		// Spool only the bounded compressed member, then decode inside the
		// supervised child. No archive member name is used as a filesystem path.
		f, err := os.CreateTemp(".", "preflight-image-")
		if err != nil {
			return fmt.Errorf("stage package image: %w", err)
		}
		defer os.Remove(f.Name())
		defer f.Close()
		size, err := io.Copy(f, io.LimitReader(all, min(l.InputBytes, l.ScratchBytes)+1))
		if err != nil {
			return fmt.Errorf("stream package image: %w", err)
		}
		if size > l.ScratchBytes {
			return exceeded(BoundScratch)
		}
		if size > l.InputBytes {
			return exceeded(BoundInput)
		}
		return preflight(f, size, kind, l)
	}
	_, err = io.Copy(io.Discard, all)
	if err != nil {
		return &Failure{Cause: UnsafeOutput}
	}
	return nil
}

type countReader struct {
	r io.Reader
	n int64
}

func (r *countReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

func checkImage(f *os.File, size int64, kind OutputType, l Limits) error {
	var width, height int
	if kind == TypePNG {
		cfg, err := png.DecodeConfig(io.LimitReader(f, size+1))
		if err != nil {
			return &Failure{Cause: UnsafeOutput}
		}
		width, height = cfg.Width, cfg.Height
	} else {
		cfg, err := jpeg.DecodeConfig(io.LimitReader(f, size+1))
		if err != nil {
			return &Failure{Cause: UnsafeOutput}
		}
		width, height = cfg.Width, cfg.Height
	}
	if width <= 0 || height <= 0 || int64(width) > int64(l.Megapixels)*1000000/int64(height) {
		return exceeded(BoundPixels)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek image: %w", err)
	}
	if kind == TypePNG {
		img, err := png.Decode(io.LimitReader(f, size+1))
		if err != nil || img.Bounds().Dx() != width || img.Bounds().Dy() != height {
			return &Failure{Cause: UnsafeOutput}
		}
	} else {
		img, err := jpeg.Decode(io.LimitReader(f, size+1))
		if err != nil || img.Bounds().Dx() != width || img.Bounds().Dy() != height {
			return &Failure{Cause: UnsafeOutput}
		}
	}
	return nil
}
