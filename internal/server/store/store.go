package store

import (
	"context"
	"database/sql"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"remota.local/remota/internal/auth"
	"remota.local/remota/internal/server/core"
)

var (
	ErrState    = errors.New("management state is missing, damaged or unsupported")
	ErrLocked   = errors.New("management directory is in use")
	ErrRecovery = errors.New("management recovery is required")
)

const schemaVersion = 1
const schemaSQL = `
CREATE TABLE management(id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL CHECK(revision>=1), admin_salt BLOB NOT NULL, admin_sum BLOB NOT NULL);
CREATE TABLE credentials(
 id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK(kind IN ('access','mcp')), name TEXT NOT NULL,
 created_ms INTEGER NOT NULL, expires_ms INTEGER NOT NULL, revoked_ms INTEGER,
 allow_exec INTEGER, operation_id TEXT NOT NULL UNIQUE, rotated_from_id TEXT REFERENCES credentials(id), digest BLOB NOT NULL UNIQUE,
 CHECK((kind='access' AND allow_exec IS NULL) OR (kind='mcp' AND allow_exec IN (0,1)))
);
PRAGMA user_version=1;`

type Data struct {
	Revision    int64
	Admin       auth.PasswordHash
	Credentials []core.CredentialRecord
}
type Handoff struct {
	AdminPassword string `json:"admin_password"`
	Access        string `json:"access_secret"`
	MCP           string `json:"mcp_secret"`
}
type Store struct {
	db   *sql.DB
	lock *os.File
	dir  string
}
type Outcome int

const (
	NotCommitted Outcome = iota
	Committed
	Unknown
)

type Guard struct {
	Action, OperationID, TargetID string
	TargetIDs                     []string
	ResetAdmin                    bool
}
type Change struct {
	ExpectedRevision int64
	Credential       *core.CredentialRecord
	RevokeIDs        []string
	RevokedAt        time.Time
	Admin            *auth.PasswordHash
}

func dbOpen(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p := filepath.ToSlash(absolute)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{"mode": {"rw"}, "_pragma": {"journal_mode(DELETE)", "synchronous(EXTRA)", "foreign_keys(1)", "busy_timeout(1000)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func openLocked(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, ErrState
	}
	fi, err := os.Lstat(filepath.Join(abs, "management.db"))
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ErrState
	}
	f, err := acquireLock(filepath.Join(abs, ".lock"))
	if err != nil {
		return nil, err
	}
	db, err := dbOpen(filepath.Join(abs, "management.db"))
	if err != nil {
		f.Close()
		return nil, ErrState
	}
	s := &Store{db: db, lock: f, dir: abs}
	if err = s.configuration(); err != nil {
		s.Close()
		return nil, ErrState
	}
	return s, nil
}

func Open(dir string) (*Store, Data, error)         { return open(dir, false) }
func OpenRecovery(dir string) (*Store, Data, error) { return open(dir, true) }
func open(dir string, recovery bool) (*Store, Data, error) {
	s, err := openLocked(dir)
	if err != nil {
		return nil, Data{}, err
	}
	if !recovery {
		if _, err = os.Lstat(filepath.Join(s.dir, ".pending")); !errors.Is(err, os.ErrNotExist) {
			s.Close()
			return nil, Data{}, ErrRecovery
		}
	}
	d, err := s.Read()
	if err != nil {
		s.Close()
		return nil, Data{}, err
	}
	return s, d, nil
}

func (s *Store) Close() error { return errors.Join(s.db.Close(), s.lock.Close()) }
func (s *Store) configuration() error {
	for query, want := range map[string]string{"PRAGMA journal_mode": "delete", "PRAGMA synchronous": "3", "PRAGMA foreign_keys": "1"} {
		var got string
		if err := s.db.QueryRow(query).Scan(&got); err != nil || got != want {
			return ErrState
		}
	}
	return nil
}

