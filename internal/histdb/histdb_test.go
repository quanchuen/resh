package histdb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/curusarn/resh/internal/recio"
	"github.com/curusarn/resh/record"
	"go.uber.org/zap"
)

func testRecord(i int) record.V1 {
	return record.V1{
		CmdLine:         fmt.Sprintf("git commit -m 'fix %d'", i%300),
		ExitCode:        i % 3,
		DeviceID:        "3f2a9c1e-7b4d-4e8a-9f1c-2d3e4f5a6b7c",
		SessionID:       fmt.Sprintf("8c1d2e3f-4a5b-6c7d-8e9f-%012d", i/50),
		RecordID:        fmt.Sprintf("1a2b3c4d-5e6f-7a8b-9c0d-%012d", i),
		Home:            "/home/user",
		Pwd:             fmt.Sprintf("/home/user/src/project%d", i%7),
		RealPwd:         fmt.Sprintf("/home/user/src/project%d", i%7),
		Device:          "laptop",
		GitOriginRemote: "git@github.com:user/project.git",
		Time:            fmt.Sprintf("%d.12", 1700000000+i*37),
		Duration:        "0.04",
	}
}

func openTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestRoundTrip(t *testing.T) {
	db, _ := openTestDB(t)

	recs := []record.V1{
		testRecord(0),
		testRecord(1),
		// legacy records can have empty or non-UUID record IDs and empty fields
		{CmdLine: "ls", RecordID: "", Time: "1.00"},
		{CmdLine: "ls", RecordID: "123", Time: "2.00"},
		{CmdLine: "echo 'ünïcode' \"quotes\"\n", RecordID: "not-a-uuid", PartOne: true, SessionExit: true},
		{CmdLine: "fav", Deleted: true, Favorite: true, PartsNotMerged: true},
		// uppercase UUID is not canonical - must round-trip exactly
		{CmdLine: "up", RecordID: "1A2B3C4D-5E6F-7A8B-9C0D-000000000001"},
	}
	for _, r := range recs {
		if err := db.Insert(r); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	got, err := db.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if !reflect.DeepEqual(got, recs) {
		t.Fatalf("records differ after round trip\n got: %+v\nwant: %+v", got, recs)
	}
	n, err := db.Count()
	if err != nil || n != len(recs) {
		t.Fatalf("Count = %d, %v; want %d", n, err, len(recs))
	}
}

func TestReopen(t *testing.T) {
	db, path := openTestDB(t)
	if err := db.Insert(testRecord(0), testRecord(1)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	// cached IDs are gone - inserting existing strings and sessions must reuse rows
	if err := db2.Insert(testRecord(2)); err != nil {
		t.Fatal(err)
	}
	got, err := db2.All()
	if err != nil {
		t.Fatal(err)
	}
	want := []record.V1{testRecord(0), testRecord(1), testRecord(2)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	var sessions int
	db2.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("sessions = %d, want 1", sessions)
	}
}

func TestImportJSONL(t *testing.T) {
	db, _ := openTestDB(t)
	imported, err := db.JSONLImported()
	if err != nil || imported {
		t.Fatalf("JSONLImported = %v, %v; want false", imported, err)
	}
	recs := []record.V1{testRecord(0), testRecord(1)}
	if err := db.ImportJSONL(recs, "history.reshjson"); err != nil {
		t.Fatal(err)
	}
	imported, err = db.JSONLImported()
	if err != nil || !imported {
		t.Fatalf("JSONLImported = %v, %v; want true", imported, err)
	}
	got, _ := db.All()
	if !reflect.DeepEqual(got, recs) {
		t.Fatalf("got %+v\nwant %+v", got, recs)
	}
}

func TestSmallerThanJSONL(t *testing.T) {
	db, dbPath := openTestDB(t)
	jsonlPath := filepath.Join(t.TempDir(), "history.reshjson")

	var recs []record.V1
	for i := 0; i < 10000; i++ {
		recs = append(recs, testRecord(i))
	}
	if err := db.Insert(recs...); err != nil {
		t.Fatal(err)
	}
	// flush WAL into the main database file
	if _, err := db.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	rio := recio.New(zap.NewNop().Sugar())
	if err := rio.OverwriteFile(jsonlPath, recs); err != nil {
		t.Fatal(err)
	}
	dbInfo, _ := os.Stat(dbPath)
	jsonlInfo, _ := os.Stat(jsonlPath)
	t.Logf("10000 records: JSONL %d bytes, SQLite %d bytes", jsonlInfo.Size(), dbInfo.Size())
	if dbInfo.Size() >= jsonlInfo.Size()/2 {
		t.Fatalf("database is not substantially smaller than JSONL")
	}
}

const (
	v1Line     = `v1{"cmdLine":"make test","exitCode":2,"deviceID":"3f2a9c1e-7b4d-4e8a-9f1c-2d3e4f5a6b7c","sessionID":"s1","recordID":"1a2b3c4d-5e6f-7a8b-9c0d-000000000001","home":"/home/u","pwd":"/src","realPwd":"/src","device":"lap","gitOriginRemote":"git@x:y.git","time":"1700000001.50","duration":"3.20"}` + "\n"
	legacyLine = `{"cmdLine":"ls -la","exitCode":0,"sessionId":"s0","recordId":"r0","home":"/home/u","pwd":"/tmp","realPwd":"/tmp","host":"lap","reshUuid":"3f2a9c1e-7b4d-4e8a-9f1c-2d3e4f5a6b7c","realtimeBefore":1600000000.5,"realtimeDuration":0.25,"partsMerged":true}` + "\n"
	badLine    = "this line is corrupted\n"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.reshjson")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportJSONLFile(t *testing.T) {
	sugar := zap.NewNop().Sugar()
	content := legacyLine + badLine + v1Line
	path := writeFile(t, content)
	rio := recio.New(sugar)
	want, _, err := rio.ReadFile(path)
	if err != nil || len(want) != 2 {
		t.Fatalf("test setup: ReadFile = %d records, %v", len(want), err)
	}

	db, _ := openTestDB(t)
	res, err := db.ImportJSONLFile(sugar, path, 3)
	if err != nil {
		t.Fatalf("ImportJSONLFile: %v", err)
	}
	if res != (ImportResult{Imported: 2, Dropped: 1, FileFound: true}) {
		t.Fatalf("result = %+v", res)
	}
	got, _ := db.All()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imported records differ\n got: %+v\nwant: %+v", got, want)
	}
	// the JSON history is the backup - it must not be modified
	after, _ := os.ReadFile(path)
	if string(after) != content {
		t.Fatalf("history file was modified")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("unexpected backup file created")
	}

	// second import is refused and adds nothing
	_, err = db.ImportJSONLFile(sugar, path, 3)
	if !errors.Is(err, ErrAlreadyImported) {
		t.Fatalf("second import err = %v, want ErrAlreadyImported", err)
	}
	if err := db.ImportJSONL(want, path); !errors.Is(err, ErrAlreadyImported) {
		t.Fatalf("ImportJSONL after import err = %v, want ErrAlreadyImported", err)
	}
	if n, _ := db.Count(); n != 2 {
		t.Fatalf("Count = %d after repeated import, want 2", n)
	}
}

func TestImportJSONLFileMissing(t *testing.T) {
	db, _ := openTestDB(t)
	res, err := db.ImportJSONLFile(zap.NewNop().Sugar(), filepath.Join(t.TempDir(), "nope"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if res != (ImportResult{}) {
		t.Fatalf("result = %+v", res)
	}
	if imported, _ := db.JSONLImported(); !imported {
		t.Fatal("missing history file should still mark import as done")
	}
}

func TestImportJSONLFileTooManyErrors(t *testing.T) {
	path := writeFile(t, v1Line+badLine+badLine+badLine+badLine)
	db, _ := openTestDB(t)
	_, err := db.ImportJSONLFile(zap.NewNop().Sugar(), path, 3)
	if err == nil {
		t.Fatal("expected error")
	}
	if n, _ := db.Count(); n != 0 {
		t.Fatalf("Count = %d after failed import, want 0", n)
	}
	if imported, _ := db.JSONLImported(); imported {
		t.Fatal("failed import must not be marked as done")
	}
}

// daemon and install-utils can import at the same time - history must be imported exactly once
func TestImportJSONLFileConcurrentProcesses(t *testing.T) {
	var content string
	for i := 0; i < 2000; i++ {
		content += v1Line
	}
	path := writeFile(t, content)
	dbPath := filepath.Join(t.TempDir(), "history.db")

	const n = 4
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// separate handles behave like separate processes (separate connections and locks)
			db, err := Open(dbPath)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			_, err = db.ImportJSONLFile(zap.NewNop().Sugar(), path, 3)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	imports := 0
	for err := range errs {
		switch {
		case err == nil:
			imports++
		case errors.Is(err, ErrAlreadyImported):
		default:
			t.Fatalf("import failed: %v", err)
		}
	}
	if imports != 1 {
		t.Fatalf("history imported %d times, want 1", imports)
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if c, _ := db.Count(); c != 2000 {
		t.Fatalf("Count = %d, want 2000", c)
	}
}
