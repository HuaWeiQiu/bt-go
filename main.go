package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

type server struct {
	downloads *downloadManager
}

type downloadManager struct {
	client  *torrent.Client
	dataDir string
	mu      sync.RWMutex
	tasks   map[string]*downloadTask
}

type downloadTask struct {
	ID        string    `json:"id"`
	Magnet    string    `json:"magnet"`
	Name      string    `json:"name"`
	InfoHash  string    `json:"infoHash"`
	Status    string    `json:"status"`
	SavePath  string    `json:"savePath"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Error     string    `json:"error,omitempty"`

	torrent *torrent.Torrent
	mu      sync.Mutex
	last    progressSample
}

type progressSample struct {
	At        time.Time
	Completed int64
	Speed     float64
}

type addRequest struct {
	Magnet string `json:"magnet"`
}

type taskStatus struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	InfoHash        string    `json:"infoHash"`
	Status          string    `json:"status"`
	SavePath        string    `json:"savePath"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Error           string    `json:"error,omitempty"`
	TotalBytes      int64     `json:"totalBytes"`
	CompletedBytes  int64     `json:"completedBytes"`
	DownloadSpeed   float64   `json:"downloadSpeed"`
	ProgressPercent float64   `json:"progressPercent"`
	BytesMissing    int64     `json:"bytesMissing"`
	Peers           int       `json:"peers"`
	ActivePeers     int       `json:"activePeers"`
	Seeders         int       `json:"seeders"`
	MetadataReady   bool      `json:"metadataReady"`
}

func newServer(dataDir string, listenPort int) (*server, func(), string, error) {
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve download dir: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, nil, "", fmt.Errorf("create download dir: %w", err)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = absDir
	cfg.Seed = true
	cfg.NoUpload = false
	cfg.DisableTCP = false
	cfg.DisableUTP = false
	cfg.ListenPort = listenPort

	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("start torrent client: %w", err)
	}

	srv := &server{
		downloads: &downloadManager{
			client:  client,
			dataDir: absDir,
			tasks:   make(map[string]*downloadTask),
		},
	}
	return srv, func() {
		_ = client.Close()
	}, absDir, nil
}

func newMux(srv *server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.health)
	mux.HandleFunc("GET /", srv.index)
	mux.HandleFunc("POST /api/tasks", srv.addTask)
	mux.HandleFunc("GET /api/tasks", srv.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", srv.getTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", srv.deleteTask)
	mux.HandleFunc("POST /api/parse", srv.parseMagnet)
	return logRequest(cors(mux))
}

func runHTTP(addr, dataDir string, listenPort int) error {
	srv, cleanup, absDir, err := newServer(dataDir, listenPort)
	if err != nil {
		return err
	}
	defer cleanup()

	log.Printf("bt-go listening on %s, download dir=%s, bt port=%d", addr, absDir, listenPort)
	return http.ListenAndServe(addr, newMux(srv))
}

func runHTTPOnListener(ln net.Listener, dataDir string, listenPort int) (*http.Server, string, func(), error) {
	srv, cleanup, absDir, err := newServer(dataDir, listenPort)
	if err != nil {
		return nil, "", nil, err
	}
	httpServer := &http.Server{Handler: newMux(srv)}
	go func() {
		log.Printf("bt-go desktop service listening on %s, download dir=%s, bt port=%d", ln.Addr().String(), absDir, listenPort)
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("desktop service stopped: %v", err)
		}
	}()
	return httpServer, ln.Addr().String(), cleanup, nil
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *server) parseMagnet(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	mi, err := parseMagnet(req.Magnet)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":     mi.DisplayName,
		"infoHash": mi.InfoHash.HexString(),
		"trackers": trackers(mi),
	})
}

func (s *server) addTask(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	task, err := s.downloads.add(req.Magnet)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.downloads.status(task))
}

func (s *server) listTasks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.downloads.list())
}

