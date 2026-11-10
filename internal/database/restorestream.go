package database

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

// mysqlRestorePreamble relaxes 8.0's strict mode so 5.7-era dumps (zero dates,
// invalid defaults) load, and lets tables arrive in any order.
const mysqlRestorePreamble = "SET SESSION sql_mode=''; SET FOREIGN_KEY_CHECKS=0;\n"

var (
	gzipMagic     = []byte{0x1f, 0x8b}
	pgCustomMagic = []byte("PGDMP")
)

func hasMagic(br *bufio.Reader, magic []byte) bool {
	head, _ := br.Peek(len(magic))
	return bytes.Equal(head, magic)
}

func decompressed(src io.Reader) (*bufio.Reader, error) {
	br := bufio.NewReader(src)
	if !hasMagic(br, gzipMagic) {
		return br, nil
	}
	zr, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("open gzip stream: %w", err)
	}
	return bufio.NewReader(zr), nil
}

func mysqlRestoreStream(src io.Reader) (io.Reader, error) {
	body, err := decompressed(src)
	if err != nil {
		return nil, err
	}
	return io.MultiReader(strings.NewReader(mysqlRestorePreamble), body), nil
}

func postgresRestoreStream(src io.Reader) (body io.Reader, custom bool, err error) {
	br, err := decompressed(src)
	if err != nil {
		return nil, false, err
	}
	return br, hasMagic(br, pgCustomMagic), nil
}

// tailBuffer keeps only the last max bytes written: a failing restore's useful
// error is at the end, and the tools above it can be arbitrarily chatty.
type tailBuffer struct {
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string {
	return strings.TrimSpace(string(t.buf))
}
