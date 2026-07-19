package pipeline

import dbsilver "danny.vn/banhmi/pkg/store/silver"

func silverSectionRows(rows []dbsilver.ListSectionsByDocumentRow) []dbsilver.SilverDocumentSection {
	out := make([]dbsilver.SilverDocumentSection, 0, len(rows))
	for _, row := range rows {
		out = append(out, dbsilver.SilverDocumentSection(row))
	}
	return out
}
