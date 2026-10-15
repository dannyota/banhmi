package intake

import "testing"

func TestAdmissionSchemaRuntimePrivileges(t *testing.T) {
	f := newAdmissionFixture(t, true)
	f.reserve(t)
	ctx := f.source.db.ctx
	for _, table := range []string{"document_admission_limit", "document_admission_slot", "document_admission_receipt"} {
		for _, role := range []string{"banhmi_corpus_writer_runtime", "banhmi_corpus_writer_export"} {
			for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
				var granted bool
				if err := f.source.app.QueryRow(ctx, `SELECT has_table_privilege($1,$2,$3)`, role, "ingest."+table, privilege).Scan(&granted); err != nil {
					t.Fatal(err)
				}
				want := role == "banhmi_corpus_writer_runtime" && (privilege == "SELECT" || privilege == "INSERT")
				if granted != want {
					t.Fatalf("%s %s %s=%v", role, privilege, table, granted)
				}
			}
		}
		var sequence bool
		if err := f.source.app.QueryRow(ctx, `SELECT has_sequence_privilege('banhmi_corpus_writer_runtime',pg_get_serial_sequence($1,'id'),'USAGE')`, "ingest."+table).Scan(&sequence); err != nil || !sequence {
			t.Fatal("runtime lacks admission sequence usage")
		}
	}
}

func TestAdmissionSchemaFrozenRowsAndUpdateShapes(t *testing.T) {
	f := newAdmissionFixture(t, true)
	reserved := f.reserve(t)
	for _, statement := range []string{
		`DELETE FROM ingest.document_admission_limit`,
		`DELETE FROM ingest.document_admission_slot`,
		`DELETE FROM ingest.document_admission_receipt`,
		`UPDATE ingest.document_admission_receipt SET response='{}'`,
		`UPDATE ingest.document_admission_slot SET workflow_id='document-ingest/vn/other/attempt-a',revision=revision+1`,
		`UPDATE ingest.document_admission_slot SET revision=revision+2`,
		`UPDATE ingest.document_admission_slot SET settled_watermark=-1,revision=revision+1`,
		`UPDATE ingest.document_admission_slot SET state='open_only',revision=revision+1`,
		`UPDATE ingest.document_admission_slot SET terminal_reason='abandoned',revision=revision+1`,
		`UPDATE ingest.document_admission_slot SET wait_reason='other',revision=revision+1`,
		`UPDATE ingest.document_admission_limit SET revision=revision+2`,
		`UPDATE ingest.document_admission_limit SET next_queue='resume',revision=revision+1`,
		`UPDATE ingest.document_admission_limit SET processing_limit=0,revision=revision+1`,
		`UPDATE ingest.document_admission_limit SET open_limit=1,revision=revision+1`,
		`UPDATE ingest.document_admission_limit SET receipt_json_bytes=0,revision=revision+1`,
		`UPDATE ingest.document_admission_limit SET quarantine_expiry_us=9223372036854776,revision=revision+1`,
		`UPDATE ingest.document_admission_limit SET next_queue='other'`,
	} {
		before := admissionRows(t, f)
		tx, err := f.source.db.conn.Begin(f.source.db.ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, mutationErr := tx.Exec(f.source.db.ctx, statement)
		if err := tx.Rollback(f.source.db.ctx); err != nil {
			t.Fatal(err)
		}
		if mutationErr == nil {
			t.Fatalf("invalid shape succeeded: %s", statement)
		}
		requireAdmissionUnchanged(t, f, before)
	}
	// Configuration and turn changes each have one permitted update shape.
	tx, err := f.source.app.Begin(f.source.db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.source.db.ctx, `UPDATE ingest.document_admission_limit SET next_queue='resume'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.source.db.ctx, `UPDATE ingest.document_admission_limit SET revision=revision+1,processing_limit=1,open_limit=1,updated_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(f.source.db.ctx); err != nil {
		t.Fatal(err)
	}
	f.status = "terminal"
	closing, err := f.adapter.Reconcile(f.source.db.ctx, admissionChange(f, reserved, true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.Reconcile(f.source.db.ctx, admissionChange(f, closing, true)); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	if _, err := f.source.app.Exec(f.source.db.ctx, `UPDATE ingest.document_admission_slot SET revision=revision+1`); err == nil {
		t.Fatal("released tombstone was mutable")
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionSchemaIndexesAndSingleton(t *testing.T) {
	f := newAdmissionFixture(t, false)
	var revision int64
	var queue string
	var unconfigured bool
	if err := f.source.app.QueryRow(f.source.db.ctx, `SELECT revision,next_queue,
processing_limit IS NULL AND open_limit IS NULL AND receipt_json_bytes IS NULL AND quarantine_expiry_us IS NULL
FROM ingest.document_admission_limit WHERE id=1`).Scan(&revision, &queue, &unconfigured); err != nil || revision != 0 || queue != "launch" || !unconfigured {
		t.Fatal("singleton was not seeded fail-closed")
	}
	for _, index := range []string{"document_admission_live_version_idx", "document_admission_scan_idx", "document_admission_resume_idx",
		"document_admission_origin_idx", "document_admission_current_idx", "document_admission_terminal_idx", "document_admission_launch_idx",
		"document_admission_receipt_slot_idx", "document_admission_receipt_actor_idx"} {
		var valid bool
		if err := f.source.app.QueryRow(f.source.db.ctx, `SELECT i.indisvalid AND i.indisready FROM pg_index i WHERE i.indexrelid=$1::regclass`, "ingest."+index).Scan(&valid); err != nil || !valid {
			t.Fatalf("missing admission index %s", index)
		}
	}
	// Both singleton CHECK branches explicitly reject partial NULL bounds.
	for i, expression := range []string{"processing_limit=1", "revision=1", "revision=1,processing_limit=1,open_limit=2,receipt_json_bytes=1"} {
		tx, err := f.source.db.conn.Begin(f.source.db.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.source.db.ctx, "ALTER TABLE ingest.document_admission_limit DISABLE TRIGGER document_admission_limit_frozen"); err != nil {
			t.Fatal(err)
		}
		_, mutationErr := tx.Exec(f.source.db.ctx, "UPDATE ingest.document_admission_limit SET "+expression)
		if err := tx.Rollback(f.source.db.ctx); err != nil {
			t.Fatal(err)
		}
		if mutationErr == nil {
			t.Fatalf("singleton NULL branch %d accepted", i)
		}
	}
}
