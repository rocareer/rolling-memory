// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package thinclient

import (
	"database/sql"
	"runtime"

	"github.com/rocareer/rolling-memory/internal/zhao/rm/closedset"
	"github.com/rocareer/rolling-memory/internal/zhao/rm/cluster"
)

// EnsureZhaoRMSQLiteSchema ensures the canonical Zhao/RM tables (`rm_docs` & `rm_terms`)
// are present in the client SQLite database.
func EnsureZhaoRMSQLiteSchema(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(cluster.SQLiteSchema)
	return err
}

// ZhaoRMSchemaContracts forces the compiler/linker to retain all Zhao/RM engine
// and dual-store schema definitions in the binary.
func ZhaoRMSchemaContracts() {
	runtime.KeepAlive(cluster.SQLiteSchema)
	runtime.KeepAlive(cluster.PostgresCipherSchema)
	runtime.KeepAlive(cluster.PostgresPlainSchema)
	runtime.KeepAlive(cluster.ErrCipherStoreRejectsPlaintext)
	runtime.KeepAlive(cluster.ErrPlainStoreRequiresPlaintext)
	runtime.KeepAlive(closedset.StandardClosedSetTable)
	runtime.KeepAlive(closedset.UniversalSchemaClosedSetTable)
	var s *cluster.Store
	_ = s.DeleteDocs()
	var ds *cluster.DualClusterStore
	runtime.KeepAlive(ds)
}
