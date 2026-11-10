package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/server/core"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func initial(t *testing.T) (string, Data, Handoff) {
	t.Helper()
	dir := t.TempDir()
	d, h, err := Initialize(dir, time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC))
	must(t, err)
	return dir, d, h
}
func record(d Data, i int) core.CredentialRecord {
	v := d.Credentials[0].Credential
	v.ID = auth.ID("crd")
	v.CreationOperationID = fmt.Sprintf("%032x", i)
	v.Name = strings.Repeat("n", 80)
	v.RotatedFromID = ""
	v.Revoked = false
	v.RevokedAt = nil
	return core.CredentialRecord{Credential: v, Digest: auth.Digest(auth.Secret(""))}
}

func TestInitializeOpenAndOfflineRecovery(t *testing.T) {
	dir, d, h := initial(t)
	if !d.Admin.Verify(h.AdminPassword) || d.Admin.Verify(h.AdminPassword+" ") {
		t.Fatal("password digest differs")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "management.db"))
	must(t, err)
	for _, secret := range []string{h.AdminPassword, h.Access, h.MCP} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext secret persisted")
		}
	}
	_, _, err = Initialize(dir, time.Now())
	if err == nil {
		t.Fatal("reinitialization overwrote state")
	}
	s, loaded, err := Open(dir)
	must(t, err)
	if loaded.Revision != 1 || len(loaded.Credentials) != 2 {
		t.Fatal("durable state differs")
	}
	_, _, err = Open(dir)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second owner accepted: %v", err)
	}
	_, _, err = Recover(dir, time.Now(), nil, true)
	if !errors.Is(err, ErrLocked) {
		t.Fatal("online administrator reset accepted")
	}
	must(t, s.Close())
	after, password, err := Recover(dir, time.Now(), []string{d.Credentials[0].Credential.ID}, true)
	must(t, err)
	if after.Revision != 2 || after.Admin.Verify(h.AdminPassword) || !after.Admin.Verify(password) {
		t.Fatal("administrator recovery failed")
	}
	found := false
	for _, r := range after.Credentials {
		if r.Credential.ID == d.Credentials[0].Credential.ID {
			found = r.Credential.Revoked
		}
	}
	if !found {
		t.Fatal("offline revocation lost")
	}
}

func TestStartupRefusesMissingDamagedAndUnsupportedState(t *testing.T) {
	for _, kind := range []string{"missing_dir", "empty", "corrupt", "unknown_version", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			switch kind {
			case "missing_dir":
				dir = filepath.Join(dir, "absent")
			case "corrupt":
				must(t, os.WriteFile(filepath.Join(dir, "management.db"), []byte("broken database"), 0600))
			case "unknown_version", "incomplete":
				_, _, err := Initialize(dir, time.Now())
				must(t, err)
				s, _, err := Open(dir)
				must(t, err)
				query := "PRAGMA user_version=99"
				if kind == "incomplete" {
					query = "DELETE FROM management"
				}
				_, err = s.db.Exec(query)
				must(t, err)
				must(t, s.Close())
			}
			_, _, err := Open(dir)
			if !errors.Is(err, ErrState) {
				t.Fatalf("bad state accepted: %v", err)
			}
			if kind == "missing_dir" {
				if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("startup created missing directory")
				}
			}
			if kind == "empty" {
				files, err := os.ReadDir(dir)
				must(t, err)
				if len(files) != 0 {
					t.Fatal("startup created state")
				}
			}
		})
	}
}

