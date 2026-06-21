package bnm

import (
	"testing"
	"time"
)

// Real BNM listing row shapes: a tech PD (absolute href) and a non-tech PD
// (relative href). parseSector returns BOTH (scope.Match filters to the tech
// subset later, via the BNM signal in Number).
const sectorHTML = `<table id="filta"><tbody>
<tr><td style="white-space:nowrap;"><span class="hidden">2025/11/00</span>28 Nov 2025</td>
<td><p><a href="https://www.bnm.gov.my/documents/20124/938039/pd-rmit-nov25.pdf">Risk Management in Technology (RMiT)</a></p></td>
<td class=" test"><div class="badge status-badge badge-info">Policy Document</div></td><td class="tohideall">2025</td></tr>
<tr><td><span class="hidden">2026/03/00</span>27 Mar 2026</td>
<td><p><a href="/documents/20124/938039/pd-rrf-mar2026.pdf">Reference Rate Framework</a></p></td>
<td class=" test"><div class="badge badge-info">Exposure Draft</div></td><td>2026</td></tr>
</tbody></table>`

func TestParseSector(t *testing.T) {
	docs := parseSector(sectorHTML, "https://www.bnm.gov.my", "/banking-islamic-banking", map[string]bool{})
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2", len(docs))
	}

	rmit := docs[0]
	if rmit.Title != "Risk Management in Technology (RMiT)" {
		t.Fatalf("rmit title = %q", rmit.Title)
	}
	if rmit.ExternalID != "/documents/20124/938039/pd-rmit-nov25.pdf" {
		t.Fatalf("rmit external id = %q", rmit.ExternalID)
	}
	if rmit.Number != "BNM/pd-rmit-nov25" { // carries the bnm signal
		t.Fatalf("rmit number = %q, want BNM/pd-rmit-nov25", rmit.Number)
	}
	if rmit.IssuedAt != time.Date(2025, 11, 28, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("rmit issued = %v", rmit.IssuedAt)
	}
	if string(rmit.DocType) != "Policy Document" {
		t.Fatalf("rmit type = %q", rmit.DocType)
	}
	if len(rmit.Files) != 1 || rmit.Files[0].URL != "https://www.bnm.gov.my/documents/20124/938039/pd-rmit-nov25.pdf" {
		t.Fatalf("rmit file = %+v", rmit.Files)
	}

	rrf := docs[1]
	if rrf.ExternalID != "/documents/20124/938039/pd-rrf-mar2026.pdf" || rrf.Number != "BNM/pd-rrf-mar2026" {
		t.Fatalf("rrf id/number = %q / %q", rrf.ExternalID, rrf.Number)
	}
	if rrf.Files[0].URL != "https://www.bnm.gov.my/documents/20124/938039/pd-rrf-mar2026.pdf" {
		t.Fatalf("rrf url (relative not absolutized) = %q", rrf.Files[0].URL)
	}
	if string(rrf.DocType) != "Exposure Draft" {
		t.Fatalf("rrf type = %q", rrf.DocType)
	}
}
