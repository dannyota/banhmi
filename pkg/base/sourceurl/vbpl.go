// Package sourceurl contains pure official-source URL builders shared by
// serving and corpus ingestion.
package sourceurl

import "net/url"

const vbplAPIBase = "https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public"

// VBPLFilesListingURL returns the stable official file-listing endpoint for one
// opaque VBPL document identifier.
func VBPLFilesListingURL(externalID string) string {
	return vbplAPIBase + "/doc/minio/buckets/vbpl/folders/" + url.PathEscape(externalID) + "/files?parts=1,2,3,4,5"
}
