// Package histdb stores RESH history records in a SQLite database.
//
// Values that repeat across many records (session/device info, command lines,
// paths, git remotes) are stored once and referenced by ID which keeps
// the database much smaller than the original JSON lines history file.
package histdb

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/curusarn/resh/internal/futil"
	"github.com/curusarn/resh/internal/recio"
	"github.com/curusarn/resh/record"
	"github.com/google/uuid"
	"go.uber.org/zap"

	// pure Go SQLite driver - keeps the build CGO-free
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

-- interned strings: command lines, paths, git remotes
CREATE TABLE IF NOT EXISTS strings (
	id    INTEGER PRIMARY KEY,
	value TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS sessions (
	id           INTEGER PRIMARY KEY,
	session_uuid TEXT NOT NULL,
	device_id    TEXT NOT NULL,
	device       TEXT NOT NULL,
	home         TEXT NOT NULL,
	UNIQUE (session_uuid, device_id, device, home)
);

CREATE TABLE IF NOT EXISTS records (
	id         INTEGER PRIMARY KEY,
	-- 16 byte BLOB for UUIDs, TEXT otherwise (no declared type => no type conversion)
	record_id  NOT NULL,
	session    INTEGER NOT NULL REFERENCES sessions(id),
	cmd        INTEGER NOT NULL REFERENCES strings(id),
	pwd        INTEGER NOT NULL REFERENCES strings(id),
	real_pwd   INTEGER NOT NULL REFERENCES strings(id),
	git_remote INTEGER NOT NULL REFERENCES strings(id),
	exit_code  INTEGER NOT NULL,
	time       TEXT NOT NULL,
	duration   TEXT NOT NULL,
	flags      INTEGER NOT NULL DEFAULT 0
);
`

// record flags stored as a bitmask
const (
	flagDeleted = 1 << iota
	flagFavorite
	flagPartOne
	flagPartsNotMerged
	flagSessionExit
)

const metaKeyJSONLImported = "jsonl_imported"

// ErrAlreadyImported is returned when the JSON lines history was already imported into the database
var ErrAlreadyImported = errors.New("JSON history was already imported")

// DB is a RESH history database
type DB struct {
	db *sql.DB

	// mu serializes writes and guards the ID caches
	mu      sync.Mutex
	strIDs  map[string]int64
	sessIDs map[sessionKey]int64
}

type sessionKey struct {
	sessionID string
	deviceID  string
	device    string
	home      string
}

// Open opens (and creates if needed) the history database at path
func Open(path string) (*DB, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)" +
		// take the write lock when a transaction begins so that concurrent processes
		// (daemon, install-utils) wait for each other instead of failing on lock upgrade
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("could not open history database: %w", err)
	}
	// SQLite allows a single writer - one connection avoids SQLITE_BUSY between our own goroutines
	db.SetMaxOpenConns(1)

	h := &DB{
		db:      db,
		strIDs:  map[string]int64{},
		sessIDs: map[sessionKey]int64{},
	}
	err = h.migrate()
	if err != nil {
		db.Close()
		return nil, err
	}
	return h, nil
}

// Close closes the database
func (h *DB) Close() error {
	return h.db.Close()
}

func (h *DB) migrate() error {
	var version int
	err := h.db.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		return fmt.Errorf("could not read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("history database schema version %d is newer than supported version %d - please update RESH", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	_, err = h.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("could not create history database schema: %w", err)
	}
	_, err = h.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	if err != nil {
		return fmt.Errorf("could not set schema version: %w", err)
	}
	return nil
}

// Insert appends records to the history
func (h *DB) Insert(recs ...record.V1) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	tx, err := h.db.Begin()
	if err != nil {
		return fmt.Errorf("could not begin transaction: %w", err)
	}
	// on failure the caches may reference rows that were rolled back
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
			h.strIDs = map[string]int64{}
			h.sessIDs = map[sessionKey]int64{}
		}
	}()

	err = h.insertTx(tx, recs)
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("could not commit records: %w", err)
	}
	ok = true
	return nil
}

func (h *DB) insertTx(tx *sql.Tx, recs []record.V1) error {
	stmt, err := tx.Prepare(`INSERT INTO records
		(record_id, session, cmd, pwd, real_pwd, git_remote, exit_code, time, duration, flags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("could not prepare insert: %w", err)
	}
	defer stmt.Close()

	for i := range recs {
		rec := &recs[i]
		sess, err := h.sessionID(tx, sessionKey{rec.SessionID, rec.DeviceID, rec.Device, rec.Home})
		if err != nil {
			return err
		}
		var strs [4]int64
		for j, s := range []string{rec.CmdLine, rec.Pwd, rec.RealPwd, rec.GitOriginRemote} {
			strs[j], err = h.stringID(tx, s)
			if err != nil {
				return err
			}
		}
		_, err = stmt.Exec(encodeRecordID(rec.RecordID), sess, strs[0], strs[1], strs[2], strs[3],
			rec.ExitCode, rec.Time, rec.Duration, encodeFlags(rec))
		if err != nil {
			return fmt.Errorf("could not insert record: %w", err)
		}
	}
	return nil
}

