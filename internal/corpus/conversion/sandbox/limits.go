// Package sandbox supervises disposable untrusted-byte workers. It has no
// transport, database, credential, or Temporal client. Its child protocol is
// distinct from pure processing over validated blocks on corpus-parser-v2.
//
// Linux process accounting detects resource overruns; deployment must also
// impose hard cgroup CPU/memory limits and bounded scratch mounts. Containers
// must deny network, use user namespaces and seccomp, drop capabilities, and
// mount the root and input read-only. Go does not establish those boundaries.
package sandbox

import (
	"fmt"
	"math"
	"time"
)

// Limits freezes every positive ceiling for one attempt, including validation.
type Limits struct {
	WallTime       time.Duration
	CPUTime        time.Duration
	VCPUs          int
	MemoryBytes    int64
	ScratchBytes   int64
	OutputBytes    int64
	TempFiles      int
	InputBytes     int64
	ExpandedBytes  int64
	Members        int
	XMLDepth       int
	Pages          int
	Megapixels     int
	ExpansionRatio int64
}

// TaskQueue routes byte-reading recipes to the isolated converter worker.
// This package has no plain CPU execution route.
const TaskQueue = "corpus-converter-v2"

// CheckPages bounds the actual running page/frame count supplied by an adapter.
// A source header's promised count is not sufficient evidence.
func (l Limits) CheckPages(count int) error {
	if count < 0 {
		return &Failure{Cause: UnsafeOutput}
	}
	if count > l.Pages {
		return exceeded(BoundPages)
	}
	return nil
}

// OfficeLimits returns the spec's proposed defaults, unmeasured on hosted
// workers. CPUTime (240 CPU seconds), TempFiles (10,000), and ExpansionRatio
// (100) are additional conservative configurable ceilings, not approved O7
// values. PDFLimits changes the proposed wall allowance to 300 seconds.
func OfficeLimits() Limits {
	return Limits{WallTime: 120 * time.Second, CPUTime: 240 * time.Second, VCPUs: 2,
		MemoryBytes: 4 << 30, ScratchBytes: 2 << 30, OutputBytes: 256 << 20,
		TempFiles: 10000, InputBytes: 128 << 20, ExpandedBytes: 512 << 20,
		Members: 10000, XMLDepth: 128, Pages: 500, Megapixels: 40, ExpansionRatio: 100}
}

// PDFLimits returns proposed PDF ceilings, unmeasured on hosted workers.
func PDFLimits() Limits {
	l := OfficeLimits()
	l.WallTime = 300 * time.Second
	l.CPUTime = 600 * time.Second
	return l
}

// Validate rejects omitted, zero, and negative dimensions.
func (l Limits) Validate() error {
	if l.WallTime <= 0 || l.CPUTime <= 0 || l.VCPUs <= 0 || l.MemoryBytes <= 0 || l.ScratchBytes <= 0 || l.OutputBytes <= 0 || l.TempFiles <= 0 || l.InputBytes <= 0 || l.ExpandedBytes <= 0 || l.Members <= 0 || l.XMLDepth <= 0 || l.Pages <= 0 || l.Megapixels <= 0 || l.ExpansionRatio <= 0 {
		return fmt.Errorf("every sandbox ceiling must be positive")
	}
	for _, value := range []int64{l.MemoryBytes, l.ScratchBytes, l.OutputBytes, l.InputBytes, l.ExpandedBytes} {
		if value > math.MaxInt64/2 {
			return fmt.Errorf("sandbox byte ceiling exceeds safe accounting range")
		}
	}
	if int64(l.Megapixels) > math.MaxInt64/1000000 {
		return fmt.Errorf("pixel ceiling exceeds safe accounting range")
	}
	return nil
}

// Bound identifies the exact exceeded ceiling without retaining document data.
type Bound string

const (
	BoundCPU      Bound = "cpu_time"
	BoundMemory   Bound = "resident_memory"
	BoundScratch  Bound = "scratch_bytes"
	BoundOutput   Bound = "output_bytes"
	BoundFiles    Bound = "temp_files"
	BoundInput    Bound = "input_bytes"
	BoundExpanded Bound = "expanded_bytes"
	BoundMembers  Bound = "members"
	BoundXML      Bound = "xml_depth"
	BoundPages    Bound = "pages_or_frames"
	BoundPixels   Bound = "megapixels"
	BoundRatio    Bound = "expansion_ratio"
)

// Cause is a closed terminal finding vocabulary.
type Cause string

const (
	ResourceLimit Cause = "RESOURCE_LIMIT"
	Timeout       Cause = "TIMEOUT"
	Cancelled     Cause = "CANCELLED"
	ExitFailure   Cause = "EXIT_FAILURE"
	UnsafeOutput  Cause = "UNSAFE_PACKAGE"
)

// Failure contains machine reasons only. It intentionally excludes child logs.
type Failure struct {
	Cause Cause
	Bound Bound
}

func (f *Failure) Error() string { return fmt.Sprintf("sandbox: %s (%s)", f.Cause, f.Bound) }

func exceeded(b Bound) error { return &Failure{Cause: ResourceLimit, Bound: b} }

func (f *Failure) valid() bool {
	if f.Cause != ResourceLimit {
		return f.Bound == "" && (f.Cause == Timeout || f.Cause == Cancelled || f.Cause == ExitFailure || f.Cause == UnsafeOutput)
	}
	switch f.Bound {
	case BoundCPU, BoundMemory, BoundScratch, BoundOutput, BoundFiles, BoundInput, BoundExpanded, BoundMembers, BoundXML, BoundPages, BoundPixels, BoundRatio:
		return true
	default:
		return false
	}
}
