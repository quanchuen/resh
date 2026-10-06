package main

import (
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/curusarn/resh/internal/cfg"
	"github.com/curusarn/resh/internal/datadir"
	"github.com/curusarn/resh/internal/futil"
	"github.com/curusarn/resh/internal/histdb"
	"github.com/curusarn/resh/internal/output"
)

func printRecoveryInfo(rf *futil.RestorableFile) {
	fmt.Printf(" -> Backup is '%s'\n"+
		" -> Original file location is '%s'\n"+
		" -> Please copy the backup over the file - run: cp -f '%s' '%s'\n\n",
		rf.PathBackup, rf.Path,
		rf.PathBackup, rf.Path,
	)
}

func migrateAll(out *output.Output) {
	cfgBackup, err := migrateConfig(out)
	if err != nil {
		// out.InfoE("Failed to update config file format", err)
		out.FatalE("Failed to update config file format", err)
	}
	err = migrateHistory(out)
	if err != nil {
		errHist := err
		out.InfoE("Failed to update RESH history", errHist)
		out.Info("Restoring config from backup ...")
		err = cfgBackup.Restore()
		if err != nil {
			out.InfoE("FAILED TO RESTORE CONFIG FROM BACKUP!", err)
			printRecoveryInfo(cfgBackup)
		} else {
			out.Info("Config file was restored successfully")
		}
		out.FatalE("Failed to update history", errHist)
	}
}

func migrateConfig(out *output.Output) (*futil.RestorableFile, error) {
	cfgPath, err := cfg.GetPath()
	if err != nil {
		return nil, fmt.Errorf("could not get config file path: %w", err)
	}

	// Touch config to get rid of edge-cases
	created, err := futil.TouchFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("failed to touch config file: %w", err)
	}

	// Backup
	backup, err := futil.BackupFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("could not backup config file: %w", err)
	}

	// Migrate
	changes, err := cfg.Migrate()
	if err != nil {
		// Restore
		errMigrate := err
		errMigrateWrap := fmt.Errorf("failed to update config file: %w", errMigrate)
		out.InfoE("Failed to update config file format", errMigrate)
		out.Info("Restoring config from backup ...")
		err = backup.Restore()
		if err != nil {
			out.InfoE("FAILED TO RESTORE CONFIG FROM BACKUP!", err)
			printRecoveryInfo(backup)
		} else {
			out.Info("Config file was restored successfully")
		}
		// We are returning the root cause - there might be a better solution how to report the errors
		return nil, errMigrateWrap
	}
	if created {
		out.Info(fmt.Sprintf("RESH config created in '%s'", cfgPath))
	} else if changes {
		out.Info("RESH config file format has changed since last update - your config was updated to reflect the changes.")
	}
	return backup, nil
}

func migrateHistory(out *output.Output) error {
	dataDir, err := datadir.MakePath()
	if err != nil {
		return fmt.Errorf("failed to get data directory: %w", err)
	}
	dbPath := path.Join(dataDir, datadir.HistoryDBFileName)
	db, err := histdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	imported, err := db.JSONLImported()
	if err != nil {
		return err
	}
	if imported {
		// history is already in the database - nothing to migrate
		return nil
	}
	err = migrateHistoryLocation(out)
	if err != nil {
		return fmt.Errorf("failed to move history to new location %w", err)
	}
	return migrateHistoryToDB(out, db, path.Join(dataDir, datadir.HistoryFileName), dbPath)
}

// migrateHistoryToDB imports JSON lines history into the history database
// The JSON history file is left unchanged so it serves as a backup
func migrateHistoryToDB(out *output.Output, db *histdb.DB, historyPath, dbPath string) error {
	out.Info(fmt.Sprintf("Moving RESH history to database '%s' ...", dbPath))
	res, err := db.ImportJSONLFile(out.Logger.Sugar(), historyPath, 3)
	if errors.Is(err, histdb.ErrAlreadyImported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to import history into database: %w", err)
	}
	if !res.FileFound {
		// this is normal during new installation
		out.Info("No RESH history file found - created empty history database")
		return nil
	}
	if res.Dropped > 0 {
		out.Info(fmt.Sprintf("Skipped %d history records that could not be read", res.Dropped))
	}
	out.Info(fmt.Sprintf("Moved %d history records to database", res.Imported))
	out.Info(fmt.Sprintf("Original history file '%s' was left unchanged as a backup - you can delete it", historyPath))
	return nil
}

// Find first existing history and use it
// Don't bother with merging of history in multiple locations - it could get messy and it shouldn't be necessary
func migrateHistoryLocation(out *output.Output) error {
	dataDir, err := datadir.MakePath()
	if err != nil {
		return fmt.Errorf("failed to get data directory: %w", err)
	}
	historyPath := path.Join(dataDir, datadir.HistoryFileName)

	exists, err := futil.FileExists(historyPath)
	if err != nil {
		return fmt.Errorf("failed to check history file: %w", err)
	}
	if exists {
		// TODO: get rid of this output (later)
		out.Info(fmt.Sprintf("Found history file in '%s' - nothing to move", historyPath))
		return nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	legacyHistoryPaths := []string{
		path.Join(homeDir, ".resh_history.json"),
		path.Join(homeDir, ".resh/history.json"),
	}
	for _, path := range legacyHistoryPaths {
		exists, err = futil.FileExists(path)
		if err != nil {
			return fmt.Errorf("failed to check existence of legacy history file: %w", err)
		}
		if exists {
			// TODO: maybe get rid of this output later
			out.Info(fmt.Sprintf("Copying history file to new location: '%s' -> '%s' ...", path, historyPath))
			err = futil.CopyFile(path, historyPath)
			if err != nil {
				return fmt.Errorf("failed to copy history file: %w", err)
			}
			out.Info("History file copied successfully")
			return nil
		}
	}
	// out.Info("WARNING: No RESH history file found (this is normal during new installation)")
	return nil
}
