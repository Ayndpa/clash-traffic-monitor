package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed web/*
var webAssets embed.FS

type config struct {
	ListenAddr    string
	MihomoURL     string
	MihomoSecret  string
	DatabasePath  string
	PollInterval  time.Duration
	RetentionDays int
	AllowedOrigin string
}

type trafficLog struct {
	Timestamp int64  `json:"timestamp"`
	SourceIP  string `json:"sourceIP"`
	Host      string `json:"host"`
	Process   string `json:"process"`
	Outbound  string `json:"outbound"`
	Upload    int64  `json:"upload"`
	Download  int64  `json:"download"`
}

type aggregatedData struct {
	Label    string `json:"label"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Total    int64  `json:"total"`
	Count    int64  `json:"count"`
}

type trendPoint struct {
	Timestamp int64 `json:"timestamp"`
	Upload    int64 `json:"upload"`
	Download  int64 `json:"download"`
}

type connection struct {
	ID       string   `json:"id"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Chains   []string `json:"chains"`
	Metadata struct {
		SourceIP      string `json:"sourceIP"`
		Host          string `json:"host"`
		DestinationIP string `json:"destinationIP"`
		Process       string `json:"process"`
	} `json:"metadata"`
}

type connectionsResponse struct {
	Connections   []connection `json:"connections"`
	UploadTotal   int64        `json:"uploadTotal"`
	DownloadTotal int64        `json:"downloadTotal"`
}

type service struct {
	db                *sql.DB
	client            *http.Client
	cfg               config
	mu                sync.Mutex
	lastConnections   map[string]connection
	lastUploadTotal   int64
	lastDownloadTotal int64
	lastCleanup       time.Time
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	db, err := openDatabase(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	svc := &service{
		db:              db,
		client:          &http.Client{Timeout: 10 * time.Second},
		cfg:             cfg,
		lastConnections: make(map[string]connection),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go svc.runCollector(ctx)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           svc.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("traffic monitor listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown server: %v", err)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		ListenAddr:    getenv("TRAFFIC_MONITOR_LISTEN", ":8080"),
		MihomoURL:     strings.TrimRight(getenv("MIHOMO_URL", getenv("CLASH_API", "http://127.0.0.1:9090")), "/"),
		MihomoSecret:  getenv("MIHOMO_SECRET", getenv("CLASH_SECRET", "")),
		DatabasePath:  getenv("TRAFFIC_MONITOR_DB", "./traffic_monitor.db"),
		RetentionDays: getenvInt("TRAFFIC_MONITOR_RETENTION_DAYS", 30),
		AllowedOrigin: getenv("TRAFFIC_MONITOR_ALLOWED_ORIGIN", "*"),
	}

	pollMS := getenvInt("TRAFFIC_MONITOR_POLL_INTERVAL_MS", 2000)
	if pollMS < 500 {
		pollMS = 500
	}
	cfg.PollInterval = time.Duration(pollMS) * time.Millisecond

	if cfg.MihomoURL == "" {
		return config{}, errors.New("MIHOMO_URL is required")
	}

	return cfg, nil
}

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	schema := `
	PRAGMA journal_mode=WAL;
	PRAGMA busy_timeout=5000;

	CREATE TABLE IF NOT EXISTS traffic_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp INTEGER NOT NULL,
		source_ip TEXT NOT NULL,
		host TEXT NOT NULL,
		process TEXT NOT NULL,
		outbound TEXT NOT NULL,
		upload INTEGER NOT NULL,
		download INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_traffic_logs_timestamp ON traffic_logs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_traffic_logs_source_ip ON traffic_logs(source_ip);
	CREATE INDEX IF NOT EXISTS idx_traffic_logs_host ON traffic_logs(host);
	CREATE INDEX IF NOT EXISTS idx_traffic_logs_process ON traffic_logs(process);
	CREATE INDEX IF NOT EXISTS idx_traffic_logs_outbound ON traffic_logs(outbound);
	`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func (s *service) runCollector(ctx context.Context) {
	s.collectOnce(ctx)

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectOnce(ctx)
		}
	}
}

func (s *service) collectOnce(ctx context.Context) {
	resp, err := s.fetchConnections(ctx)
	if err != nil {
		log.Printf("poll Mihomo connections: %v", err)
		return
	}

	if err := s.processConnections(resp); err != nil {
		log.Printf("process connections: %v", err)
	}
}

func (s *service) fetchConnections(ctx context.Context) (*connectionsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.MihomoURL+"/connections", nil)
	if err != nil {
		return nil, err
	}

	if s.cfg.MihomoSecret != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.MihomoSecret)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var payload connectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	return &payload, nil
}

func (s *service) processConnections(payload *connectionsResponse) error {
	nowMS := time.Now().UnixMilli()

	s.mu.Lock()
	defer s.mu.Unlock()

	if payload.UploadTotal < s.lastUploadTotal || payload.DownloadTotal < s.lastDownloadTotal {
		log.Printf("detected Mihomo counter reset, clearing in-memory baselines")
		s.lastConnections = make(map[string]connection)
	}

	s.lastUploadTotal = payload.UploadTotal
	s.lastDownloadTotal = payload.DownloadTotal

	activeIDs := make(map[string]struct{}, len(payload.Connections))
	logs := make([]trafficLog, 0, len(payload.Connections))

	for _, conn := range payload.Connections {
		activeIDs[conn.ID] = struct{}{}

		prev, hasPrev := s.lastConnections[conn.ID]
		uploadDelta := conn.Upload
		downloadDelta := conn.Download

		if hasPrev {
			uploadDelta = conn.Upload - prev.Upload
			downloadDelta = conn.Download - prev.Download
		}

		if uploadDelta < 0 {
			uploadDelta = conn.Upload
		}
		if downloadDelta < 0 {
			downloadDelta = conn.Download
		}
		if uploadDelta == 0 && downloadDelta == 0 {
			s.lastConnections[conn.ID] = conn
			continue
		}

		logs = append(logs, trafficLog{
			Timestamp: nowMS,
			SourceIP:  defaultString(conn.Metadata.SourceIP, "Inner"),
			Host:      defaultString(firstNonEmpty(conn.Metadata.Host, conn.Metadata.DestinationIP), "Unknown"),
			Process:   defaultString(conn.Metadata.Process, "Unknown"),
			Outbound:  outboundName(conn.Chains),
			Upload:    uploadDelta,
			Download:  downloadDelta,
		})

		s.lastConnections[conn.ID] = conn
	}

	for id := range s.lastConnections {
		if _, ok := activeIDs[id]; !ok {
			delete(s.lastConnections, id)
		}
	}

	if len(logs) == 0 {
		return nil
	}

	if err := s.insertLogs(logs); err != nil {
		return err
	}

	if s.cfg.RetentionDays > 0 && time.Since(s.lastCleanup) >= time.Hour {
		if err := s.cleanupOldLogs(nowMS); err != nil {
			log.Printf("cleanup old logs: %v", err)
		} else {
			s.lastCleanup = time.Now()
		}
	}

	return nil
}

func (s *service) insertLogs(logs []trafficLog) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO traffic_logs (timestamp, source_ip, host, process, outbound, upload, download)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, entry := range logs {
		if _, err := stmt.Exec(
			entry.Timestamp,
			entry.SourceIP,
			entry.Host,
			entry.Process,
			entry.Outbound,
			entry.Upload,
			entry.Download,
		); err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func (s *service) cleanupOldLogs(nowMS int64) error {
	cutoff := nowMS - int64(time.Duration(s.cfg.RetentionDays)*24*time.Hour/time.Millisecond)
	_, err := s.db.Exec(`DELETE FROM traffic_logs WHERE timestamp < ?`, cutoff)
	return err
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	staticFS, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/", fileServer)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/traffic/aggregate", s.handleAggregate)
	mux.HandleFunc("/api/traffic/substats", s.handleSubstats)
	mux.HandleFunc("/api/traffic/proxy-stats", s.handleProxyStats)
	mux.HandleFunc("/api/traffic/devices-by-host", s.handleDevicesByHost)
	mux.HandleFunc("/api/traffic/devices-by-proxy-host", s.handleDevicesByProxyHost)
	mux.HandleFunc("/api/traffic/trend", s.handleTrend)
	mux.HandleFunc("/api/traffic/logs", s.handleLogs)
	return s.withCORS(mux)
}

func (s *service) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.cfg.AllowedOrigin
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *service) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *service) handleAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	dimension := r.URL.Query().Get("dimension")
	start, end, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	data, err := s.queryAggregate(dimension, start, end)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, data)
}

func (s *service) handleSubstats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	dimension := r.URL.Query().Get("dimension")
	label := r.URL.Query().Get("label")
	start, end, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if label == "" {
		writeError(w, http.StatusBadRequest, errors.New("label is required"))
		return
	}

	data, err := s.querySubstats(dimension, label, start, end)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *service) handleProxyStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	dimension := r.URL.Query().Get("dimension")
	parentLabel := r.URL.Query().Get("parentLabel")
	host := r.URL.Query().Get("host")
	start, end, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if parentLabel == "" || host == "" {
		writeError(w, http.StatusBadRequest, errors.New("parentLabel and host are required"))
		return
	}

	data, err := s.queryProxyStats(dimension, parentLabel, host, start, end)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *service) handleDevicesByHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	host := r.URL.Query().Get("host")
	start, end, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if host == "" {
		writeError(w, http.StatusBadRequest, errors.New("host is required"))
		return
	}

	data, err := s.queryByFilters("source_ip", "host = ?", []any{host}, start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *service) handleDevicesByProxyHost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	proxy := r.URL.Query().Get("proxy")
	host := r.URL.Query().Get("host")
	start, end, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if proxy == "" || host == "" {
		writeError(w, http.StatusBadRequest, errors.New("proxy and host are required"))
		return
	}

	data, err := s.queryByFilters(
		"source_ip",
		"outbound = ? AND host = ?",
		[]any{proxy, host},
		start,
		end,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *service) handleTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	start, end, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bucket := parseInt64(r.URL.Query().Get("bucket"), 60000)
	if bucket <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("bucket must be positive"))
		return
	}

	data, err := s.queryTrend(start, end, bucket)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *service) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}

	if _, err := s.db.Exec(`DELETE FROM traffic_logs`); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *service) queryAggregate(dimension string, start, end int64) ([]aggregatedData, error) {
	column, err := dimensionColumn(dimension)
	if err != nil {
		return nil, err
	}
	return s.queryByFilters(column, "", nil, start, end)
}

func (s *service) querySubstats(dimension, label string, start, end int64) ([]aggregatedData, error) {
	column, err := dimensionColumn(dimension)
	if err != nil {
		return nil, err
	}
	if column == "host" {
		return nil, errors.New("host is not supported for substats")
	}
	return s.queryByFilters("host", column+" = ?", []any{label}, start, end)
}

func (s *service) queryProxyStats(dimension, parentLabel, host string, start, end int64) ([]aggregatedData, error) {
	column, err := dimensionColumn(dimension)
	if err != nil {
		return nil, err
	}
	if column == "host" {
		return nil, errors.New("host is not supported for proxy stats")
	}
	return s.queryByFilters("outbound", column+" = ? AND host = ?", []any{parentLabel, host}, start, end)
}

func (s *service) queryByFilters(groupColumn, extraFilter string, extraArgs []any, start, end int64) ([]aggregatedData, error) {
	base := `
		SELECT ` + groupColumn + ` AS label,
		       COALESCE(SUM(upload), 0) AS upload,
		       COALESCE(SUM(download), 0) AS download,
		       COALESCE(SUM(upload + download), 0) AS total,
		       COUNT(*) AS count
		FROM traffic_logs
		WHERE timestamp BETWEEN ? AND ?
	`
	args := []any{start, end}
	if extraFilter != "" {
		base += " AND " + extraFilter
		args = append(args, extraArgs...)
	}
	base += `
		GROUP BY ` + groupColumn + `
		ORDER BY total DESC, label ASC
	`

	rows, err := s.db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]aggregatedData, 0)
	for rows.Next() {
		var item aggregatedData
		if err := rows.Scan(&item.Label, &item.Upload, &item.Download, &item.Total, &item.Count); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

func (s *service) queryTrend(start, end, bucket int64) ([]trendPoint, error) {
	rows, err := s.db.Query(`
		SELECT ((timestamp / ?) * ?) AS bucket_start,
		       COALESCE(SUM(upload), 0) AS upload,
		       COALESCE(SUM(download), 0) AS download
		FROM traffic_logs
		WHERE timestamp BETWEEN ? AND ?
		GROUP BY bucket_start
		ORDER BY bucket_start ASC
	`, bucket, bucket, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make(map[int64]trendPoint)
	for rows.Next() {
		var point trendPoint
		if err := rows.Scan(&point.Timestamp, &point.Upload, &point.Download); err != nil {
			return nil, err
		}
		buckets[point.Timestamp] = point
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	points := make([]trendPoint, 0, (end-start)/bucket+1)
	for t := start; t <= end; t += bucket {
		key := (t / bucket) * bucket
		if point, ok := buckets[key]; ok {
			points = append(points, point)
			continue
		}
		points = append(points, trendPoint{Timestamp: key})
	}
	return points, nil
}

func parseTimeRange(r *http.Request) (int64, int64, error) {
	start := parseInt64(r.URL.Query().Get("start"), 0)
	end := parseInt64(r.URL.Query().Get("end"), 0)
	if start <= 0 || end <= 0 {
		return 0, 0, errors.New("start and end are required")
	}
	if end < start {
		return 0, 0, errors.New("end must be greater than or equal to start")
	}
	return start, end, nil
}

func dimensionColumn(dimension string) (string, error) {
	switch dimension {
	case "sourceIP":
		return "source_ip", nil
	case "host":
		return "host", nil
	case "process":
		return "process", nil
	case "outbound":
		return "outbound", nil
	default:
		return "", fmt.Errorf("unsupported dimension %q", dimension)
	}
}

func outboundName(chains []string) string {
	if len(chains) == 0 || chains[0] == "" {
		return "DIRECT"
	}
	return chains[0]
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseInt64(value string, fallback int64) int64 {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