func (s *Store) Read() (Data, error) {
	var d Data
	var version int
	var integrity string
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		return d, ErrState
	}
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return d, ErrState
	}
	if err := s.db.QueryRow("SELECT revision,admin_salt,admin_sum FROM management WHERE id=1").Scan(&d.Revision, &d.Admin.Salt, &d.Admin.Sum); err != nil {
		return d, ErrState
	}
	if d.Revision < 1 || d.Revision > 9007199254740991 || len(d.Admin.Salt) != 16 || len(d.Admin.Sum) != 32 {
		return d, ErrState
	}
	rows, err := s.db.Query("SELECT id,kind,name,created_ms,expires_ms,revoked_ms,allow_exec,operation_id,rotated_from_id,digest FROM credentials ORDER BY created_ms,id")
	if err != nil {
		return d, ErrState
	}
	defer rows.Close()
	for rows.Next() {
		var r core.CredentialRecord
		v := &r.Credential
		var created, expires int64
		var revoked, allow sql.NullInt64
		var rotated sql.NullString
		var digest []byte
		if err = rows.Scan(&v.ID, &v.Kind, &v.Name, &created, &expires, &revoked, &allow, &v.CreationOperationID, &rotated, &digest); err != nil {
			return d, ErrState
		}
		v.CreatedAt = time.UnixMilli(created).UTC()
		v.ExpiresAt = time.UnixMilli(expires).UTC()
		v.RotatedFromID = rotated.String
		v.Revoked = revoked.Valid
		if revoked.Valid {
			t := time.UnixMilli(revoked.Int64).UTC()
			v.RevokedAt = &t
		}
		v.AllowExec = allow.Int64 == 1
		if len(digest) != 32 || !validRecord(*v) || v.Kind == "access" && allow.Valid || v.Kind == "mcp" && (!allow.Valid || (allow.Int64 != 0 && allow.Int64 != 1)) {
			return d, ErrState
		}
		copy(r.Digest[:], digest)
		d.Credentials = append(d.Credentials, r)
	}
	if rows.Err() != nil || len(d.Credentials) < 2 || len(d.Credentials) > core.MaxCredentialRecords {
		return d, ErrState
	}
	byID := make(map[string]core.Credential)
	for _, r := range d.Credentials {
		byID[r.Credential.ID] = r.Credential
	}
	for _, r := range d.Credentials {
		v := r.Credential
		if v.RotatedFromID != "" {
			p, ok := byID[v.RotatedFromID]
			if !ok || !p.Revoked || p.Kind != v.Kind || p.Name != v.Name || p.AllowExec != v.AllowExec {
				return d, ErrState
			}
		}
	}
	return d, nil
}

func validRecord(v core.Credential) bool {
	if !core.ValidID(v.ID, "crd") || !core.ValidID(v.CreationOperationID, "") || (v.Kind != "access" && v.Kind != "mcp") || !v.ExpiresAt.After(v.CreatedAt) {
		return false
	}
	name, err := core.NormalizeCredentialName(v.Name)
	if err != nil || name != v.Name {
		return false
	}
	if v.RevokedAt != nil && (v.RevokedAt.Year() < 1 || v.RevokedAt.Year() > 9999) {
		return false
	}
	ttl := v.ExpiresAt.Sub(v.CreatedAt)
	if ttl < core.MinCredentialLifetime || ttl > core.MaxCredentialLifetime {
		return false
	}
	return v.CreatedAt.Year() >= 1 && v.ExpiresAt.Year() <= 9999
}

func insert(tx *sql.Tx, r core.CredentialRecord) error {
	v := r.Credential
	var allow any
	var revoked any
	var rotated any
	if v.Kind == "mcp" {
		allow = v.AllowExec
	}
	if v.RevokedAt != nil {
		revoked = v.RevokedAt.UnixMilli()
	}
	if v.RotatedFromID != "" {
		rotated = v.RotatedFromID
	}
	_, err := tx.Exec("INSERT INTO credentials VALUES(?,?,?,?,?,?,?,?,?,?)", v.ID, v.Kind, v.Name, v.CreatedAt.UnixMilli(), v.ExpiresAt.UnixMilli(), revoked, allow, v.CreationOperationID, rotated, r.Digest[:])
	return err
}

