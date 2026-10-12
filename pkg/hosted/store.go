package hosted

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Repository struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Branch         string    `json:"branch"`
	Private        bool      `json:"private"`
	RefreshMinutes int       `json:"refreshMinutes"`
	State          string    `json:"state"`
	Error          string    `json:"error,omitempty"`
	Head           string    `json:"head,omitempty"`
	Generations    []string  `json:"generations,omitempty"`
	LastSync       time.Time `json:"lastSync"`
	NextSync       time.Time `json:"nextSync"`
	Queued         bool      `json:"queued"`
	Archived       bool      `json:"archived,omitempty"`
}
type session struct {
	Login, Token     string
	Expires, Checked time.Time
}
type store struct{ db *sql.DB }

func openStore(dir string) (*store, error) {
	path := filepath.Join(dir, "workspace.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS repositories(id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL, document BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, document BLOB NOT NULL, expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS deliveries(id TEXT PRIMARY KEY, received INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db}, nil
}
func (s *store) repos() ([]Repository, error) {
	rows, err := s.db.Query("SELECT document FROM repositories ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		var b []byte
		var r Repository
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *store) saveRepo(r Repository) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO repositories(id,name,document) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,document=excluded.document", r.ID, r.Name, b)
	return err
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func tokenHash(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }
func (s *store) saveSession(id string, v session) error {
	b, _ := json.Marshal(v)
	_, err := s.db.Exec("INSERT OR REPLACE INTO sessions(id,document,expires) VALUES(?,?,?)", tokenHash(id), b, v.Expires.Unix())
	return err
}
func (s *store) session(id string) (session, error) {
	var b []byte
	var v session
	err := s.db.QueryRow("SELECT document FROM sessions WHERE id=? AND expires>?", tokenHash(id), time.Now().Unix()).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	return v, err
}
func (s *store) logout(id string) { _, _ = s.db.Exec("DELETE FROM sessions WHERE id=?", tokenHash(id)) }
func (s *store) clean() {
	_, _ = s.db.Exec("DELETE FROM sessions WHERE expires<?", time.Now().Unix())
	_, _ = s.db.Exec("DELETE FROM deliveries WHERE received<?", time.Now().Add(-7*24*time.Hour).Unix())
}
func (s *store) admins() []string {
	var b []byte
	var a []string
	if s.db.QueryRow("SELECT value FROM settings WHERE key='admins'").Scan(&b) == nil {
		_ = json.Unmarshal(b, &a)
	}
	return a
}
func (s *store) saveAdmins(a []string) error {
	b, _ := json.Marshal(a)
	_, err := s.db.Exec("INSERT OR REPLACE INTO settings(key,value) VALUES('admins',?)", b)
	return err
}

// Revalidation never recreates a session deleted by a concurrent logout.
func (s *store) refreshSession(id string, v session) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	result, err := s.db.Exec("UPDATE sessions SET document=? WHERE id=?", b, tokenHash(id))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
