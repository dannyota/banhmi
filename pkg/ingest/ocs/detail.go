package ocs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

const getLawDocPath = "/ocs-api/public/doc/getLawDoc"

// getLawDocRequest is the request body for the getLawDoc API.
type getLawDocRequest struct {
	ReqHeader reqHeader `json:"reqHeader"`
	ReqBody   reqBody   `json:"reqBody"`
}

type reqHeader struct {
	ReqID       string `json:"reqId"`
	ReqChannel  string `json:"reqChannel"`
	ReqDtm      string `json:"reqDtm"`
	ReqBy       string `json:"reqBy"`
	ServiceName string `json:"serviceName"`
	UUID        string `json:"uuid"`
	SessionID   string `json:"sessionId"`
}

type reqBody struct {
	TimelineID string `json:"timelineId"`
}

// getLawDocResponse is the response envelope from the getLawDoc API.
type getLawDocResponse struct {
	RespBody struct {
		LawInfo     lawInfo      `json:"lawInfo"`
		LawSections []lawSection `json:"lawSections"`
	} `json:"respBody"`
}

// lawInfo carries document-level metadata from the getLawDoc response.
type lawInfo struct {
	LawNameTh            string `json:"lawNameTh"`
	PublishDateAd        string `json:"publishDateAd"`        // "YYYY-MM-DD" or ""
	EffectiveDateStartAd string `json:"effectiveDateStartAd"` // "YYYY-MM-DD" or ""
	StateID              string `json:"stateId"`              // "01" = in_force, "02" = repealed
}

// lawSection is one structured section from the getLawDoc response.
type lawSection struct {
	SectionTypeID  json.Number `json:"sectionTypeId"` // 4=มาตรา, 8=หมวด, 9=ส่วน (string or int)
	SectionNo      string      `json:"sectionNo"`
	SectionLabel   string      `json:"sectionLabel"`
	SectionContent string      `json:"sectionContent"` // HTML content
}

func (s lawSection) typeID() int {
	n, _ := s.SectionTypeID.Int64()
	return int(n)
}

// Section type constants from the OCS API.
const (
	sectionTypeMatra   = 4 // มาตรา (Section/Article)
	sectionTypeChapter = 8 // หมวด (Chapter)
	sectionTypePart    = 9 // ส่วน (Part)
)

// FetchDetail calls the getLawDoc API to retrieve the full text of an Act.
// The encTimelineID is extracted from the DetailURL query parameter "tid"
// (appended during discovery).
func (s *Source) FetchDetail(ctx context.Context, ref ingest.DetailRef) (*ingest.DiscoveredDoc, error) {
	if ref.ExternalID == "" {
		return nil, fmt.Errorf("ocs fetch detail: empty external id")
	}

	doc := &ingest.DiscoveredDoc{
		SourceID:   SourceID,
		ExternalID: ref.ExternalID,
		DocType:    "พระราชบัญญัติ",
		DetailURL:  ref.DetailURL,
	}

	tid := extractTimelineID(ref.DetailURL)
	if tid == "" {
		s.log.Warn("ocs fetch detail: no timeline id in detail url", "doc", ref.ExternalID)
		return doc, nil
	}

	body, err := s.fetchLawDoc(ctx, tid)
	if err != nil {
		return nil, fmt.Errorf("ocs fetch detail %s: %w", ref.ExternalID, err)
	}

	var resp getLawDocResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("ocs fetch detail %s: parse response: %w", ref.ExternalID, err)
	}

	// Extract metadata from lawInfo.
	info := resp.RespBody.LawInfo
	if info.LawNameTh != "" {
		doc.Title = info.LawNameTh
	}
	if t := parseAdDate(info.PublishDateAd); !t.IsZero() {
		doc.IssuedAt = t
	}
	if t := parseAdDate(info.EffectiveDateStartAd); !t.IsZero() {
		doc.EffectiveAt = t
	}
	if st := mapState(info.StateID); st != "" {
		doc.Status = st
	}

	// Build HTML body from sections.
	html := buildSectionsHTML(resp.RespBody.LawSections)
	if html != "" {
		doc.HTML = html
	}

	s.log.Info("ocs fetch detail done", "doc", ref.ExternalID, "sections", len(resp.RespBody.LawSections), "html_len", len(html))
	return doc, nil
}

// extractTimelineID parses the "tid" query parameter from a detail URL.
func extractTimelineID(detailURL string) string {
	if detailURL == "" {
		return ""
	}
	u, err := url.Parse(detailURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("tid")
}

// fetchLawDoc calls the getLawDoc POST API and returns the raw response body.
func (s *Source) fetchLawDoc(ctx context.Context, timelineID string) ([]byte, error) {
	now := time.Now().UTC()
	reqPayload := getLawDocRequest{
		ReqHeader: reqHeader{
			ReqID:       fmt.Sprintf("%d", now.UnixMilli()),
			ReqChannel:  "WEB",
			ReqDtm:      now.Format("2006-01-02 15:04:05.000"),
			ReqBy:       "unknow",
			ServiceName: "getPublicLawDoc",
			UUID:        "banhmi",
			SessionID:   "banhmi",
		},
		ReqBody: reqBody{
			TimelineID: timelineID,
		},
	}

	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	return s.post(ctx, s.textBaseURL+getLawDocPath, body)
}

// buildSectionsHTML converts lawSections into a structured HTML string.
func buildSectionsHTML(sections []lawSection) string {
	if len(sections) == 0 {
		return ""
	}

	var b strings.Builder
	for _, sec := range sections {
		content := strings.TrimSpace(sec.SectionContent)
		if content == "" {
			continue
		}

		switch sec.typeID() {
		case sectionTypePart: // ส่วน (Part)
			label := sec.SectionLabel
			if label == "" {
				label = "ส่วน " + sec.SectionNo
			}
			fmt.Fprintf(&b, "<h2>%s</h2>\n", label)
		case sectionTypeChapter: // หมวด (Chapter)
			label := sec.SectionLabel
			if label == "" {
				label = "หมวด " + sec.SectionNo
			}
			fmt.Fprintf(&b, "<h3>%s</h3>\n", label)
		case sectionTypeMatra: // มาตรา (Section/Article)
			label := sec.SectionLabel
			if label == "" {
				label = "มาตรา " + sec.SectionNo
			}
			fmt.Fprintf(&b, "<h4>%s</h4>\n", label)
		default:
			// Unknown section type — include as-is with a data attribute.
		}
		fmt.Fprintf(&b, "%s\n", content)
	}

	return b.String()
}

// parseAdDate parses a "YYYY-MM-DD" date string (Gregorian / AD calendar).
func parseAdDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