func (s *server) getTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.downloads.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.status(task))
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if !s.downloads.delete(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (m *downloadManager) add(magnet string) (*downloadTask, error) {
	mi, err := parseMagnet(magnet)
	if err != nil {
		return nil, err
	}

	id := mi.InfoHash.HexString()
	m.mu.Lock()
	if existing, ok := m.tasks[id]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	m.mu.Unlock()

	t, err := m.client.AddMagnet(magnet)
	if err != nil {
		return nil, fmt.Errorf("add magnet: %w", err)
	}

	now := time.Now()
	task := &downloadTask{
		ID:        id,
		Magnet:    magnet,
		Name:      mi.DisplayName,
		InfoHash:  id,
		Status:    "metadata",
		SavePath:  m.dataDir,
		CreatedAt: now,
		UpdatedAt: now,
		torrent:   t,
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.mu.Unlock()

	go m.watch(task)
	return task, nil
}

func (m *downloadManager) get(id string) (*downloadTask, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, ok := m.tasks[id]
	return task, ok
}

func (m *downloadManager) list() []taskStatus {
	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()

	result := make([]taskStatus, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, m.status(task))
	}
	return result
}

func (m *downloadManager) delete(id string) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if ok {
		delete(m.tasks, id)
		task.setState("stopped", "")
	}
	m.mu.Unlock()

	if ok && task.torrent != nil {
		task.torrent.Drop()
	}
	return ok
}

func (m *downloadManager) status(task *downloadTask) taskStatus {
	var total, completed, missing int64
	var peers, activePeers, seeders int
	metadataReady := false
	if task.torrent != nil {
		stats := task.torrent.Stats()
		peers = stats.TotalPeers
		activePeers = stats.ActivePeers
		seeders = stats.ConnectedSeeders
		metadataReady = task.torrent.Info() != nil
		if metadataReady {
			total = task.torrent.Length()
			completed = task.torrent.BytesCompleted()
			missing = task.torrent.BytesMissing()
		}
	}

	status, name, infoHash, savePath, createdAt, updatedAt, errText, speed := task.refreshWithProgress(completed)

	progress := 0.0
	if total > 0 {
		progress = float64(completed) * 100 / float64(total)
	}

	return taskStatus{
		ID:              task.ID,
		Name:            name,
		InfoHash:        infoHash,
		Status:          status,
		SavePath:        savePath,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
		Error:           errText,
		TotalBytes:      total,
		CompletedBytes:  completed,
		DownloadSpeed:   speed,
		ProgressPercent: progress,
		BytesMissing:    missing,
		Peers:           peers,
		ActivePeers:     activePeers,
		Seeders:         seeders,
		MetadataReady:   metadataReady,
	}
}

func (m *downloadManager) watch(task *downloadTask) {
	select {
	case <-task.torrent.GotInfo():
		task.setName(task.torrent.Name())
		task.setState("downloading", "")
		task.torrent.DownloadAll()
	case <-time.After(10 * time.Minute):
		task.setState("metadata_timeout", "metadata not found within 10 minutes")
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		status, _, _, _, _, _, _, _ := task.refreshWithProgress(task.torrent.BytesCompleted())
		if status == "completed" || status == "stopped" {
			return
		}
	}
}

func (t *downloadTask) setName(name string) {
	if name == "" {
		return
	}
	t.mu.Lock()
	t.Name = name
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
}

func (t *downloadTask) setState(status, errText string) {
	t.mu.Lock()
	t.Status = status
	t.Error = errText
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
}

