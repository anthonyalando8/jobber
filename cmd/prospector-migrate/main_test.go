package main

import (
	"github.com/anthonyalando8/prospector/internal/store"
	"testing"
)

func TestRestoreFingerprintRejectsContentAndSchemaChanges(t *testing.T) {
	a := &store.SchemaReport{Tables: map[string]store.TableInventory{"draft": {Rows: 1, DataHash: "original"}}}
	b := &store.SchemaReport{Tables: map[string]store.TableInventory{"draft": {Rows: 1, DataHash: "changed"}}}
	if sameData(a, b) {
		t.Fatal("changed body accepted")
	}
	b.Tables["draft"] = a.Tables["draft"]
	if !sameData(a, b) {
		t.Fatal("matching snapshot rejected")
	}
	b.Tables["extra"] = store.TableInventory{}
	if sameData(a, b) {
		t.Fatal("extra table accepted")
	}
}

func TestScratchDSNPreservesParameters(t *testing.T) {
	got, err := databaseDSN("postgres://user:synthetic@localhost/original?sslmode=disable", "prospector_verify_123")
	if err != nil || got != "postgres://user:synthetic@localhost/prospector_verify_123?sslmode=disable" {
		t.Fatalf("unexpected scratch config")
	}
}
