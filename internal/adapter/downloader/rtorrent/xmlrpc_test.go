package rtorrent

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestRPCRequestPreservesBinaryAndArguments(t *testing.T) {
	binary := []byte{'d', 0, 0xff, '<', '&', 'e'}
	command := `d.directory.set="/downloads/中文 & title"`
	body, err := encodeRPCRequest("load.raw_start", "", binary, command, int64(1)<<40)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		XMLName xml.Name
		Method  string `xml:"methodName"`
		Params  []struct {
			String  *string `xml:"value>string"`
			Binary  *string `xml:"value>base64"`
			Integer *int64  `xml:"value>i8"`
		} `xml:"params>param"`
	}
	if xml.Unmarshal(body, &wire) != nil || wire.XMLName.Local != "methodCall" || wire.Method != "load.raw_start" || len(wire.Params) != 4 {
		t.Fatal("invalid XML-RPC call shape")
	}
	if wire.Params[0].String == nil || *wire.Params[0].String != "" || wire.Params[1].Binary == nil || wire.Params[2].String == nil || *wire.Params[2].String != command || wire.Params[3].Integer == nil || *wire.Params[3].Integer != int64(1)<<40 {
		t.Fatal("RPC parameter order/type changed")
	}
	decoded, err := base64.StdEncoding.DecodeString(*wire.Params[1].Binary)
	if err != nil || !bytes.Equal(binary, decoded) {
		t.Fatal("raw torrent bytes changed")
	}
}

func TestRPCResponseNativeValues(t *testing.T) {
	for _, tc := range []struct {
		name, body, kind, text string
		integer                int64
	}{
		{"version", "<string>0.9.8</string>", "string", "0.9.8", 0},
		{"bare", "名称 &amp; title", "string", "名称 & title", 0},
		{"empty", "", "string", "", 0},
		{"large_bytes", "<i8>5497558138880</i8>", "int", "", 5497558138880},
		{"integer", "<int>0</int>", "int", "", 0},
		{"i4", "<i4>-1</i4>", "int", "", -1},
		{"boolean", "<boolean>1</boolean>", "boolean", "", 1},
		{"binary", "<base64>AP8=</base64>", "base64", string([]byte{0, 255}), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := decodeRPCResponse(rpcTestResponse(tc.body))
			if err != nil || value.kind != tc.kind || value.text != tc.text || value.integer != tc.integer {
				t.Fatalf("wrong native value: %v", err)
			}
		})
	}
	empty, err := decodeRPCResponse(rpcTestResponse("<array><data/></array>"))
	if err != nil || empty.kind != "array" || empty.array == nil || len(empty.array) != 0 {
		t.Fatal("empty array is not an explicit empty list")
	}
	row, err := decodeRPCResponse(rpcTestResponse("<array><data><value><array><data><value><string>hash</string></value><value><i8>5497558138880</i8></value></data></array></value></data></array>"))
	if err != nil || len(row.array) != 1 || len(row.array[0].array) != 2 || row.array[0].array[1].integer != 5497558138880 {
		t.Fatal("multicall row lost integer precision")
	}
}

func TestRPCResponseRejectsMalformedAndFault(t *testing.T) {
	fault := `<fault><value><struct><member><name>faultCode</name><value><int>-503</int></value></member><member><name>faultString</name><value><string>owned-private-credential</string></value></member></struct></value></fault>`
	invalid := map[string][]byte{
		"empty":             nil,
		"html":              []byte("<html>upstream gateway</html>"),
		"two_roots":         append(rpcTestResponse("<int>0</int>"), rpcTestResponse("<int>0</int>")...),
		"truncated":         []byte("<methodResponse><params>"),
		"fault":             []byte("<methodResponse>" + fault + "</methodResponse>"),
		"fault_and_success": []byte("<methodResponse>" + fault + "<params><param><value>ok</value></param></params></methodResponse>"),
		"two_results":       []byte("<methodResponse><params><param><value>one</value></param><param><value>two</value></param></params></methodResponse>"),
		"unknown_field":     []byte("<methodResponse><ignored/><params><param><value>ok</value></param></params></methodResponse>"),
		"namespace":         []byte(`<methodResponse xmlns="urn:other"><params><param><value>ok</value></param></params></methodResponse>`),
		"attribute":         []byte(`<methodResponse other="ignored"><params><param><value>ok</value></param></params></methodResponse>`),
		"doctype":           []byte(`<!DOCTYPE methodResponse [<!ENTITY x SYSTEM "file:///not-read">]><methodResponse><params><param><value>&x;</value></param></params></methodResponse>`),
		"instruction":       append([]byte("<?process unsafe?>"), rpcTestResponse("<int>0</int>")...),
		"tail":              append(rpcTestResponse("<int>0</int>"), []byte("tail")...),
	}
	for name, value := range map[string]string{
		"two_values":           "<int>0</int><string>also</string>",
		"mixed_text":           "extra<int>0</int>",
		"int_overflow":         "<int>2147483648</int>",
		"i8_overflow":          "<i8>9223372036854775808</i8>",
		"fractional_integer":   "<i8>1.5</i8>",
		"bad_boolean":          "<boolean>true</boolean>",
		"nan":                  "<double>NaN</double>",
		"infinite":             "<double>+Inf</double>",
		"bad_binary":           "<base64>!@#</base64>",
		"unknown_type":         "<untrusted>value</untrusted>",
		"missing_data":         "<array/>",
		"wrong_array_child":    "<array><data><int>1</int></data></array>",
		"duplicate_member":     "<struct><member><name>key</name><value>1</value></member><member><name>key</name><value>2</value></member></struct>",
		"missing_member_value": "<struct><member><name>key</name></member></struct>",
	} {
		invalid[name] = rpcTestResponse(value)
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := decodeRPCResponse(body)
			if !errors.Is(err, ports.ErrBadResponse) || strings.Contains(err.Error(), "owned-private") {
				t.Fatalf("unsafe or accepted invalid reply: %v", err)
			}
		})
	}
}

func TestRPCResponseResourceBounds(t *testing.T) {
	cases := map[string][]byte{
		"bytes": rpcTestResponse("<string>" + strings.Repeat("x", maxRPCResponseBytes) + "</string>"),
		"depth": rpcTestResponse(strings.Repeat("<array><data><value>", 30) + "<int>0</int>" + strings.Repeat("</value></data></array>", 30)),
		"nodes": rpcTestResponse("<array><data>" + strings.Repeat("<value/>", 100001) + "</data></array>"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRPCResponse(body); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("missing resource limit: %v", err)
			}
		})
	}
}

func TestRPCRequestRejectsUnsafeArguments(t *testing.T) {
	for _, method := range []string{"", "load.raw;execute", "load.<raw>", strings.Repeat("x", 129)} {
		if _, err := encodeRPCRequest(method); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("accepted invalid method")
		}
	}
	for _, arg := range []any{"nul\x00value", string([]byte{0xff}), "\ufffe", "\uffff", true, nil, bytes.Repeat([]byte{'x'}, ports.MaxTorrentMetainfoBytes+1)} {
		if _, err := encodeRPCRequest("load.raw", arg); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("accepted invalid argument")
		}
	}
	if _, err := encodeRPCRequest("load.raw", make([]any, 65)...); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("unbounded parameter count")
	}
	large := strings.Repeat("x", maxRPCResponseBytes)
	if _, err := encodeRPCRequest("load.raw", large, large, large); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("unbounded aggregate input")
	}
}

func rpcTestResponse(value string) []byte {
	return []byte(`<?xml version="1.0"?><methodResponse><params><param><value>` + value + `</value></param></params></methodResponse>`)
}