func (t *downloadTask) refreshWithProgress(completed int64) (status, name, infoHash, savePath string, createdAt, updatedAt time.Time, errText string, speed float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if !t.last.At.IsZero() {
		elapsed := now.Sub(t.last.At).Seconds()
		if elapsed > 0 {
			delta := completed - t.last.Completed
			if delta < 0 {
				delta = 0
			}
			t.last.Speed = float64(delta) / elapsed
		}
	}
	t.last.At = now
	t.last.Completed = completed

	if t.torrent == nil || t.Status == "stopped" {
		return t.Status, t.Name, t.InfoHash, t.SavePath, t.CreatedAt, t.UpdatedAt, t.Error, t.last.Speed
	}
	if t.torrent.Info() == nil {
		t.Status = "metadata"
	} else if t.torrent.Info() != nil && t.torrent.Length() > 0 && t.torrent.BytesMissing() == 0 {
		t.Status = "completed"
	} else {
		t.Status = "downloading"
	}
	t.UpdatedAt = time.Now()
	return t.Status, t.Name, t.InfoHash, t.SavePath, t.CreatedAt, t.UpdatedAt, t.Error, t.last.Speed
}

func parseMagnet(raw string) (*metainfo.Magnet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("magnet is required")
	}
	if !strings.HasPrefix(raw, "magnet:?") {
		return nil, errors.New("invalid magnet link")
	}
	mi, err := metainfo.ParseMagnetUri(raw)
	if err != nil {
		return nil, fmt.Errorf("parse magnet: %w", err)
	}
	if mi.InfoHash.HexString() == "" {
		return nil, errors.New("magnet info hash is required")
	}
	return &mi, nil
}

