package discovery

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// parseDarwinProcArgs decodes the kern.procargs2 layout: argc, executable
// path, argv, then the environment. Keeping the parser platform-neutral lets
// its binary fixtures run on non-Darwin CI.
func parseDarwinProcArgs(raw []byte) (map[string]string, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("kern.procargs2 response is too short")
	}
	argc := int(int32(binary.NativeEndian.Uint32(raw[:4])))
	if argc < 0 || argc > 1<<20 {
		return nil, fmt.Errorf("invalid process argument count %d", argc)
	}
	offset := 4

	// The executable path precedes argv and is not included in argc.
	_, next, ok := nextCString(raw, offset)
	if !ok {
		return nil, fmt.Errorf("process executable path is unterminated")
	}
	offset = skipNUL(raw, next)

	for index := 0; index < argc; index++ {
		_, next, ok = nextCString(raw, offset)
		if !ok {
			return nil, fmt.Errorf("process argument %d is unterminated", index)
		}
		offset = next
	}
	offset = skipNUL(raw, offset)

	env := make(map[string]string)
	for offset < len(raw) {
		value, next, found := nextCString(raw, offset)
		if !found || value == "" {
			break
		}
		key, content, found := strings.Cut(value, "=")
		if found && isDiscoveryEnvKey(key) {
			env[key] = content
		}
		offset = next
	}
	return env, nil
}

func nextCString(raw []byte, offset int) (string, int, bool) {
	if offset >= len(raw) {
		return "", offset, false
	}
	end := offset
	for end < len(raw) && raw[end] != 0 {
		end++
	}
	if end == len(raw) {
		return "", offset, false
	}
	return string(raw[offset:end]), end + 1, true
}

func skipNUL(raw []byte, offset int) int {
	for offset < len(raw) && raw[offset] == 0 {
		offset++
	}
	return offset
}
