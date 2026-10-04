package histdb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