func (s *Store) Apply(ctx context.Context, c Change) Outcome {
	if c.ExpectedRevision >= 9007199254740991 {
		return NotCommitted
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NotCommitted
	}
	fail := func() Outcome {
		if tx.Rollback() != nil {
			return Unknown
		}
		return NotCommitted
	}
	r, err := tx.ExecContext(ctx, "UPDATE management SET revision=revision+1 WHERE id=1 AND revision=?", c.ExpectedRevision)
	if err != nil {
		return fail()
	}
	count, err := r.RowsAffected()
	if err != nil || count != 1 {
		return fail()
	}
	for _, id := range c.RevokeIDs {
		if _, err = tx.ExecContext(ctx, "UPDATE credentials SET revoked_ms=COALESCE(revoked_ms,?) WHERE id=?", c.RevokedAt.UnixMilli(), id); err != nil {
			return fail()
		}
	}
	if c.Credential != nil {
		if err = insert(tx, *c.Credential); err != nil {
			return fail()
		}
	}
	if c.Admin != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE management SET admin_salt=?,admin_sum=? WHERE id=1", c.Admin.Salt, c.Admin.Sum); err != nil {
			return fail()
		}
	}
	if err = tx.Commit(); err != nil {
		return Unknown
	}
	return Committed
}

func (s *Store) WriteGuard(g Guard) error {
	if !validGuard(g) {
		return ErrState
	}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, ".pending"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	err = errors.Join(werr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return s.syncDir()
}
func (s *Store) replaceGuard(g Guard) error {
	if !validGuard(g) {
		return ErrState
	}
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, ".pending.next."+auth.ID(""))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	_, werr := f.Write(b)
	if err = errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(path, filepath.Join(s.dir, ".pending")); err != nil {
		return err
	}
	return s.syncDir()
}

func (s *Store) ClearGuard() error {
	if err := os.Remove(filepath.Join(s.dir, ".pending")); err != nil {
		return err
	}
	return s.syncDir()
}
func (s *Store) Pending() (Guard, bool, error) {
	path := filepath.Join(s.dir, ".pending")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Guard{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return Guard{}, false, ErrState
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Guard{}, false, ErrState
	}
	var g Guard
	if jsonv2.Unmarshal(b, &g, jsonv2.RejectUnknownMembers(true)) != nil || !validGuard(g) {
		return g, true, ErrState
	}
	return g, true, nil
}

