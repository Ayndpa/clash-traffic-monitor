package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestQueryAggregate(t *testing.T) {
	svc := newTestService(t)

	insertTestLogs(t, svc.db, []trafficLog{
		{Timestamp: 1000, SourceIP: "192.168.1.2", Host: "a.com", Process: "chrome", Outbound: "NodeA", Upload: 100, Download: 200},
		{Timestamp: 1500, SourceIP: "192.168.1.2", Host: "b.com", Process: "chrome", Outbound: "NodeA", Upload: 50, Download: 20},
		{Timestamp: 2000, SourceIP: "192.168.1.3", Host: "a.com", Process: "curl", Outbound: "DIRECT", Upload: 10, Download: 30},
	})

	got, err := svc.queryAggregate("sourceIP", 500, 3000)
	if err != nil {
		t.Fatalf("queryAggregate: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0].Label != "192.168.1.2" || got[0].Upload != 150 || got[0].Download != 220 || got[0].Total != 370 {
		t.Fatalf("unexpected first row: %+v", got[0])
	}
}

func TestQueryTrendFillsEmptyBuckets(t *testing.T) {
	svc := newTestService(t)

	insertTestLogs(t, svc.db, []trafficLog{
		{Timestamp: 1000, SourceIP: "192.168.1.2", Host: "a.com", Process: "chrome", Outbound: "NodeA", Upload: 100, Download: 200},
		{Timestamp: 4100, SourceIP: "192.168.1.2", Host: "b.com", Process: "chrome", Outbound: "NodeA", Upload: 50, Download: 20},
	})

	got, err := svc.queryTrend(1000, 7000, 2000)
	if err != nil {
		t.Fatalf("queryTrend: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("expected 4 buckets, got %d", len(got))
	}
	if got[1].Timestamp != 2000 || got[1].Upload != 0 || got[1].Download != 0 {
		t.Fatalf("expected empty middle bucket, got %+v", got[1])
	}
	if got[2].Timestamp != 4000 || got[2].Upload != 50 || got[2].Download != 20 {
		t.Fatalf("unexpected populated bucket: %+v", got[2])
	}
}

func newTestService(t *testing.T) *service {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "traffic.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	return &service{db: db}
}

func insertTestLogs(t *testing.T, db *sql.DB, logs []trafficLog) {
	t.Helper()

	for _, entry := range logs {
		_, err := db.Exec(
			`INSERT INTO traffic_logs (timestamp, source_ip, host, process, outbound, upload, download)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			entry.Timestamp,
			entry.SourceIP,
			entry.Host,
			entry.Process,
			entry.Outbound,
			entry.Upload,
			entry.Download,
		)
		if err != nil {
			t.Fatalf("insert log: %v", err)
		}
	}
}
