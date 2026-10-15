package intake

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func TestAcceptanceConversionCharset(t *testing.T) {
	for _, media := range []string{"text/html", "Text/HTML; charset=UTF-8", "text/html; charset=UtF8", "text/html; charset=iso-8859-1"} {
		t.Run(media, func(t *testing.T) {
			f := newAcceptanceFixtureWith(t, model.QualityQuarantined, "synthetic charset", media)
			attachAcceptanceConversion(t, f, "", "")
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			if media != "text/html; charset=iso-8859-1" {
				if err != nil {
					t.Fatal("supported charset rejected", err)
				}
				return
			}
			requireAcceptanceCause(t, err, "acceptance detector unsupported charset")
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("unsupported charset changed acceptance rows")
			}
		})
	}
}

func TestAcceptanceConversionLocalBounds(t *testing.T) {
	for _, name := range []string{"input-limit", "input-bound", "span-limit", "span-bound"} {
		t.Run(name, func(t *testing.T) {
			f := newAcceptanceFixture(t, model.QualityQuarantined)
			attachAcceptanceConversion(t, f, "", name)
			before := acceptanceSnapshot(t, f)
			_, err := f.adapter.Apply(f.db.ctx, f.request)
			if strings.HasSuffix(name, "-limit") {
				if err != nil {
					t.Fatal("conversion at its local bound rejected", err)
				}
				return
			}
			requireAcceptanceCause(t, err, "retained evidence differs from its deterministic projection")
			if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
				t.Fatal("conversion-local overflow changed acceptance rows")
			}
		})
	}
}

func TestAcceptanceFailedMapDecodeStopsBeforeLedgerOpen(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	doc, _, _ := attachAcceptanceConversion(t, f, "", "json-charge-alias")
	before := acceptanceSnapshot(t, f)
	f.objects.opened = nil
	_, err := f.adapter.Apply(f.db.ctx, f.request)
	requireAcceptanceCause(t, err, "catalog reference mismatch")
	for _, path := range f.objects.opened {
		if path == "acceptance/"+string(doc.EditLedger.SHA256) {
			t.Fatal("failed map decode opened the larger ledger alias")
		}
	}
	if !reflect.DeepEqual(before, acceptanceSnapshot(t, f)) {
		t.Fatal("failed map decode changed acceptance rows")
	}
}

func TestAcceptanceFailedJSONAliasCannotPoisonCharge(t *testing.T) {
	f := newAcceptanceFixture(t, model.QualityQuarantined)
	ref := catalogRef(f.register(t, acceptanceJSON(t, struct{ Value string }{"synthetic ledger bytes"})))
	v := newAcceptanceEvidence(f.adapter, nil, intakedb.New(f.app), f.request, time.Now())
	v.limits.MaxJSONBytes = ref.ByteSize - 1
	alias := ref
	alias.ByteSize = 1
	f.objects.opened = nil
	_, err := v.jsonObject(f.db.ctx, alias)
	requireAcceptanceCause(t, err, "catalog reference mismatch")
	if len(v.jsonSeen) != 0 || v.jsonBytes != 0 {
		t.Fatal("failed catalog validation cached a JSON charge")
	}
	_, err = v.jsonObject(f.db.ctx, ref)
	requireAcceptanceCause(t, err, "acceptance work exceeds pinned bounds")
	if len(f.objects.opened) != 0 {
		t.Fatal("failed alias bypassed the larger object's JSON bound")
	}
	v.limits.MaxJSONBytes = ref.ByteSize
	if _, err := v.jsonObject(f.db.ctx, ref); err != nil {
		t.Fatal(err)
	}
	alias.Path, alias.ByteSize = "logical/conflicting-alias", ref.ByteSize+1
	f.objects.opened = nil
	_, err = v.jsonObject(f.db.ctx, alias)
	requireAcceptanceCause(t, err, "logical aliases disagree on size")
	if len(f.objects.opened) != 0 || v.jsonBytes != ref.ByteSize {
		t.Fatal("conflicting JSON alias read or changed the charged size")
	}
}