func trackers(mi *metainfo.Magnet) []string {
	result := make([]string, 0, len(mi.Trackers))
	for _, tr := range mi.Trackers {
		if tr != "" {
			result = append(result, tr)
		}
	}
	return result
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": msg,
	})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("%s %s panic after %s: %v", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond), recovered)
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}()
		next.ServeHTTP(w, r)
	})
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}
	return parsed
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>bt-go</title>
  <style>
    :root {
      color-scheme: light;
      font-family: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      --bg: #eef2f6;
      --surface: #ffffff;
      --surface-2: #f8fafc;
      --line: #d8dee8;
      --text: #151922;
      --muted: #657083;
      --blue: #1d66d1;
      --blue-dark: #164ea2;
      --green: #158a4a;
      --amber: #b66a00;
      --red: #b42318;
      --shadow: 0 18px 45px rgba(26, 35, 50, .08);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background:
        linear-gradient(180deg, #f7f9fc 0%, var(--bg) 58%, #e9eef4 100%);
      color: var(--text);
    }
    button, textarea { font: inherit; }
    button {
      height: 42px;
      padding: 0 16px;
      border: 0;
      border-radius: 7px;
      background: var(--blue);
      color: #fff;
      font-weight: 700;
      cursor: pointer;
      transition: background .16s ease, transform .16s ease, box-shadow .16s ease;
      white-space: nowrap;
    }
    button:hover { background: var(--blue-dark); box-shadow: 0 8px 18px rgba(29, 102, 209, .18); }
    button:active { transform: translateY(1px); }
    button.secondary { background: #354155; }
    button.secondary:hover { background: #263142; box-shadow: 0 8px 18px rgba(53, 65, 85, .16); }
    button.danger { height: 34px; padding: 0 12px; background: #fff; color: var(--red); border: 1px solid #f0c5bf; }
    button.danger:hover { background: #fff4f2; box-shadow: none; }
    main {
      width: min(1180px, calc(100% - 32px));
      margin: 0 auto;
      padding: 26px 0 34px;
    }
    .topbar {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      margin-bottom: 18px;
    }
    .brand { display: flex; align-items: center; gap: 12px; min-width: 0; }
    .mark {
      width: 42px;
      height: 42px;
      display: grid;
      place-items: center;
      border-radius: 8px;
      background: #172033;
      color: #fff;
      font-weight: 800;
      letter-spacing: 0;
    }
    h1 { margin: 0; font-size: 25px; line-height: 1.1; letter-spacing: 0; }
    .subtitle { margin-top: 4px; color: var(--muted); font-size: 13px; }
    .summary {
      display: grid;
      grid-template-columns: repeat(3, minmax(118px, 1fr));
      gap: 10px;
      min-width: 390px;
    }
    .stat {
      background: rgba(255, 255, 255, .78);
      border: 1px solid rgba(216, 222, 232, .9);
      border-radius: 8px;
      padding: 10px 12px;
      box-shadow: 0 8px 22px rgba(28, 39, 56, .05);
    }
    .stat span { display: block; color: var(--muted); font-size: 12px; margin-bottom: 3px; }
    .stat strong { display: block; font-size: 18px; line-height: 1.2; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .panel {
      background: var(--surface);
      border: 1px solid var(--line);
      border-radius: 8px;
      box-shadow: var(--shadow);
    }
    .composer {
      padding: 16px;
      margin-bottom: 16px;
    }
    .composer-grid {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 12px;
      align-items: stretch;
    }
    textarea {
      width: 100%;
      min-height: 76px;
      resize: vertical;
      padding: 12px 13px;
      border: 1px solid #cbd3df;
      border-radius: 7px;
      color: var(--text);
      background: #fff;
      outline: none;
      line-height: 1.45;
    }
    textarea:focus {
      border-color: var(--blue);
      box-shadow: 0 0 0 3px rgba(29, 102, 209, .12);
    }
    .actions {
      display: flex;
      flex-direction: column;
      gap: 8px;
      min-width: 112px;
    }
    .message {
      min-height: 20px;
      margin: 10px 2px 0;
      color: var(--muted);
      font-size: 13px;
    }
    .message.ok { color: var(--green); }
    .message.err { color: var(--red); }
    .tasks-panel { overflow: hidden; }
    .table-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      padding: 14px 16px;
      border-bottom: 1px solid var(--line);
      background: var(--surface-2);
    }
    .table-head strong { font-size: 15px; }
    .table-head span { color: var(--muted); font-size: 13px; }
    table {
      width: 100%;
      border-collapse: collapse;
      table-layout: fixed;
      font-size: 14px;
    }
    th, td {
      padding: 13px 12px;
      border-bottom: 1px solid #edf0f5;
      text-align: left;
      vertical-align: middle;
    }
    th {
      color: #687486;
      font-size: 12px;
      font-weight: 800;
      background: #fbfcfe;
    }
    th.name-col { width: 34%; }
    th.progress-col { width: 20%; }
    th.small-col { width: 11%; }
    th.action-col { width: 74px; }
    tbody tr:hover { background: #fbfdff; }
    .task-name {
      display: block;
      font-weight: 750;
      line-height: 1.35;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    code {
      display: block;
      margin-top: 4px;
      color: #6b7484;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
      word-break: break-all;
    }
    .pill {
      display: inline-flex;
      align-items: center;
      height: 26px;
      padding: 0 9px;
      border-radius: 999px;
      font-size: 12px;
      font-weight: 800;
      background: #eef4ff;
      color: var(--blue);
    }
    .pill.completed { background: #eaf7ef; color: var(--green); }
    .pill.metadata, .pill.metadata_timeout { background: #fff4df; color: var(--amber); }
    .pill.stopped { background: #f1f3f6; color: #596273; }
    .bar {
      height: 9px;
      background: #e7ecf3;
      border-radius: 999px;
      overflow: hidden;
    }
    .bar span {
      display: block;
      height: 100%;
      width: 0%;
      background: linear-gradient(90deg, var(--blue), var(--green));
      border-radius: inherit;
      transition: width .25s ease;
    }
    .progress-text { margin-top: 6px; color: var(--muted); font-size: 12px; }
    .metric { font-weight: 760; }
    .muted { color: var(--muted); font-size: 12px; }
    .empty {
      padding: 42px 16px;
      text-align: center;
      color: var(--muted);
    }
    .empty strong { display: block; color: var(--text); font-size: 16px; margin-bottom: 6px; }
    @media (max-width: 860px) {
      main { width: min(100% - 20px, 720px); padding-top: 16px; }
      .topbar { align-items: stretch; flex-direction: column; }
      .summary { min-width: 0; grid-template-columns: repeat(3, minmax(0, 1fr)); }
      .composer-grid { grid-template-columns: 1fr; }
      .actions { flex-direction: row; min-width: 0; }
      .actions button { flex: 1; }
      table, thead, tbody, th, td, tr { display: block; }
      thead { display: none; }
      tbody { padding: 8px; display: block; }
      tbody tr {
        border: 1px solid #e5e9f0;
        border-radius: 8px;
        margin-bottom: 10px;
        background: #fff;
      }
      tbody tr:hover { background: #fff; }
      td { border: 0; padding: 8px 10px; }
      .task-name { white-space: normal; }
    }
  </style>
</head>
<body>
<main>
  <header class="topbar">
    <div class="brand">
      <div class="mark">BT</div>
      <div>
        <h1>bt-go</h1>
        <div class="subtitle">本地磁力下载器</div>
      </div>
    </div>
    <div class="summary">
      <div class="stat"><span>任务</span><strong id="statTasks">0</strong></div>
      <div class="stat"><span>总速度</span><strong id="statSpeed">0 B/s</strong></div>
      <div class="stat"><span>活跃 Peer</span><strong id="statPeers">0</strong></div>
    </div>
  </header>

  <section class="panel composer">
    <div class="composer-grid">
      <textarea id="magnet" placeholder="粘贴 magnet:?xt=urn:btih:..."></textarea>
      <div class="actions">
        <button onclick="addTask()">添加下载</button>
        <button class="secondary" onclick="loadTasks()">刷新</button>
      </div>
    </div>
    <div id="message" class="message"></div>
  </section>

  <section class="panel tasks-panel">
    <div class="table-head">
      <strong>下载任务</strong>
      <span id="lastRefresh">等待刷新</span>
    </div>
    <table>
      <thead>
        <tr>
          <th class="name-col">名称</th>
          <th>状态</th>
          <th class="progress-col">进度</th>
          <th class="small-col">速度</th>
          <th class="small-col">Peer</th>
          <th class="small-col">大小</th>
          <th class="action-col"></th>
        </tr>
      </thead>
      <tbody id="tasks"></tbody>
    </table>
  </section>
</main>
<script>
const fmtBytes = n => {
  if (!n || n < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return n.toFixed(i ? 2 : 0) + " " + units[i];
};

const statusText = status => ({
  metadata: "获取元数据",
  metadata_timeout: "元数据超时",
  downloading: "下载中",
  completed: "已完成",
  stopped: "已停止"
}[status] || status || "未知");

function setMessage(text, type) {
  const el = document.getElementById("message");
  el.textContent = text || "";
  el.className = "message" + (type ? " " + type : "");
}

function cell(label, content) {
  const td = document.createElement("td");
  if (label) td.setAttribute("data-label", label);
  if (content) td.appendChild(content);
  return td;
}

function renderSummary(tasks) {
  const speed = tasks.reduce((sum, task) => sum + (task.downloadSpeed || 0), 0);
  const peers = tasks.reduce((sum, task) => sum + (task.activePeers || 0), 0);
  document.getElementById("statTasks").textContent = String(tasks.length);
  document.getElementById("statSpeed").textContent = fmtBytes(speed) + "/s";
  document.getElementById("statPeers").textContent = String(peers);
  document.getElementById("lastRefresh").textContent = "最后刷新 " + new Date().toLocaleTimeString();
}

async function addTask() {
  const input = document.getElementById("magnet");
  const magnet = input.value.trim();
  if (!magnet) {
    setMessage("请先粘贴磁力链接", "err");
    return;
  }
  setMessage("正在添加任务...");
  try {
    const res = await fetch("/api/tasks", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({magnet})
    });
    const data = await res.json();
    if (!res.ok) {
      setMessage(data.error || "添加失败", "err");
      return;
    }
    input.value = "";
    setMessage("已添加任务：" + data.infoHash, "ok");
    await loadTasks();
  } catch (err) {
    setMessage("请求失败：" + err.message, "err");
  }
}

async function deleteTask(id) {
  await fetch("/api/tasks/" + encodeURIComponent(id), {method: "DELETE"});
  await loadTasks();
}

function renderEmpty(tbody) {
  const tr = document.createElement("tr");
  const td = document.createElement("td");
  td.colSpan = 7;
  const empty = document.createElement("div");
  empty.className = "empty";
  const title = document.createElement("strong");
  title.textContent = "暂无下载任务";
  const desc = document.createElement("span");
  desc.textContent = "粘贴磁力链接后会在这里显示进度、速度和 Peer 状态";
  empty.append(title, desc);
  td.appendChild(empty);
  tr.appendChild(td);
  tbody.appendChild(tr);
}

function renderTask(tbody, task) {
  const pct = Math.max(0, Math.min(100, task.progressPercent || 0));
  const tr = document.createElement("tr");

  const nameWrap = document.createElement("div");
  const name = document.createElement("span");
  name.className = "task-name";
  name.textContent = task.name || "等待元数据";
  const hash = document.createElement("code");
  hash.textContent = task.infoHash || task.id || "";
  nameWrap.append(name, hash);
  tr.appendChild(cell("名称", nameWrap));

  const statusWrap = document.createElement("div");
  const pill = document.createElement("span");
  pill.className = "pill " + (task.status || "");
  pill.textContent = statusText(task.status);
  const meta = document.createElement("div");
  meta.className = "muted";
  meta.textContent = task.metadataReady ? "metadata ready" : "metadata pending";
  statusWrap.append(pill, meta);
  tr.appendChild(cell("状态", statusWrap));

  const progressWrap = document.createElement("div");
  const bar = document.createElement("div");
  bar.className = "bar";
  const fill = document.createElement("span");
  fill.style.width = pct.toFixed(2) + "%";
  bar.appendChild(fill);
  const progressText = document.createElement("div");
  progressText.className = "progress-text";
  progressText.textContent = pct.toFixed(2) + "%";
  progressWrap.append(bar, progressText);
  tr.appendChild(cell("进度", progressWrap));

  const speed = document.createElement("div");
  speed.className = "metric";
  speed.textContent = fmtBytes(task.downloadSpeed) + "/s";
  tr.appendChild(cell("速度", speed));

  const peers = document.createElement("div");
  peers.innerHTML = "<strong></strong><br><span class=\"muted\"></span>";
  peers.querySelector("strong").textContent = (task.activePeers || 0) + "/" + (task.peers || 0);
  peers.querySelector("span").textContent = "seeders " + (task.seeders || 0);
  tr.appendChild(cell("Peer", peers));

  const size = document.createElement("div");
  size.className = "metric";
  size.textContent = fmtBytes(task.completedBytes) + " / " + fmtBytes(task.totalBytes);
  tr.appendChild(cell("大小", size));

  const btn = document.createElement("button");
  btn.className = "danger";
  btn.textContent = "停止";
  btn.onclick = () => deleteTask(task.id);
  tr.appendChild(cell("", btn));

  tbody.appendChild(tr);
}

async function loadTasks() {
  try {
    const res = await fetch("/api/tasks");
    const tasks = await res.json();
    const tbody = document.getElementById("tasks");
    tbody.innerHTML = "";
    renderSummary(tasks);
    if (!tasks.length) {
      renderEmpty(tbody);
      return;
    }
    tasks.forEach(task => renderTask(tbody, task));
  } catch (err) {
    setMessage("刷新失败：" + err.message, "err");
  }
}

loadTasks();
setInterval(loadTasks, 2000);
</script>
</body>
</html>`
