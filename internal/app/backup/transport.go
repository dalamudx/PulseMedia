package backup

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"net"
	"net/url"
	"strings"
)

// connectionOptions preserves explicit libpq transport options, including CA,
// client certificate, host identity and sslmode, without exposing passwords.
func connectionOptions(c *pgx.ConnConfig) (map[string]string, error) {
	raw := c.ConnString()
	out := map[string]string{}
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, e := url.Parse(raw)
		if e != nil {
			return nil, e
		}
		for k, v := range u.Query() {
			if len(v) != 1 {
				return nil, errors.New("duplicate connection option")
			}
			out[k] = v[0]
		}
		if out["host"] == "" && u.Host != "" {
			var hosts, ports []string
			for _, entry := range strings.Split(u.Host, ",") {
				host, port, err := net.SplitHostPort(entry)
				if err != nil {
					host = strings.Trim(entry, "[]")
					port = fmtPort(c.Port)
				}
				hosts = append(hosts, host)
				ports = append(ports, port)
			}
			out["host"] = strings.Join(hosts, ",")
			if out["port"] == "" {
				out["port"] = strings.Join(ports, ",")
			}
		}
	} else {
		for s := strings.TrimSpace(raw); s != ""; {
			i := strings.IndexByte(s, '=')
			if i < 1 {
				return nil, errors.New("invalid connection option")
			}
			k := strings.TrimSpace(s[:i])
			s = strings.TrimLeft(s[i+1:], " \t")
			var b strings.Builder
			quoted := strings.HasPrefix(s, "'")
			if quoted {
				s = s[1:]
			}
			closed := !quoted
			for len(s) > 0 {
				ch := s[0]
				s = s[1:]
				if ch == '\\' {
					if len(s) == 0 {
						return nil, errors.New("invalid connection escape")
					}
					b.WriteByte(s[0])
					s = s[1:]
					continue
				}
				if quoted && ch == '\'' {
					closed = true
					break
				}
				if !quoted && (ch == ' ' || ch == '\t') {
					break
				}
				b.WriteByte(ch)
			}
			if !closed {
				return nil, errors.New("unterminated connection option")
			}
			out[k] = b.String()
			s = strings.TrimSpace(s)
		}
	}
	delete(out, "password")
	if out["service"] != "" || out["servicefile"] != "" {
		return nil, errors.New("connection file must contain explicit transport settings")
	}
	if out["sslmode"] == "" && c.TLSConfig != nil {
		return nil, errors.New("explicit sslmode required for TLS subprocess consistency")
	}
	if out["host"] == "" {
		out["host"] = c.Host
	}
	if out["port"] == "" {
		out["port"] = fmtPort(c.Port)
	}
	out["user"] = c.User
	out["dbname"] = c.Database
	for k, v := range out {
		if strings.ContainsAny(k+v, "\r\n") || strings.ContainsAny(k, "=[]") {
			return nil, errors.New("unsafe service option")
		}
	}
	return out, nil
}
func fmtPort(p uint16) string {
	const digits = "0123456789"
	if p == 0 {
		return "0"
	}
	var b [5]byte
	i := len(b)
	for p > 0 {
		i--
		b[i] = digits[p%10]
		p /= 10
	}
	return string(b[i:])
}
func targetConfig(c *pgx.ConnConfig, database string) (*pgx.ConnConfig, error) {
	raw := c.ConnString()
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, e := url.Parse(raw)
		if e != nil {
			return nil, e
		}
		u.Path = "/" + database
		u.RawPath = ""
		q := u.Query()
		q.Del("dbname")
		u.RawQuery = q.Encode()
		return pgx.ParseConfig(u.String())
	}
	return pgx.ParseConfig(raw + " dbname=" + quote(database))
}
