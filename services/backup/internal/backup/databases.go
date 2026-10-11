package backup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Database is one service database to back up, from the database registry
// (infra/postgres/service-databases.conf).
type Database struct {
	Service string
	Name    string
}

var (
	serviceName  = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	sqlName      = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	passwordName = regexp.MustCompile(`^LIFE_OS_[A-Z0-9_]+_DB_PASSWORD$`)
)

// LoadDatabases reads the database registry at path.
func LoadDatabases(path string) ([]Database, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ParseDatabases(file)
}

// ParseDatabases reads the database registry: one
// service|database|role|password-variable row per service, with blank lines
// and lines starting with # ignored, under the rules
// infra/postgres/provision-service-databases.sh applies. Only the service
// and database names are used: the backup service logs in with its own
// role.
func ParseDatabases(r io.Reader) ([]Database, error) {
	var databases []Database
	seen := map[string]bool{}
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "|")
		if len(fields) != 4 || slices.Contains(fields, "") {
			return nil, fmt.Errorf("line %d: expected service|database|role|password_env", line)
		}
		service, database, role, password := fields[0], fields[1], fields[2], fields[3]
		switch {
		case !serviceName.MatchString(service):
			return nil, fmt.Errorf("line %d: invalid service name %q", line, service)
		case !sqlName.MatchString(database) || !sqlName.MatchString(role):
			return nil, fmt.Errorf("line %d: invalid database or role name for service %s", line, service)
		case !passwordName.MatchString(password):
			return nil, fmt.Errorf("line %d: invalid password variable for service %s", line, service)
		case seen[database]:
			return nil, fmt.Errorf("line %d: database %s is listed twice", line, database)
		}
		seen[database] = true
		databases = append(databases, Database{Service: service, Name: database})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return databases, nil
}

// Select picks the databases a run covers from the registry: all of them
// when names is empty, else those named by service or database name, in
// registry order. Names that match nothing are returned as unknown.
func Select(registry []Database, names []string) (selected []Database, unknown []string) {
	if len(names) == 0 {
		return registry, nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		found := false
		for _, database := range registry {
			if name == database.Name || name == database.Service {
				wanted[database.Name] = true
				found = true
			}
		}
		if !found && !slices.Contains(unknown, name) {
			unknown = append(unknown, name)
		}
	}
	for _, database := range registry {
		if wanted[database.Name] {
			selected = append(selected, database)
		}
	}
	return selected, unknown
}