func TestTransactionRollbackAndDiskFull(t *testing.T) {
	dir, d, _ := initial(t)
	s, _, err := Open(dir)
	must(t, err)
	defer s.Close()
	duplicate := record(d, 1)
	duplicate.Credential.CreationOperationID = d.Credentials[0].Credential.CreationOperationID
	r := s.Apply(context.Background(), Change{ExpectedRevision: 1, Credential: &duplicate, RevokeIDs: []string{d.Credentials[0].Credential.ID}, RevokedAt: time.Now()})
	if r != NotCommitted {
		t.Fatalf("constraint failure not rolled back: %+v", r)
	}
	loaded, err := s.Read()
	must(t, err)
	if loaded.Revision != 1 || loaded.Credentials[0].Credential.Revoked {
		t.Fatal("failed rotation partially committed")
	}
	var pages int
	must(t, s.db.QueryRow("PRAGMA page_count").Scan(&pages))
	_, err = s.db.Exec("PRAGMA max_page_count=" + strconv.Itoa(pages))
	must(t, err)
	revision := int64(1)
	failed := false
	for i := 2; i < 130; i++ {
		next := record(d, i)
		r = s.Apply(context.Background(), Change{ExpectedRevision: revision, Credential: &next})
		if r == Committed {
			revision++
			continue
		}
		failed = true
		break
	}
	if !failed {
		t.Fatal("SQLITE_FULL was not exercised")
	}
	loaded, err = s.Read()
	must(t, err)
	if loaded.Revision != revision || int64(len(loaded.Credentials)) != revision+1 {
		t.Fatal("write failure changed last commit")
	}
	// Independently confirm the database is actually full, without relying on a
	// discarded implementation error or a guessed failure reason.
	_, err = s.db.Exec("CREATE TABLE full_probe(value BLOB)")
	if err == nil {
		_, err = s.db.Exec("INSERT INTO full_probe VALUES(zeroblob(1048576))")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_FULL {
		t.Fatalf("expected SQLITE_FULL, got %v", err)
	}
}

// The child exits without closing SQLite or removing the guard, on either side
// of commit. No product command or remote execution path exposes this helper.
func TestCrashRecovery(t *testing.T) {
	if dir := os.Getenv("REMOTA_STORE_CRASH_DIR"); dir != "" {
		s, d, err := Open(dir)
		if err != nil {
			os.Exit(81)
		}
		r := record(d, 123)
		r.Credential.RotatedFromID = d.Credentials[0].Credential.ID
		g := Guard{Action: "rotate", TargetID: r.Credential.RotatedFromID, OperationID: r.Credential.CreationOperationID}
		if s.WriteGuard(g) != nil {
			os.Exit(82)
		}
		tx, err := s.db.Begin()
		if err != nil {
			os.Exit(83)
		}
		if _, err = tx.Exec("UPDATE credentials SET revoked_ms=? WHERE id=?", time.Now().UnixMilli(), g.TargetID); err != nil {
			os.Exit(84)
		}
		// Inherit all source metadata as a real rotation must.
		r.Credential.Name = d.Credentials[0].Credential.Name
		if insert(tx, r) != nil {
			os.Exit(85)
		}
		if _, err = tx.Exec("UPDATE management SET revision=2"); err != nil {
			os.Exit(86)
		}
		if os.Getenv("REMOTA_STORE_CRASH_AFTER") == "1" {
			if tx.Commit() != nil {
				os.Exit(87)
			}
		}
		os.Exit(0)
	}
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			dir, _, _ := initial(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashRecovery$")
			cmd.Env = append(os.Environ(), "REMOTA_STORE_CRASH_DIR="+dir, "REMOTA_STORE_CRASH_AFTER="+map[bool]string{false: "0", true: "1"}[after])
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("crash child failed: %v %s", err, out)
			}
			_, _, err := Open(dir)
			if !errors.Is(err, ErrRecovery) {
				t.Fatal("uncertain startup resumed automatically")
			}
			st, _, err := OpenRecovery(dir)
			must(t, err)
			guard, _, err := st.Pending()
			must(t, err)
			must(t, st.Close())
			loaded, _, err := Recover(dir, time.Now(), nil, false)
			must(t, err)
			want := 2
			if after {
				want = 3
			}
			if len(loaded.Credentials) != want {
				t.Fatal("crash lost atomicity")
			}
			for _, r := range loaded.Credentials {
				if (r.Credential.ID == guard.TargetID || r.Credential.CreationOperationID == fmt.Sprintf("%032x", 123)) && !r.Credential.Revoked {
					t.Fatal("uncertain credential revived")
				}
			}
			s, _, err := Open(dir)
			must(t, err)
			must(t, s.Close())
		})
	}
}

func TestCorruptRecoveryGuardFailsClosed(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"Action":"init","Action":"init"}`,
		`{"Action":"init"} {}`,
		`{"Action":"revoke","TargetID":"invalid"}`,
		`{"Action":"init","unknown":true}`,
		`{"Action":"init","TargetID":"\ud800"}`,
		"{\"Action\":\"init\",\"TargetID\":\"" + string([]byte{0xff}) + "\"}",
	} {
		t.Run(body, func(t *testing.T) {
			dir, _, _ := initial(t)
			must(t, os.WriteFile(filepath.Join(dir, ".pending"), []byte(body), 0600))
			_, _, err := Open(dir)
			if !errors.Is(err, ErrRecovery) {
				t.Fatalf("startup accepted marker: %v", err)
			}
			_, _, err = Recover(dir, time.Now(), nil, true)
			if !errors.Is(err, ErrState) {
				t.Fatalf("invalid marker cleared: %v", err)
			}
			if _, err = os.Stat(filepath.Join(dir, ".pending")); err != nil {
				t.Fatal("invalid marker removed")
			}
		})
	}
}

func TestUncertainPasswordRecoveryRequiresReset(t *testing.T) {
	for _, action := range []string{"init", "recovery"} {
		t.Run(action, func(t *testing.T) {
			dir, _, _ := initial(t)
			s, _, err := Open(dir)
			must(t, err)
			must(t, s.WriteGuard(Guard{Action: action, ResetAdmin: true}))
			must(t, s.Close())
			_, _, err = Recover(dir, time.Now(), nil, false)
			if !errors.Is(err, ErrRecovery) {
				t.Fatal("unknown password handoff accepted")
			}
			d, password, err := Recover(dir, time.Now(), nil, true)
			must(t, err)
			if !d.Admin.Verify(password) {
				t.Fatal("recovered password invalid")
			}
			if action == "init" {
				for _, r := range d.Credentials {
					if !r.Credential.Revoked {
						t.Fatal("unknown initial secret remained active")
					}
				}
			}
		})
	}
}
