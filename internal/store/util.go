package store

// util.go: 哈希与文件名消毒。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sanitize(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.TrimSpace(name)
	if name == "" {
		return "upload.pdf"
	}
	return name
}
