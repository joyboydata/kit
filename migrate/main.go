// Command migrate reads app metadata from PostgreSQL (bun) and writes it into
// a bbolt database at the configured data directory.
//
// Usage:
//
//	APLIQ_DATABASE_DSN="postgres://postgres:postgres@172.18.0.1:5432/apliq?sslmode=disable" \
//	APLIQ_DATA_DIR="/var/apliq/data" \
//	go run ./x/migrate
//
// Run this BEFORE deploying the bbolt-based build. The migration is idempotent
// — it will overwrite any existing bbolt data at the target path.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"go.etcd.io/bbolt"
	_ "github.com/lib/pq"
)

const (
	envDSN     = "APLIQ_DATABASE_DSN"
	envDataDir = "APLIQ_DATA_DIR"
	defaultDir = "./data"
)

type App struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	FilePath  string    `json:"file_path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	FileCount int       `json:"file_count"`
	Status    string    `json:"status"`
	ErrorMsg  string    `json:"error_msg"`
	Icon      string    `json:"icon"`
	Color     string    `json:"color"`
}

type Deployment struct {
	ID        string    `json:"id"`
	AppID     string    `json:"app_id"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	FilePath  string    `json:"file_path"`
	FileCount int       `json:"file_count"`
}

func main() {
	ctx := context.Background()

	dsn := os.Getenv(envDSN)
	if dsn == "" {
		log.Fatalf("set %s environment variable", envDSN)
	}

	dataDir := os.Getenv(envDataDir)
	if dataDir == "" {
		dataDir = defaultDir
	}

	log.Printf("source PostgreSQL: %s", maskDSN(dsn))
	log.Printf("target bbolt dir: %s", dataDir)

	// Open PostgreSQL
	sqldb, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer sqldb.Close()

	if err := sqldb.PingContext(ctx); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}

	// Read apps
	apps, err := readApps(ctx, sqldb)
	if err != nil {
		log.Fatalf("read apps: %v", err)
	}
	log.Printf("read %d apps from PostgreSQL", len(apps))

	// Read deployments
	deployments, err := readDeployments(ctx, sqldb)
	if err != nil {
		log.Fatalf("read deployments: %v", err)
	}
	log.Printf("read %d deployments from PostgreSQL", len(deployments))

	// Write to bbolt
	if err := writeBbolt(dataDir, apps, deployments); err != nil {
		log.Fatalf("write bbolt: %v", err)
	}

	log.Println("migration complete!")
}

func readApps(ctx context.Context, db *sql.DB) ([]App, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, file_path, created_at, updated_at, file_count, status,
		       COALESCE(error_msg, '') AS error_msg
		FROM apps ORDER BY created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("query apps: %w", err)
	}
	defer rows.Close()

	var apps []App
	for rows.Next() {
		var a App
		if err := rows.Scan(&a.ID, &a.Name, &a.FilePath, &a.CreatedAt, &a.UpdatedAt, &a.FileCount, &a.Status, &a.ErrorMsg); err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		// Default values for new fields
		a.Icon = "GlobeAlt"
		a.Color = "slate"
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

func readDeployments(ctx context.Context, db *sql.DB) ([]Deployment, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, app_id, version, created_at, file_path, file_count
		FROM deployments ORDER BY version
	`)
	if err != nil {
		return nil, fmt.Errorf("query deployments: %w", err)
	}
	defer rows.Close()

	var deps []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.Version, &d.CreatedAt, &d.FilePath, &d.FileCount); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

func writeBbolt(dataDir string, apps []App, deployments []Deployment) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	dbPath := dataDir + "/apliq.db"
	db, err := bbolt.Open(dbPath, 0644, nil)
	if err != nil {
		return fmt.Errorf("open bbolt: %w", err)
	}
	defer db.Close()

	return db.Update(func(tx *bbolt.Tx) error {
		appsB, err := tx.CreateBucketIfNotExists([]byte("apps"))
		if err != nil {
			return fmt.Errorf("create apps bucket: %w", err)
		}
		deployB, err := tx.CreateBucketIfNotExists([]byte("deployments"))
		if err != nil {
			return fmt.Errorf("create deployments bucket: %w", err)
		}
		idxB, err := tx.CreateBucketIfNotExists([]byte("idx_deployments_by_app"))
		if err != nil {
			return fmt.Errorf("create idx bucket: %w", err)
		}

		for _, a := range apps {
			data, err := json.Marshal(a)
			if err != nil {
				return fmt.Errorf("marshal app %s: %w", a.ID, err)
			}
			if err := appsB.Put([]byte(a.ID), data); err != nil {
				return fmt.Errorf("put app %s: %w", a.ID, err)
			}
		}

		for _, d := range deployments {
			data, err := json.Marshal(d)
			if err != nil {
				return fmt.Errorf("marshal deployment %s: %w", d.ID, err)
			}
			if err := deployB.Put([]byte(d.ID), data); err != nil {
				return fmt.Errorf("put deployment %s: %w", d.ID, err)
			}
			idxKey := fmt.Sprintf("%s/%d", d.AppID, d.Version)
			if err := idxB.Put([]byte(idxKey), []byte(d.ID)); err != nil {
				return fmt.Errorf("put idx %s: %w", idxKey, err)
			}
		}

		return nil
	})
}

func maskDSN(dsn string) string {
	// Show the host part only, mask credentials
	return dsn // for now; it's a local dev tool
}