package backup

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestParseDatabasesReadsTheRegistry(t *testing.T) {
	databases, err := ParseDatabases(strings.NewReader(`# service|database|role|password environment variable
finance|finance_db|finance_user|LIFE_OS_FINANCE_DB_PASSWORD

  calendar-feeds|calendar_feeds_db|calendar_feeds_user|LIFE_OS_CALENDAR_FEEDS_DB_PASSWORD
# food|food_db|food_user|LIFE_OS_FOOD_DB_PASSWORD
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Database{{Service: "finance", Name: "finance_db"}, {Service: "calendar-feeds", Name: "calendar_feeds_db"}}
	if !slices.Equal(databases, want) {
		t.Fatalf("ParseDatabases = %+v, want %+v", databases, want)
	}
}

func TestParseDatabasesRejectsWhatProvisioningRejects(t *testing.T) {
	for name, line := range map[string]string{
		"too few fields":   "finance|finance_db|finance_user",
		"too many fields":  "finance|finance_db|finance_user|LIFE_OS_FINANCE_DB_PASSWORD|extra",
		"empty field":      "finance||finance_user|LIFE_OS_FINANCE_DB_PASSWORD",
		"bad service":      "Finance|finance_db|finance_user|LIFE_OS_FINANCE_DB_PASSWORD",
		"bad database":     "finance|finance-db|finance_user|LIFE_OS_FINANCE_DB_PASSWORD",
		"bad role":         "finance|finance_db|finance user|LIFE_OS_FINANCE_DB_PASSWORD",
		"bad password var": "finance|finance_db|finance_user|FINANCE_PASSWORD",
	} {
		if _, err := ParseDatabases(strings.NewReader(line + "\n")); err == nil {
			t.Errorf("%s: %q accepted", name, line)
		}
	}
	_, err := ParseDatabases(strings.NewReader("a|same_db|a_user|LIFE_OS_A_DB_PASSWORD\nb|same_db|b_user|LIFE_OS_B_DB_PASSWORD\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a database listed twice: error = %v", err)
	}
}

// The repository's own registry parses, so every service in it is backed up.
func TestLoadDatabasesReadsTheRepositoryRegistry(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	databases, err := LoadDatabases(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "infra", "postgres", "service-databases.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(databases) < 10 || databases[0] != (Database{Service: "finance", Name: "finance_db"}) {
		t.Fatalf("LoadDatabases = %+v", databases)
	}
}

func TestSelectPicksDatabasesByServiceOrDatabaseName(t *testing.T) {
	registry := []Database{{"finance", "finance_db"}, {"tasks", "tasks_db"}, {"calendar-feeds", "calendar_feeds_db"}}
	if selected, unknown := Select(registry, nil); len(selected) != 3 || unknown != nil {
		t.Errorf("Select(all) = %v, %v", selected, unknown)
	}
	selected, unknown := Select(registry, []string{"calendar_feeds_db", "finance", "finance_db"})
	if want := []Database{{"finance", "finance_db"}, {"calendar-feeds", "calendar_feeds_db"}}; !slices.Equal(selected, want) || unknown != nil {
		t.Errorf("Select = %v, %v; want registry order without repeats", selected, unknown)
	}
	if _, unknown := Select(registry, []string{"tasks", "food", "food"}); !slices.Equal(unknown, []string{"food"}) {
		t.Errorf("unknown = %v", unknown)
	}
}
