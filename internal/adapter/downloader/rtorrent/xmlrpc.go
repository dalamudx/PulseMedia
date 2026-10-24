package rtorrent

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const maxRPCResponseBytes = 4 << 20

// Keep the wire representation separate from download facts. In particular an
// XML-RPC fault or a malformed array cannot become an empty torrent list.
type rpcValue struct {
	kind    string
	text    string
	integer int64
	number  float64
	array   []rpcValue
	object  map[string]rpcValue
}

type rpcRequest struct {
	XMLName xml.Name       `xml:"methodCall"`
	Method  string         `xml:"methodName"`
	Params  []rpcParameter `xml:"params>param"`
}

type rpcParameter struct {
	Value rpcArgument `xml:"value"`
}

type rpcArgument struct {
	Text    *string `xml:"string,omitempty"`
	Binary  *string `xml:"base64,omitempty"`
	Integer *int64  `xml:"i8,omitempty"`
}

func encodeRPCRequest(method string, arguments ...any) ([]byte, error) {
	if method == "" || len(method) > 128 || len(arguments) > 64 {
		return nil, ports.ErrBadResponse
	}
	for _, r := range method {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' {
			continue
		}
		return nil, ports.ErrBadResponse
	}
	request := rpcRequest{Method: method, Params: make([]rpcParameter, 0, len(arguments))}
	remaining := ports.MaxTorrentMetainfoBytes + 65536
	for _, argument := range arguments {
		var value rpcArgument
		switch v := argument.(type) {
		case string:
			remaining -= len(v)
			if remaining < 0 || len(v) > maxRPCResponseBytes || !rpcXMLText(v) {
				return nil, ports.ErrBadResponse
			}
			value.Text = &v
		case []byte:
			remaining -= len(v)
			if remaining < 0 || len(v) > ports.MaxTorrentMetainfoBytes {
				return nil, ports.ErrBadResponse
			}
			encoded := base64.StdEncoding.EncodeToString(v)
			value.Binary = &encoded
		case int64:
			value.Integer = &v
		default:
			return nil, ports.ErrBadResponse
		}
		request.Params = append(request.Params, rpcParameter{Value: value})
	}
	body, err := xml.Marshal(request)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	return body, nil
}

func rpcXMLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r != '\t' && r != '\n' && r != '\r' && (r < 0x20 || r == 0xfffe || r == 0xffff) {
			return false
		}
	}
	return true
}

type rpcNode struct {
	name     string
	text     string
	children []rpcNode
}

func decodeRPCResponse(body []byte) (rpcValue, error) {
	bad := func() (rpcValue, error) { return rpcValue{}, ports.ErrBadResponse }
	if len(body) == 0 || len(body) > maxRPCResponseBytes {
		return bad()
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var root *rpcNode
	declaration := false
	count := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return bad()
		}
		switch t := token.(type) {
		case xml.StartElement:
			if root != nil {
				return bad()
			}
			node, err := readRPCNode(decoder, t, 0, &count)
			if err != nil {
				return bad()
			}
			root = &node
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return bad()
			}
		case xml.ProcInst:
			if root != nil || declaration || t.Target != "xml" {
				return bad()
			}
			declaration = true
		case xml.Comment:
		default:
			return bad()
		}
	}
	if root == nil || !rpcContainer(*root, "methodResponse", 1) {
		return bad()
	}
	child := root.children[0]
	if child.name == "fault" {
		// Validate the shape, but never return upstream fault text to callers.
		if !rpcContainer(child, "fault", 1) {
			return bad()
		}
		value, err := decodeRPCValue(child.children[0])
		if err != nil || value.kind != "struct" || len(value.object) != 2 || value.object["faultCode"].kind != "int" || value.object["faultString"].kind != "string" {
			return bad()
		}
		return bad()
	}
	if !rpcContainer(child, "params", 1) || !rpcContainer(child.children[0], "param", 1) {
		return bad()
	}
	return decodeRPCValue(child.children[0].children[0])
}

func readRPCNode(decoder *xml.Decoder, start xml.StartElement, depth int, count *int) (rpcNode, error) {
	*count++
	if depth > 24 || *count > 100000 || start.Name.Space != "" || len(start.Attr) != 0 {
		return rpcNode{}, ports.ErrBadResponse
	}
	node := rpcNode{name: start.Name.Local}
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return rpcNode{}, ports.ErrBadResponse
		}
		switch t := token.(type) {
		case xml.StartElement:
			child, err := readRPCNode(decoder, t, depth+1, count)
			if err != nil {
				return rpcNode{}, err
			}
			node.children = append(node.children, child)
		case xml.CharData:
			text.Write(t)
		case xml.Comment:
		case xml.EndElement:
			if t.Name != start.Name {
				return rpcNode{}, ports.ErrBadResponse
			}
			node.text = text.String()
			return node, nil
		default:
			return rpcNode{}, ports.ErrBadResponse
		}
	}
}

func rpcContainer(node rpcNode, name string, count int) bool {
	return node.name == name && strings.TrimSpace(node.text) == "" && (count < 0 || len(node.children) == count)
}

func decodeRPCValue(node rpcNode) (rpcValue, error) {
	bad := func() (rpcValue, error) { return rpcValue{}, ports.ErrBadResponse }
	if node.name != "value" {
		return bad()
	}
	if len(node.children) == 0 {
		return rpcValue{kind: "string", text: node.text}, nil
	}
	if !rpcContainer(node, "value", 1) {
		return bad()
	}
	node = node.children[0]
	value := rpcValue{kind: node.name}
	switch node.name {
	case "array":
		if !rpcContainer(node, "array", 1) || !rpcContainer(node.children[0], "data", -1) {
			return bad()
		}
		value.array = make([]rpcValue, 0, len(node.children[0].children))
		for _, child := range node.children[0].children {
			v, err := decodeRPCValue(child)
			if err != nil {
				return bad()
			}
			value.array = append(value.array, v)
		}
		return value, nil
	case "struct":
		if !rpcContainer(node, "struct", -1) {
			return bad()
		}
		value.object = make(map[string]rpcValue, len(node.children))
		for _, child := range node.children {
			if !rpcContainer(child, "member", 2) || child.children[0].name != "name" || len(child.children[0].children) != 0 || child.children[0].text == "" {
				return bad()
			}
			key := child.children[0].text
			if _, exists := value.object[key]; exists {
				return bad()
			}
			v, err := decodeRPCValue(child.children[1])
			if err != nil {
				return bad()
			}
			value.object[key] = v
		}
		return value, nil
	}
	if len(node.children) != 0 {
		return bad()
	}
	switch node.name {
	case "string":
		value.text = node.text
	case "int", "i4", "i8":
		bits := 32
		if node.name == "i8" {
			bits = 64
		}
		n, err := strconv.ParseInt(strings.TrimSpace(node.text), 10, bits)
		if err != nil {
			return bad()
		}
		value.kind, value.integer = "int", n
	case "boolean":
		s := strings.TrimSpace(node.text)
		if s != "0" && s != "1" {
			return bad()
		}
		if s == "1" {
			value.integer = 1
		}
	case "double":
		n, err := strconv.ParseFloat(strings.TrimSpace(node.text), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return bad()
		}
		value.number = n
	case "base64":
		b, err := base64.StdEncoding.DecodeString(node.text)
		if err != nil {
			return bad()
		}
		value.text = string(b)
	default:
		return bad()
	}
	return value, nil
}
