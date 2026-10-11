package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestCompareListsEveryDifference(t *testing.T) {
	manifest := DatabaseResult{
		Name:      "finance_db",
		Tables:    []TableRows{{"public", "accounts", 6}, {"public", "transactions", 1834}, {"public", "budgets", 1}},
		Migration: &Migration{Version: 14},
	}
	if got := compare(manifest, []TableRows{{"public", "accounts", 6}, {"public", "budgets", 1}, {"public", "transactions", 1834}}, &Migration{Version: 14}); len(got) != 0 {
		t.Fatalf("an identical restore differs: %+v", got)
	}
	got := compare(manifest, []TableRows{{"public", "accounts", 5}, {"public", "budgets", 1}, {"extra", "leftover", 2}}, &Migration{Version: 14, Dirty: true})
	want := []Mismatch{
		{Subject: "public.accounts", Expected: "6 rows", Actual: "5 rows"},
		{Subject: "public.transactions", Expected: "1834 rows", Actual: "missing"},
		{Subject: "extra.leftover", Expected: "absent", Actual: "2 rows"},
		{Subject: "migration", Expected: "version 14", Actual: "version 14 (dirty)"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("compare = %+v\nwant %+v", got, want)
	}
	if got := compare(DatabaseResult{Tables: []TableRows{{"public", "t", 1}}}, []TableRows{{"public", "t", 1}}, &Migration{Version: 2}); !slices.Equal(got, []Mismatch{{Subject: "migration", Expected: "none", Actual: "version 2"}}) {
		t.Fatalf("a migration the manifest lacks: %+v", got)
	}
}

func TestVerifyFileChecksSizeAndSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "finance_db.dump")
	data := []byte("PGDMP and the rest of a dump")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	if err := verifyFile(path, int64(len(data)), strings.ToUpper(good)); err != nil {
		t.Fatalf("verifyFile = %v", err)
	}
	if err := verifyFile(path, int64(len(data))+1, good); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("a size mismatch: %v", err)
	}
	if err := verifyFile(path, int64(len(data)), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("a checksum mismatch: %v", err)
	}
	if err := verifyFile(path+".missing", 1, good); err == nil {
		t.Error("a missing dump verified")
	}
}

func TestConnectErrorSaysWhatToDo(t *testing.T) {
	for code, want := range map[string]string{
		"42501": "lifeos_backup cannot connect to food_db: run make db-provision-backup",
		"3D000": "food_db does not exist: provision it with make db-provision",
		"28P01": "lifeos_backup cannot log in: set LIFE_OS_BACKUP_DB_PASSWORD",
	} {
		err := connectError("lifeos_backup", "food_db", fmt.Errorf("failed to connect: %w", &pgconn.PgError{Code: code, Message: "server says no"}))
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "server says no") {
			t.Errorf("%s: %v", code, err)
		}
	}
	if err := connectError("lifeos_backup", "food_db", errors.New("dial tcp: no route")); !strings.Contains(err.Error(), "connect to food_db: dial tcp") {
		t.Errorf("a network error: %v", err)
	}
}

func TestParseCheckDay(t *testing.T) {
	for value, want := range map[string]time.Weekday{"sunday": time.Sunday, " Monday ": time.Monday, "SATURDAY": time.Saturday} {
		if day, enabled, err := ParseCheckDay(value); err != nil || !enabled || day != want {
			t.Errorf("ParseCheckDay(%q) = %s, %v, %v", value, day, enabled, err)
		}
	}
	if _, enabled, err := ParseCheckDay("off"); err != nil || enabled {
		t.Errorf("off = %v, %v", enabled, err)
	}
	for _, value := range []string{"", "sun", "weekly", "7"} {
		if _, _, err := ParseCheckDay(value); err == nil {
			t.Errorf("ParseCheckDay(%q) accepted", value)
		}
	}
	if name := ScratchName("google_calendar_db"); name != "lifeos_restore_check_google_calendar_db" {
		t.Errorf("ScratchName = %s", name)
	}
}
