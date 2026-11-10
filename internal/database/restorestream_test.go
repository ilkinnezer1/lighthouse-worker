package database

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMysqlRestoreStream(t *testing.T) {
	const dump = "CREATE TABLE t (id int);\n"
	for name, src := range map[string][]byte{
		"plain": []byte(dump),
		"gzip":  gzipped(t, dump),
	} {
		t.Run(name, func(t *testing.T) {
			body, err := mysqlRestoreStream(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := readAll(t, body), mysqlRestorePreamble+dump; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestMysqlRestoreStreamEmpty(t *testing.T) {
	body, err := mysqlRestoreStream(bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, body); got != mysqlRestorePreamble {
		t.Fatalf("got %q", got)
	}
}

func TestMysqlRestoreStreamCorruptGzip(t *testing.T) {
	src := gzipped(t, strings.Repeat("INSERT INTO t VALUES (1);\n", 100))
	src = src[:len(src)-6]
	body, err := mysqlRestoreStream(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("expected a read error from a truncated gzip stream")
	}
}

func TestPostgresRestoreStream(t *testing.T) {
	custom := "PGDMP\x01\x0e\x00binary"
	plain := "SET statement_timeout = 0;\nCREATE TABLE t (id int);\n"
	cases := []struct {
		name       string
		src        []byte
		wantCustom bool
		wantBody   string
	}{
		{"custom", []byte(custom), true, custom},
		{"custom gzip", gzipped(t, custom), true, custom},
		{"plain", []byte(plain), false, plain},
		{"plain gzip", gzipped(t, plain), false, plain},
		{"short", []byte("PG"), false, "PG"},
		{"empty", nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, isCustom, err := postgresRestoreStream(bytes.NewReader(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			if isCustom != tc.wantCustom {
				t.Fatalf("custom = %v, want %v", isCustom, tc.wantCustom)
			}
			if got := readAll(t, body); got != tc.wantBody {
				t.Fatalf("body %q, want %q", got, tc.wantBody)
			}
		})
	}
}

func TestTailBuffer(t *testing.T) {
	tb := newTailBuffer(8)
	for _, s := range []string{"abc", "defgh", "ijk"} {
		if n, err := tb.Write([]byte(s)); err != nil || n != len(s) {
			t.Fatalf("write %q: n=%d err=%v", s, n, err)
		}
	}
	if got := tb.String(); got != "defghijk" {
		t.Fatalf("got %q", got)
	}
	_, _ = tb.Write([]byte("0123456789ERROR 1067 (42000) at line 1234"))
	if got := tb.String(); got != "ine 1234" {
		t.Fatalf("got %q", got)
	}
}

func TestImportExt(t *testing.T) {
	cases := []struct {
		typ     dbgen.DatabaseType
		name    string
		want    string
		wantErr bool
	}{
		{dbgen.DatabaseTypeMYSQL, "wj_dev3.sql", "sql", false},
		{dbgen.DatabaseTypeMYSQL, "WJ.SQL.GZ", "sql.gz", false},
		{dbgen.DatabaseTypeMYSQL, "db.dump", "", true},
		{dbgen.DatabaseTypePOSTGRES, "db.dump", "dump", false},
		{dbgen.DatabaseTypePOSTGRES, "db.sql.gz", "sql.gz", false},
		{dbgen.DatabaseTypePOSTGRES, "db.tar", "", true},
		{dbgen.DatabaseTypePOSTGRES, "", "", true},
	}
	for _, tc := range cases {
		got, err := importExt(tc.typ, tc.name)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("importExt(%s, %q) = %q, %v", tc.typ, tc.name, got, err)
		}
		if err != nil && !errors.Is(err, ErrImportBadFile) {
			t.Errorf("importExt(%s, %q) error not ErrImportBadFile: %v", tc.typ, tc.name, err)
		}
	}
}

func TestCappedReader(t *testing.T) {
	under := &cappedReader{r: strings.NewReader("12345"), remaining: 5}
	if got := readAll(t, under); got != "12345" || under.exceeded {
		t.Fatalf("at limit: got %q exceeded=%v", got, under.exceeded)
	}

	over := &cappedReader{r: strings.NewReader("123456"), remaining: 5}
	_, err := io.ReadAll(over)
	if !errors.Is(err, ErrImportTooLarge) || !over.exceeded {
		t.Fatalf("over limit: err=%v exceeded=%v", err, over.exceeded)
	}
}
