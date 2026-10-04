package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/curusarn/resh/internal/datadir"
	"github.com/curusarn/resh/internal/histdb"
	"github.com/curusarn/resh/internal/output"
	"go.uber.org/zap"
)

const (
	v1Line     = `v1{"cmdLine":"make test","exitCode":2,"deviceID":"d","sessionID":"s1","recordID":"r1","home":"/home/u","pwd":"/src","realPwd":"/src","device":"lap","gitOriginRemote":"","time":"1700000001.50","duration":"3.20"}` + "\n"
	legacyLine = `{"cmdLine":"ls -la","exitCode":0,"sessionId":"s0","recordId":"r0","home":"/home/u","pwd":"/tmp","realPwd":"/tmp","host":"lap","reshUuid":"d","realtimeBefore":1600000000.5,"realtimeDuration":0.25,"partsMerged":true}` + "\n"
	badLine    = "corrupted\n"
)

// setupDirs points RESH data dir and home dir to temporary directories
func setupDirs(t *testing.T) (dataDir, homeDir string) {
	t.Helper()
	homeDir = t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_DATA_HOME", xdg)
	dataDir = filepath.Join(xdg, "resh")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	return dataDir, homeDir
}

func testOutput() *output.Output {
	return output.New(zap.NewNop(), "test ERROR")
}

func openDB(t *testing.T, dataDir string) *histdb.DB {
	t.Helper()
	db, err := histdb.Open(filepath.Join(dataDir, datadir.HistoryDBFileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateHistoryToDB(t *testing.T) {
	dataDir, _ := setupDirs(t)
	jsonlPath := filepath.Join(dataDir, datadir.HistoryFileName)
	content := legacyLine + badLine + v1Line
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	for run := 1; run <= 2; run++ {
		if err := migrateHistory(testOutput()); err != nil {
			t.Fatalf("run %d: migrateHistory: %v", run, err)
		}
	}

	db := openDB(t, dataDir)
	recs, err := db.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].CmdLine != "ls -la" || recs[1].CmdLine != "make test" {
		t.Fatalf("records = %+v, want ls -la and make test once each", recs)
	}
	after, _ := os.ReadFile(jsonlPath)
	if string(after) != content {
		t.Fatal("JSON history file was modified - it should be left as a backup")
	}
}

func TestMigrateHistoryFromLegacyLocation(t *testing.T) {
	dataDir, homeDir := setupDirs(t)
	if err := os.WriteFile(filepath.Join(homeDir, ".resh_history.json"), []byte(legacyLine), 0644); err != nil {
		t.Fatal(err)
	}
	if err := migrateHistory(testOutput()); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, dataDir)
	if n, _ := db.Count(); n != 1 {
		t.Fatalf("Count = %d, want 1", n)
	}
}

func TestMigrateHistoryNewInstall(t *testing.T) {
	dataDir, _ := setupDirs(t)
	if err := migrateHistory(testOutput()); err != nil {
		t.Fatal(err)
	}
	db := openDB(t, dataDir)
	if n, _ := db.Count(); n != 0 {
		t.Fatalf("Count = %d, want 0", n)
	}
	if imported, _ := db.JSONLImported(); !imported {
		t.Fatal("new install should be marked as migrated")
	}
}

func TestMigrateHistoryTooManyErrors(t *testing.T) {
	dataDir, _ := setupDirs(t)
	jsonlPath := filepath.Join(dataDir, datadir.HistoryFileName)
	if err := os.WriteFile(jsonlPath, []byte(v1Line+badLine+badLine+badLine+badLine), 0644); err != nil {
		t.Fatal(err)
	}
	if err := migrateHistory(testOutput()); err == nil {
		t.Fatal("expected error")
	}
	db := openDB(t, dataDir)
	if imported, _ := db.JSONLImported(); imported {
		t.Fatal("failed migration must not be marked as done")
	}
	if n, _ := db.Count(); n != 0 {
		t.Fatalf("Count = %d after failed migration, want 0", n)
	}
}
