package emailplatform

import (
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Hostnames as net/url reports them; plain http is allowed only for these.
var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// parseBaseURL validates the configured base URL and returns it without a
// trailing slash. Errors never repeat the value: it may carry credentials.
func parseBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", invalid("base URL is required")
	}
	// Checked on the text: net/url drops an empty fragment.
	if strings.ContainsAny(raw, "?#") {
		return "", invalid("base URL must not contain a query or fragment")
	}
	if strings.Contains(raw, "@") {
		return "", invalid("base URL must not contain credentials")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalid("base URL is not a valid URL")
	}
	switch {
	case u.Opaque != "" || u.Host == "" || u.Hostname() == "":
		return "", invalid("base URL must be an absolute URL with a host")
	case u.Scheme == "http":
		if !loopbackHosts[u.Hostname()] {
			return "", invalid("base URL must use https (plain http only for localhost, 127.0.0.1 and [::1])")
		}
	case u.Scheme != "https":
		return "", invalid("base URL must use https")
	}
	prefix := u.EscapedPath()
	for strings.Contains(prefix, "//") {
		prefix = strings.ReplaceAll(prefix, "//", "/")
	}
	return u.Scheme + "://" + u.Host + strings.TrimSuffix(prefix, "/"), nil
}

// buildPath fills the {name} placeholders of template with values in order,
// each escaped as one path segment.
func buildPath(template string, values []string) (string, error) {
	var b strings.Builder
	rest := template
	for _, value := range values {
		start := strings.IndexByte(rest, '{')
		end := strings.IndexByte(rest, '}')
		if start < 0 || end < start {
			return "", invalid("too many path parameters")
		}
		name := rest[start+1 : end]
		// A dot segment would send the request to another operation's path.
		if value == "" || value == "." || value == ".." {
			return "", invalid("path parameter " + name + " must not be empty, . or ..")
		}
		b.WriteString(rest[:start])
		b.WriteString(url.PathEscape(value))
		rest = rest[end+1:]
	}
	if strings.IndexByte(rest, '{') >= 0 {
		return "", invalid("missing path parameter")
	}
	b.WriteString(rest)
	return b.String(), nil
}

var timeType = reflect.TypeFor[time.Time]()

// encodeQuery encodes a generated …Params struct (or a pointer to one, nil
// for none) by its form tags. Field kinds it does not know are an error,
// never skipped: a dropped filter would widen what a list returns.
func encodeQuery(params any) (url.Values, error) {
	values := url.Values{}
	v := reflect.ValueOf(params)
	if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return values, nil
	}
	v = reflect.Indirect(v)
	if v.Kind() != reflect.Struct {
		return nil, invalid("query parameters must be a struct")
	}
	for i := range v.NumField() {
		field := v.Type().Field(i)
		tag, ok := field.Tag.Lookup("form")
		name, _, _ := strings.Cut(tag, ",")
		if !ok || name == "" {
			return nil, invalid("query parameter field " + field.Name + " has no form tag")
		}
		value := v.Field(i)
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				continue
			}
			value = value.Elem()
		}
		if value.Kind() == reflect.Slice {
			for j := range value.Len() {
				s, err := queryValue(value.Index(j))
				if err != nil {
					return nil, invalid("query parameter " + name + ": " + err.Error())
				}
				values.Add(name, s)
			}
			continue
		}
		s, err := queryValue(value)
		if err != nil {
			return nil, invalid("query parameter " + name + ": " + err.Error())
		}
		values.Add(name, s)
	}
	return values, nil
}

type unsupportedKind struct{ kind string }

func (e unsupportedKind) Error() string { return "unsupported type " + e.kind }

func queryValue(v reflect.Value) (string, error) {
	if v.Type() == timeType {
		t, _ := reflect.TypeAssert[time.Time](v)
		return t.Format(time.RFC3339Nano), nil
	}
	kind := v.Kind()
	if kind == reflect.String {
		return v.String(), nil
	}
	if kind == reflect.Int || kind == reflect.Int8 || kind == reflect.Int16 || kind == reflect.Int32 || kind == reflect.Int64 {
		return strconv.FormatInt(v.Int(), 10), nil
	}
	if kind == reflect.Bool {
		return strconv.FormatBool(v.Bool()), nil
	}
	return "", unsupportedKind{kind.String()}
}
