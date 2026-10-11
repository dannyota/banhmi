package sourceurl

import "testing"

func TestVBPLFilesListingURL(t *testing.T) {
	got := VBPLFilesListingURL("id/with space")
	want := "https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public/doc/minio/buckets/vbpl/folders/id%2Fwith%20space/files?parts=1,2,3,4,5"
	if got != want {
		t.Fatalf("VBPLFilesListingURL() = %q, want %q", got, want)
	}
}
