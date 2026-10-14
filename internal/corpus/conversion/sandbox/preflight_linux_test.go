//go:build linux

package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runPreflight(t *testing.T, data []byte, kind OutputType, limits Limits) (Receipt, error) {
	t.Helper()
	a := testAttempt(t, "success")
	if err := os.Remove(filepath.Join(a.InputDir, "mode")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.InputDir, "fixture"), data, 0400); err != nil {
		t.Fatal(err)
	}
	a.InputType = kind
	return newTestSupervisor(t, limits, nil).Run(context.Background(), a)
}

func TestZIPBombDeclaredExpansion(t *testing.T) {
	encoded, err := os.ReadFile("testdata/zip-bomb.hex")
	if err != nil {
		t.Fatal(err)
	}
	data, err := hex.DecodeString(strings.Join(strings.Fields(string(encoded)), ""))
	if err != nil {
		t.Fatal(err)
	}
	// Ensure this is a ZIP with the intended declared expansion, not a malformed
	// file that happens to fail. Actual payload is empty; header declares 1 GiB.
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 1 || z.File[0].UncompressedSize64 != 1<<30 {
		t.Fatal("fixture lost bomb header")
	}
	r, err := runPreflight(t, data, TypeZIP, OfficeLimits())
	assertFailure(t, r, err, ResourceLimit, BoundExpanded)
}

func TestZIPStreamingExpansionAndMembers(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte{'x'}, 8<<10)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	l := OfficeLimits()
	l.ExpansionRatio = 2
	r, err := runPreflight(t, b.Bytes(), TypeZIP, l)
	assertFailure(t, r, err, ResourceLimit, BoundRatio)
	b.Reset()
	w = zip.NewWriter(&b)
	for _, name := range []string{"a", "b"} {
		if _, err := w.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	l = OfficeLimits()
	l.Members = 1
	r, err = runPreflight(t, b.Bytes(), TypeZIP, l)
	assertFailure(t, r, err, ResourceLimit, BoundMembers)
}

func TestZIPReadsPayloadAndChecksCRC(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "payload", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("synthetic CRC payload")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), b.Bytes()...)
	index := bytes.Index(data, []byte("synthetic CRC payload"))
	if index < 0 {
		t.Fatal("stored fixture lost payload")
	}
	data[index] ^= 1
	r, err := runPreflight(t, data, TypeZIP, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestDeepXMLInputAndOutput(t *testing.T) {
	data, err := os.ReadFile("testdata/deep.xml")
	if err != nil {
		t.Fatal(err)
	}
	l := OfficeLimits()
	l.XMLDepth = 10
	r, err := runPreflight(t, data, TypeXML, l)
	assertFailure(t, r, err, ResourceLimit, BoundXML)
	a := testAttempt(t, "deep-output")
	if err := os.WriteFile(filepath.Join(a.InputDir, "deep.xml"), data, 0400); err != nil {
		t.Fatal(err)
	}
	r, err = newTestSupervisor(t, l, nil).Run(context.Background(), a)
	assertFailure(t, r, err, ResourceLimit, BoundXML)
}

func TestImageBombAndMalformedPixels(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 1100, 1000))); err != nil {
		t.Fatal(err)
	}
	l := OfficeLimits()
	l.Megapixels = 1
	r, err := runPreflight(t, b.Bytes(), TypePNG, l)
	assertFailure(t, r, err, ResourceLimit, BoundPixels)
	r, err = runPreflight(t, b.Bytes()[:len(b.Bytes())-20], TypePNG, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestRejectDTDsAndDisguisedPackages(t *testing.T) {
	r, err := runPreflight(t, []byte(`<!DOCTYPE x [<!ENTITY a SYSTEM "https://example.invalid">]><x>&a;</x>`), TypeXML, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
	r, err = runPreflight(t, []byte{'P', 'K', 3, 4}, TypeText, OfficeLimits())
	assertFailure(t, r, err, UnsafeOutput, "")
}

func TestOriginalInputBound(t *testing.T) {
	l := OfficeLimits()
	l.InputBytes = 4
	r, err := newTestSupervisor(t, l, nil).Run(context.Background(), testAttempt(t, "success"))
	assertFailure(t, r, err, ResourceLimit, BoundInput)
}

func TestXMLStreamingByteBound(t *testing.T) {
	l := OfficeLimits()
	l.ExpandedBytes = 32
	r, err := runPreflight(t, []byte("<a>"+strings.Repeat("x", 128)+"</a>"), TypeXML, l)
	assertFailure(t, r, err, ResourceLimit, BoundExpanded)
}
