package validation

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func probeDB(path, mode string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // Windows drive paths must produce file:///C:/..., not a URI authority.
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := url.Values{"mode": {mode}, "_pragma": {"journal_mode(DELETE)", "synchronous(EXTRA)", "foreign_keys(1)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func mustProbeDB(t *testing.T, path, mode string) *sql.DB {
	t.Helper()
	db, err := probeDB(path, mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqlMust(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func probeValue(t *testing.T, db *sql.DB) string {
	t.Helper()
	var value string
	if err := db.QueryRow("SELECT value FROM probe WHERE id=1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %q %v", integrity, err)
	}
	return value
}

func TestSQLiteTransactionsAndConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	db := mustProbeDB(t, path, "rwc")
	for query, want := range map[string]string{"PRAGMA journal_mode": "delete", "PRAGMA synchronous": "3", "PRAGMA foreign_keys": "1"} {
		var got string
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %q, %v", query, got, err)
		}
	}
	sqlMust(t, db, "CREATE TABLE probe(id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO probe VALUES(1,'old'); PRAGMA user_version=7")
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE probe SET value='must-rollback'; INSERT INTO probe VALUES(1,'duplicate')"); err == nil {
		t.Fatal("constraint failure missing")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := probeValue(t, db); got != "old" {
		t.Fatal("failed transaction changed data")
	}
	sqlMust(t, db, "UPDATE probe SET value='committed'")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := mustProbeDB(t, path, "rw")
	if got := probeValue(t, reopened); got != "committed" {
		t.Fatal("commit not persisted")
	}
	var version int
	if err := reopened.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("version: %d %v", version, err)
	}
}

func TestSQLiteMissingCorruptAndWriteFailures(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	db := mustProbeDB(t, missing, "rw")
	if err := db.Ping(); err == nil {
		t.Fatal("rw created a missing database")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup created a file")
	}
	corrupt := filepath.Join(dir, "corrupt.db")
	bad := []byte("deliberately not an sqlite database")
	if err := os.WriteFile(corrupt, bad, 0600); err != nil {
		t.Fatal(err)
	}
	broken := mustProbeDB(t, corrupt, "rw")
	var check string
	if err := broken.QueryRow("PRAGMA integrity_check").Scan(&check); err == nil {
		t.Fatal("corrupt state accepted")
	}
	after, _ := os.ReadFile(corrupt)
	if string(after) != string(bad) {
		t.Fatal("corrupt file was reinitialized")
	}
	path := filepath.Join(dir, "full.db")
	writable := mustProbeDB(t, path, "rwc")
	sqlMust(t, writable, "CREATE TABLE probe(id INTEGER PRIMARY KEY,value TEXT NOT NULL); INSERT INTO probe VALUES(1,'old')")
	var pages int
	if err := writable.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	var effective int
	if err := writable.QueryRow("PRAGMA max_page_count=" + strconv.Itoa(pages)).Scan(&effective); err != nil || effective != pages {
		t.Fatalf("page cap: %d %v", effective, err)
	}
	_, err := writable.Exec("INSERT INTO probe VALUES(2,zeroblob(1048576))")
	var se *sqlite.Error
	if !errors.As(err, &se) || se.Code()&255 != 13 {
		t.Fatalf("expected SQLITE_FULL, got %v", err)
	}
	if got := probeValue(t, writable); got != "old" {
		t.Fatal("full disk simulation changed committed row")
	}
}

func TestSQLiteProcessCrashRecovery(t *testing.T) {
	for _, mode := range []string{"uncommitted", "committed"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "probe.db")
			db := mustProbeDB(t, path, "rwc")
			sqlMust(t, db, "CREATE TABLE probe(id INTEGER PRIMARY KEY,value TEXT NOT NULL); INSERT INTO probe VALUES(1,'old'); CREATE TABLE ballast(id INTEGER PRIMARY KEY,payload BLOB); WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<32) INSERT INTO ballast SELECT x,zeroblob(4096) FROM n")
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600); err != nil {
				t.Fatal(err)
			}
			child := startHelper(t, "sqlite-write", dir)
			_ = waitFile(t, filepath.Join(dir, "sqlite.ready"))
			if mode == "uncommitted" {
				if _, err := os.Stat(path + "-journal"); err != nil {
					t.Fatal("rollback journal not created")
				}
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = child.Wait()
			reopened := mustProbeDB(t, path, "rw")
			want := "old"
			if mode == "committed" {
				want = "new"
			}
			if got := probeValue(t, reopened); got != want {
				t.Fatalf("crash recovery %s: %q", mode, got)
			}
		})
	}
}

func sqliteWriteHelper() int {
	dir := os.Getenv(dirKey)
	db, err := probeDB(filepath.Join(dir, "probe.db"), "rw")
	if err != nil {
		return 41
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA cache_size=8"); err != nil {
		return 42
	}
	tx, err := db.Begin()
	if err != nil {
		return 43
	}
	if _, err := tx.Exec("UPDATE probe SET value='new'; UPDATE ballast SET payload=zeroblob(8192)"); err != nil {
		return 44
	}
	mode, _ := os.ReadFile(filepath.Join(dir, "mode"))
	if string(mode) == "committed" {
		if err := tx.Commit(); err != nil {
			return 45
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sqlite.ready"), []byte("ready"), 0600); err != nil {
		return 46
	}
	time.Sleep(12 * time.Second)
	return 0
}