func (h *DB) stringID(tx *sql.Tx, s string) (int64, error) {
	if id, found := h.strIDs[s]; found {
		return id, nil
	}
	var id int64
	err := tx.QueryRow("SELECT id FROM strings WHERE value = ?", s).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var res sql.Result
		res, err = tx.Exec("INSERT INTO strings (value) VALUES (?)", s)
		if err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return 0, fmt.Errorf("could not store string: %w", err)
	}
	h.strIDs[s] = id
	return id, nil
}

func (h *DB) sessionID(tx *sql.Tx, k sessionKey) (int64, error) {
	if id, found := h.sessIDs[k]; found {
		return id, nil
	}
	var id int64
	err := tx.QueryRow(`SELECT id FROM sessions
		WHERE session_uuid = ? AND device_id = ? AND device = ? AND home = ?`,
		k.sessionID, k.deviceID, k.device, k.home).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var res sql.Result
		res, err = tx.Exec("INSERT INTO sessions (session_uuid, device_id, device, home) VALUES (?, ?, ?, ?)",
			k.sessionID, k.deviceID, k.device, k.home)
		if err == nil {
			id, err = res.LastInsertId()
		}
	}
	if err != nil {
		return 0, fmt.Errorf("could not store session: %w", err)
	}
	h.sessIDs[k] = id
	return id, nil
}

// All returns all records in insertion order
func (h *DB) All() ([]record.V1, error) {
	rows, err := h.db.Query(`SELECT
			r.record_id, s.session_uuid, s.device_id, s.device, s.home,
			c.value, p.value, rp.value, g.value,
			r.exit_code, r.time, r.duration, r.flags
		FROM records r
		JOIN sessions s ON s.id = r.session
		JOIN strings c ON c.id = r.cmd
		JOIN strings p ON p.id = r.pwd
		JOIN strings rp ON rp.id = r.real_pwd
		JOIN strings g ON g.id = r.git_remote
		ORDER BY r.id`)
	if err != nil {
		return nil, fmt.Errorf("could not query records: %w", err)
	}
	defer rows.Close()

	var recs []record.V1
	for rows.Next() {
		var rec record.V1
		var recordID any
		var flags int
		err = rows.Scan(&recordID, &rec.SessionID, &rec.DeviceID, &rec.Device, &rec.Home,
			&rec.CmdLine, &rec.Pwd, &rec.RealPwd, &rec.GitOriginRemote,
			&rec.ExitCode, &rec.Time, &rec.Duration, &flags)
		if err != nil {
			return nil, fmt.Errorf("could not read record: %w", err)
		}
		rec.RecordID = decodeRecordID(recordID)
		decodeFlags(&rec, flags)
		recs = append(recs, rec)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("could not read records: %w", err)
	}
	return recs, nil
}

// Count returns number of records
func (h *DB) Count() (int, error) {
	var n int
	err := h.db.QueryRow("SELECT COUNT(*) FROM records").Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("could not count records: %w", err)
	}
	return n, nil
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// JSONLImported reports whether the JSON lines history file was already imported
func (h *DB) JSONLImported() (bool, error) {
	return jsonlImported(h.db)
}

