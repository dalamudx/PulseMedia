package migration

import (
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/app/backup"
	"github.com/jackc/pgx/v5"
)

// connection accepts an explicit PostgreSQL URI. Inherited service selection is
// rejected; other libpq environment/home settings are shadowed before pgx parses.
// The returned URI is private and must never be logged or passed in argv.
func connection(path string) (string, error) {
	// pgx interprets even an empty service option as a service lookup. Refuse
	// inherited service selection before the driver can read a service file.
	if os.Getenv("PGSERVICE") != "" {
		return "", ErrConnection
	}
	b, err := backup.ReadPrivate(path)
	if err != nil {
		return "", ErrConnection
	}
	u, err := url.Parse(strings.TrimSpace(string(b)))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Fragment != "" || u.Opaque != "" {
		return "", ErrConnection
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrConnection
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("host port user password dbname sslmode sslrootcert sslcert sslkey sslpassword sslsni sslnegotiation connect_timeout target_session_attrs min_protocol_version max_protocol_version channel_binding require_auth") {
		allowed[k] = true
	}
	for k, values := range q {
		if !allowed[k] || len(values) != 1 || strings.ContainsAny(values[0], "\x00\r\n") {
			return "", ErrConnection
		}
	}
	// A single endpoint is intentional: failover is an operator decision offline.
	host := q.Get("host")
	if host == "" {
		host = u.Hostname()
	}
	user := q.Get("user")
	if user == "" && u.User != nil {
		user = u.User.Username()
	}
	database := q.Get("dbname")
	if database == "" {
		database = strings.TrimPrefix(u.Path, "/")
	}
	if host == "" || strings.Contains(host, ",") || user == "" || database == "" || strings.Contains(database, "/") {
		return "", ErrConnection
	}
	mode := q.Get("sslmode")
	local := strings.HasPrefix(host, "/") || host == "localhost" || host == "127.0.0.1" || host == "::1"
	if mode != "verify-full" && (mode != "disable" || !local) {
		return "", ErrConnection
	}
	if q.Get("port") == "" {
		q.Set("port", u.Port())
	}
	if q.Get("port") == "" {
		q.Set("port", "5432")
	}
	if strings.Contains(q.Get("port"), ",") {
		return "", ErrConnection
	}
	password := q.Get("password")
	if _, ok := q["password"]; !ok && u.User != nil {
		password, _ = u.User.Password()
	}
	q.Set("host", host)
	q.Set("user", user)
	q.Set("dbname", database)
	q.Set("password", password)
	// Explicit empties shadow both environment and ~/.pg* defaults. Do not read
	// service/pass files or auto-discover certificates from the user's home.
	for _, k := range strings.Fields("passfile servicefile sslcert sslkey sslrootcert sslpassword options min_protocol_version max_protocol_version require_auth") {
		if _, ok := q[k]; !ok {
			q.Set(k, "")
		}
	}
	for k, v := range map[string]string{"connect_timeout": "5", "sslsni": "1", "sslnegotiation": "postgres", "target_session_attrs": "any", "channel_binding": "prefer"} {
		if _, ok := q[k]; !ok {
			q.Set(k, v)
		}
	}
	q.Set("application_name", "pulsemigrate")
	q.Set("timezone", "UTC")
	// PG18 must notice a canceled client's closed socket during long DDL,
	// rather than retaining its migration session lock until the query ends.
	q.Set("client_connection_check_interval", "100ms")
	// Keyword form preserves spaces/plus signs and explicit empty settings;
	// only the private file accepts user input, never command arguments.
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, k := range keys {
		canonical.WriteString(k + "='" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(q.Get(k)) + "' ")
	}
	dsn := canonical.String()
	c, err := pgx.ParseConfig(dsn)
	if err != nil || len(c.Fallbacks) != 0 || (mode == "verify-full" && c.TLSConfig == nil) {
		return "", ErrConnection
	}
	if c.Host != host || c.User != user || c.Database != database || c.Password != password {
		return "", errors.New("explicit connection settings required")
	}
	return dsn, nil
}