func validGuard(g Guard) bool {
	switch g.Action {
	case "init", "recovery":
		if g.OperationID != "" || g.TargetID != "" {
			return false
		}
	case "create":
		if !core.ValidID(g.OperationID, "") || g.TargetID != "" {
			return false
		}
	case "rotate":
		if !core.ValidID(g.OperationID, "") || !core.ValidID(g.TargetID, "crd") {
			return false
		}
	case "revoke":
		if g.OperationID != "" || !core.ValidID(g.TargetID, "crd") {
			return false
		}
	default:
		return false
	}
	if len(g.TargetIDs) > core.MaxCredentialRecords {
		return false
	}
	seen := make(map[string]bool, len(g.TargetIDs))
	for _, id := range g.TargetIDs {
		if !core.ValidID(id, "crd") || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func (s *Store) syncDir() error {
	f, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func Initialize(dir string, now time.Time) (Data, Handoff, error) {
	var d Data
	var h Handoff
	if err := os.MkdirAll(dir, 0700); err != nil {
		return d, h, ErrState
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return d, h, ErrState
	}
	lock, err := acquireLock(filepath.Join(dir, ".lock"))
	if err != nil {
		return d, h, err
	}
	defer lock.Close()
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		return d, h, ErrState
	}
	f, err := os.OpenFile(filepath.Join(dir, "management.db"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return d, h, ErrState
	}
	f.Close()
	db, err := dbOpen(filepath.Join(dir, "management.db"))
	if err != nil {
		return d, h, ErrState
	}
	defer db.Close()
	s := &Store{db: db, dir: dir}
	if err = s.configuration(); err != nil {
		return d, h, ErrState
	}
	if err = s.WriteGuard(Guard{Action: "init"}); err != nil {
		return d, h, ErrState
	}
	h = Handoff{auth.Secret(""), auth.Secret("rm_access_"), auth.Secret("rm_mcp_")}
	d.Admin = auth.HashPassword(h.AdminPassword)
	d.Revision = 1
	now = now.UTC().Truncate(time.Millisecond)
	for _, item := range []struct{ kind, secret string }{{"access", h.Access}, {"mcp", h.MCP}} {
		v := core.Credential{ID: auth.ID("crd"), Kind: item.kind, Name: "Initial " + item.kind, AllowExec: item.kind == "mcp", CreatedAt: now, ExpiresAt: now.Add(core.DefaultCredentialLifetime), CreationOperationID: auth.ID("")}
		d.Credentials = append(d.Credentials, core.CredentialRecord{Credential: v, Digest: auth.Digest(item.secret)})
	}
	tx, err := db.Begin()
	if err != nil {
		return Data{}, Handoff{}, ErrState
	}
	defer tx.Rollback()
	if _, err = tx.Exec(schemaSQL); err != nil {
		return Data{}, Handoff{}, ErrState
	}
	if _, err = tx.Exec("INSERT INTO management VALUES(1,?,?,?)", d.Revision, d.Admin.Salt, d.Admin.Sum); err != nil {
		return Data{}, Handoff{}, ErrState
	}
	for _, r := range d.Credentials {
		if err = insert(tx, r); err != nil {
			return Data{}, Handoff{}, ErrState
		}
	}
	if err = tx.Commit(); err != nil {
		return Data{}, Handoff{}, ErrRecovery
	}
	if err = s.ClearGuard(); err != nil {
		return Data{}, Handoff{}, ErrRecovery
	}
	return d, h, nil
}

// Recover checks the actual transaction result and conservatively revokes any
// affected source and any credential whose one-time handoff may have been lost.
func Recover(dir string, now time.Time, revoke []string, resetAdmin bool) (Data, string, error) {
	s, d, err := OpenRecovery(dir)
	if err != nil {
		return Data{}, "", err
	}
	defer s.Close()
	g, pending, err := s.Pending()
	if err != nil {
		return Data{}, "", err
	}
	if pending && (g.Action == "init" || g.ResetAdmin) && !resetAdmin {
		return Data{}, "", ErrRecovery
	}
	ids := make(map[string]bool)
	for _, id := range revoke {
		ids[id] = true
	}
	if pending && g.TargetID != "" {
		ids[g.TargetID] = true
	}
	if pending {
		for _, id := range g.TargetIDs {
			ids[id] = true
		}
	}
	for _, r := range d.Credentials {
		if pending && (g.Action == "init" || r.Credential.CreationOperationID == g.OperationID) {
			ids[r.Credential.ID] = true
		}
	}
	byID := make(map[string]core.Credential)
	for _, r := range d.Credentials {
		byID[r.Credential.ID] = r.Credential
	}
	change := Change{ExpectedRevision: d.Revision, RevokedAt: now.UTC().Truncate(time.Millisecond)}
	for id := range ids {
		v, ok := byID[id]
		if !ok {
			return Data{}, "", ErrState
		}
		if !v.Revoked {
			change.RevokeIDs = append(change.RevokeIDs, id)
		}
	}
	password := ""
	if resetAdmin {
		password = auth.Secret("")
		hash := auth.HashPassword(password)
		change.Admin = &hash
	}
	if len(change.RevokeIDs) > 0 || resetAdmin {
		if !pending {
			if err = s.WriteGuard(Guard{Action: "recovery", TargetIDs: change.RevokeIDs, ResetAdmin: resetAdmin}); err != nil {
				return Data{}, "", ErrRecovery
			}
			pending = true
		} else {
			g.TargetIDs = change.RevokeIDs
			g.ResetAdmin = resetAdmin
			if err = s.replaceGuard(g); err != nil {
				return Data{}, "", ErrRecovery
			}
		}
		if s.Apply(context.Background(), change) != Committed {
			return Data{}, "", ErrRecovery
		}
	}
	if pending {
		if err = s.ClearGuard(); err != nil {
			return Data{}, "", ErrRecovery
		}
	}
	d, err = s.Read()
	return d, password, err
}
