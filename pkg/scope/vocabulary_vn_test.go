package scope

import (
	"encoding/csv"
	"os"
	"testing"
)

// TestVNScopeVocabularyQueryCoverage pins the VN seed vocabulary against
// realistic compliance-officer queries over the SBV take-all topics
// (2026-07-24 expansion): banking-scoped queries must resolve in scope at
// query time, and generic cross-sector queries without a banking signal must
// abstain — the weak-class signal gate is the precision guard.
func TestVNScopeVocabularyQueryCoverage(t *testing.T) {
	f, err := os.Open("../../deploy/seed/scope_term.csv")
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	var terms []Term
	for _, r := range rows[1:] {
		terms = append(terms, Term{Text: r[0], Class: r[1]})
	}
	m := Load(terms)

	inScope := []string{
		"thời hạn lưu trữ hồ sơ tài liệu ngành ngân hàng",
		"tỷ lệ dự trữ bắt buộc đối với tiền gửi ngoại tệ của tổ chức tín dụng",
		"lãi suất tái cấp vốn của ngân hàng nhà nước",
		"điều kiện kinh doanh vàng miếng",
		"huy động và cho vay vốn bằng vàng của tổ chức tín dụng",
		"điều kiện cấp giấy phép thành lập ngân hàng thương mại cổ phần",
		"kiểm toán độc lập đối với tổ chức tín dụng",
		"tỷ lệ an toàn vốn tối thiểu của ngân hàng",
		"phân loại nợ và trích lập dự phòng rủi ro tín dụng của ngân hàng",
		"hệ thống tài khoản kế toán các tổ chức tín dụng",
		"chế độ báo cáo thống kê áp dụng đối với tổ chức tín dụng",
		"quy định về bảo lãnh ngân hàng",
		"phát hành chứng chỉ tiền gửi của tổ chức tín dụng",
		"vốn điều lệ của chi nhánh ngân hàng nước ngoài",
		"xếp hạng tổ chức tài chính vi mô",
		"hạn mức trả tiền bảo hiểm tiền gửi",
	}
	for _, q := range inScope {
		if r := m.MatchQuery(q); !r.InScope {
			t.Errorf("query %q: want in scope, got abstain", q)
		}
	}

	abstain := []string{
		"lãi suất chậm nộp thuế",
		"cấp giấy phép xây dựng nhà ở",
		"giấy phép lái xe hạng B2",
		"kiểm toán độc lập công ty niêm yết",
		"chế độ báo cáo thống kê ngành nông nghiệp",
		"tài khoản kế toán doanh nghiệp sản xuất",
		"phân loại nợ công quốc gia",
		"lưu trữ hồ sơ bệnh án",
		"lưu trữ tài liệu cơ quan nhà nước",
		"hạch toán chi phí sản xuất kinh doanh",
		"dự phòng rủi ro hợp đồng bảo hiểm nhân thọ",
		"giá vàng sjc hôm nay",
		"trang sức vàng bạc đá quý",
	}
	for _, q := range abstain {
		if r := m.MatchQuery(q); r.InScope {
			t.Errorf("query %q: want abstain, got in scope via %v", q, r.Matched)
		}
	}
}