func jsonlImported(q queryRower) (bool, error) {
	var v string
	err := q.QueryRow("SELECT value FROM meta WHERE key = ?", metaKeyJSONLImported).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not read metadata: %w", err)
	}
	return true, nil
}

// ImportResult describes the outcome of ImportJSONLFile
type ImportResult struct {
	// Imported is the number of records written to the database
	Imported int
	// Dropped is the number of lines which could not be decoded
	Dropped int
	// FileFound is false when there was no JSON history file to import
	FileFound bool
}

// ImportJSONLFile imports the JSON lines history file at path into the database
// The file is only read, never modified, so it stays as a backup.
// A missing file is treated as empty history. Up to maxErrors undecodable lines are dropped.
// Returns ErrAlreadyImported if the JSON history was imported before.
func (h *DB) ImportJSONLFile(sugar *zap.SugaredLogger, path string, maxErrors int) (ImportResult, error) {
	var res ImportResult
	// avoid reading a potentially large file when there is nothing to do
	imported, err := h.JSONLImported()
	if err != nil {
		return res, err
	}
	if imported {
		return res, ErrAlreadyImported
	}
	res.FileFound, err = futil.FileExists(path)
	if err != nil {
		return res, fmt.Errorf("could not check history file: %w", err)
	}
	var recs []record.V1
	if res.FileFound {
		rio := recio.New(sugar)
		var decodeErrs []error
		recs, decodeErrs, err = rio.ReadFile(path)
		if err != nil {
			return res, fmt.Errorf("could not read history file: %w", err)
		}
		res.Dropped = len(decodeErrs)
		if res.Dropped > maxErrors {
			return res, fmt.Errorf("history file has too many lines that could not be decoded (%d), last error: %w",
				res.Dropped, decodeErrs[len(decodeErrs)-1])
		}
	}
	err = h.ImportJSONL(recs, path)
	if err != nil {
		return res, err
	}
	res.Imported = len(recs)
	return res, nil
}

// ImportJSONL inserts records from the JSON lines history and marks the import as done
// The check, records and the marker are in one transaction so the import is never applied twice.
// Returns ErrAlreadyImported if the JSON history was imported before.
func (h *DB) ImportJSONL(recs []record.V1, source string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	tx, err := h.db.Begin()
	if err != nil {
		return fmt.Errorf("could not begin transaction: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
			h.strIDs = map[string]int64{}
			h.sessIDs = map[sessionKey]int64{}
		}
	}()
	imported, err := jsonlImported(tx)
	if err != nil {
		return err
	}
	if imported {
		return ErrAlreadyImported
	}
	err = h.insertTx(tx, recs)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", metaKeyJSONLImported, source)
	if err != nil {
		return fmt.Errorf("could not write metadata: %w", err)
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("could not commit import: %w", err)
	}
	ok = true
	return nil
}

func encodeRecordID(id string) any {
	u, err := uuid.Parse(id)
	if err != nil || u.String() != id {
		// not a canonical UUID - store as is so it round-trips exactly
		return id
	}
	return u[:]
}

func decodeRecordID(v any) string {
	switch id := v.(type) {
	case []byte:
		u, err := uuid.FromBytes(id)
		if err != nil {
			return string(id)
		}
		return u.String()
	case string:
		return id
	default:
		return ""
	}
}

func encodeFlags(rec *record.V1) int {
	flags := 0
	for _, f := range []struct {
		set  bool
		flag int
	}{
		{rec.Deleted, flagDeleted},
		{rec.Favorite, flagFavorite},
		{rec.PartOne, flagPartOne},
		{rec.PartsNotMerged, flagPartsNotMerged},
		{rec.SessionExit, flagSessionExit},
	} {
		if f.set {
			flags |= f.flag
		}
	}
	return flags
}

func decodeFlags(rec *record.V1, flags int) {
	rec.Deleted = flags&flagDeleted != 0
	rec.Favorite = flags&flagFavorite != 0
	rec.PartOne = flags&flagPartOne != 0
	rec.PartsNotMerged = flags&flagPartsNotMerged != 0
	rec.SessionExit = flags&flagSessionExit != 0
}
