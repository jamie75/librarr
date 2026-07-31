package download

import (
	"crypto/md5" // #nosec G501 -- HTTP Digest requires MD5 compatibility.
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type digestChallenge struct {
	realm     string
	nonce     string
	opaque    string
	algorithm string
	qop       string
	stale     bool
	nc        uint32
	cnonce    string
}

func parseDigestChallenge(header string) (digestChallenge, error) {
	start := strings.Index(strings.ToLower(header), "digest")
	if start < 0 {
		return digestChallenge{}, fmt.Errorf("rTorrent did not provide a Digest challenge")
	}
	params, err := parseAuthParams(strings.TrimSpace(header[start+len("digest"):]))
	if err != nil {
		return digestChallenge{}, fmt.Errorf("malformed rTorrent Digest challenge")
	}
	challenge := digestChallenge{
		realm: strings.TrimSpace(params["realm"]), nonce: strings.TrimSpace(params["nonce"]),
		opaque: strings.TrimSpace(params["opaque"]), algorithm: strings.ToUpper(strings.TrimSpace(params["algorithm"])),
		qop: strings.TrimSpace(params["qop"]), stale: strings.EqualFold(strings.TrimSpace(params["stale"]), "true"),
	}
	if challenge.realm == "" || challenge.nonce == "" {
		return digestChallenge{}, fmt.Errorf("malformed rTorrent Digest challenge")
	}
	if challenge.algorithm == "" {
		challenge.algorithm = "MD5"
	}
	if challenge.algorithm != "MD5" {
		return digestChallenge{}, fmt.Errorf("unsupported rTorrent Digest algorithm")
	}
	if challenge.qop != "" {
		foundAuth := false
		for _, qop := range strings.Split(challenge.qop, ",") {
			if strings.EqualFold(strings.TrimSpace(qop), "auth") {
				foundAuth = true
				break
			}
		}
		if !foundAuth {
			return digestChallenge{}, fmt.Errorf("unsupported rTorrent Digest quality of protection")
		}
		challenge.qop = "auth"
	}
	cnonce, err := secureCNonce()
	if err != nil {
		return digestChallenge{}, fmt.Errorf("create Digest client nonce")
	}
	challenge.cnonce = cnonce
	return challenge, nil
}

func parseAuthParams(raw string) (map[string]string, error) {
	params := make(map[string]string)
	for i := 0; i < len(raw); {
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == ',') {
			i++
		}
		if i >= len(raw) {
			break
		}
		keyStart := i
		for i < len(raw) && raw[i] != '=' && raw[i] != ',' {
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			return nil, fmt.Errorf("missing Digest parameter value")
		}
		key := strings.ToLower(strings.TrimSpace(raw[keyStart:i]))
		i++
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		if i >= len(raw) {
			return nil, fmt.Errorf("missing Digest parameter value")
		}
		var value string
		if raw[i] == '"' {
			i++
			var b strings.Builder
			closed := false
			for i < len(raw) {
				if raw[i] == '\\' && i+1 < len(raw) {
					b.WriteByte(raw[i+1])
					i += 2
					continue
				}
				if raw[i] == '"' {
					i++
					closed = true
					break
				}
				b.WriteByte(raw[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated Digest parameter")
			}
			value = b.String()
		} else {
			valueStart := i
			for i < len(raw) && raw[i] != ',' {
				i++
			}
			value = strings.TrimSpace(raw[valueStart:i])
		}
		if key == "" || value == "" {
			return nil, fmt.Errorf("invalid Digest parameter")
		}
		params[key] = value
	}
	return params, nil
}

func secureCNonce() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func digestAuthorization(challenge *digestChallenge, username, password, method, uri string) string {
	challenge.nc++
	nc := fmt.Sprintf("%08x", challenge.nc)
	ha1 := md5Hex(username + ":" + challenge.realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)
	response := md5Hex(ha1 + ":" + challenge.nonce + ":" + ha2)
	if challenge.qop != "" {
		response = md5Hex(ha1 + ":" + challenge.nonce + ":" + nc + ":" + challenge.cnonce + ":" + challenge.qop + ":" + ha2)
	}
	parts := []string{
		`Digest username="` + escapeDigest(username) + `"`,
		`realm="` + escapeDigest(challenge.realm) + `"`,
		`nonce="` + escapeDigest(challenge.nonce) + `"`,
		`uri="` + escapeDigest(uri) + `"`,
		`algorithm=MD5`,
	}
	if challenge.opaque != "" {
		parts = append(parts, `opaque="`+escapeDigest(challenge.opaque)+`"`)
	}
	if challenge.qop != "" {
		parts = append(parts, `qop=auth`, `nc=`+nc, `cnonce="`+challenge.cnonce+`"`)
	}
	parts = append(parts, `response="`+response+`"`)
	return strings.Join(parts, ", ")
}

func md5Hex(value string) string {
	// HTTP Digest MD5 is protocol-mandated and is not used for password storage.
	// The output is used only to construct the RFC-compatible Digest response.
	// codeql[go/weak-sensitive-data-hashing]

	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func escapeDigest(value string) string {
	return strings.NewReplacer(`\\`, `\\\\`, `"`, `\"`).Replace(value)
}
