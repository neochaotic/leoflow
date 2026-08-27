//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/domain"
	"github.com/neochaotic/leoflow/internal/storage"
)

// TestCreateDagRunPersistsConf proves the conf column is written at create time
// and reads back unchanged. The conf -> LEOFLOW_PARAMS -> task params pipeline
// reads the persisted column, so a run created with no conf persisted was the
// break that left trigger-time params always empty.
func TestCreateDagRunPersistsConf(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the conf round-trip test")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	dagID := fmt.Sprintf("conf_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register version: created=%v err=%v", created, rerr)
	}

	conf := []byte(`{"date":"2026-01-01","limit":10}`)
	created, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual",
		LogicalDate: time.Now().UTC(), Conf: conf,
	})
	if err != nil {
		t.Fatalf("create run with conf: %v", err)
	}
	if string(created.Conf) != string(conf) {
		t.Errorf("created run conf = %s, want %s", created.Conf, conf)
	}

	got, err := repo.GetDagRun(ctx, "default", dagID, "r1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if string(got.Conf) != string(conf) {
		t.Errorf("read-back conf = %s, want %s (the column must round-trip)", got.Conf, conf)
	}
}

// TestCreateDagRunDefaultsConfWhenAbsent proves a run created without conf keeps
// the empty-object default rather than a NULL that downstream params handling
// would have to special-case.
func TestCreateDagRunDefaultsConfWhenAbsent(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the conf default test")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	dagID := fmt.Sprintf("confdef_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register version: created=%v err=%v", created, rerr)
	}

	if _, rerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); rerr != nil {
		t.Fatalf("create run without conf: %v", rerr)
	}
	got, err := repo.GetDagRun(ctx, "default", dagID, "r1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if string(got.Conf) != "{}" {
		t.Errorf("conf = %s, want {} default when no conf supplied", got.Conf)
	}
}
