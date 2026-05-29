package btgo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"go.etcd.io/bbolt"
	"golang.org/x/time/rate"
)

type server struct {
	downloads *downloadManager
}

type downloadManager struct {
	client      *torrent.Client
	dataDir     string
	rateLimiter *rate.Limiter
	state       *stateStore
	discovery   *discoveryManager
	saveMu      sync.Mutex

	settingsMu sync.RWMutex
	settings   appSettings

	mu    sync.RWMutex
	tasks map[string]*downloadTask
}

type taskMoveRequest struct {
	Direction string `json:"direction"`
}

type downloadTask struct {
	ID                string    `json:"id"`
	Magnet            string    `json:"magnet"`
	Name              string    `json:"name"`
	InfoHash          string    `json:"infoHash"`
	Status            string    `json:"status"`
	SavePath          string    `json:"savePath"`
	Source            string    `json:"source"`
	Trackers          []string  `json:"trackers"`
	Private           bool      `json:"private"`
	Order             int64     `json:"order"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Error             string    `json:"error,omitempty"`
	Active            bool      `json:"active"`
	Paused            bool      `json:"paused"`
	AwaitingSelection bool      `json:"awaitingSelection"`

	metaInfo         []byte
	fileSelection    map[string]bool
	filePriorities   map[string]string
	fileSelectionSet bool
	torrent          *torrent.Torrent
	mu               sync.Mutex
	last             progressSample
	done             chan struct{}
	stopOnce         sync.Once
}

type progressSample struct {
	At         time.Time
	ProgressAt time.Time
	Completed  int64
	Speed      float64
}

type addRequest struct {
	Magnet string `json:"magnet"`
}

type appSettings struct {
	MaxActiveDownloads     int   `json:"maxActiveDownloads"`
	DownloadRateLimitBytes int64 `json:"downloadRateLimitBytes"`
	WaitForFileSelection   bool  `json:"waitForFileSelection"`
}

type persistedState struct {
	Settings appSettings     `json:"settings"`
	Tasks    []persistedTask `json:"tasks"`
}

type persistedTask struct {
	ID                string            `json:"id"`
	Magnet            string            `json:"magnet"`
	Name              string            `json:"name"`
	InfoHash          string            `json:"infoHash"`
	Source            string            `json:"source"`
	Trackers          []string          `json:"trackers"`
	Private           bool              `json:"private"`
	MetaInfo          []byte            `json:"metaInfo,omitempty"`
	Files             []string          `json:"files,omitempty"`
	FilePriorities    map[string]string `json:"filePriorities,omitempty"`
	FileSelectionSet  bool              `json:"fileSelectionSet,omitempty"`
	Order             int64             `json:"order"`
	Paused            bool              `json:"paused"`
	AwaitingSelection bool              `json:"awaitingSelection"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
}

type fileSelectionRequest struct {
	Files      []string          `json:"files"`
	Priorities map[string]string `json:"priorities"`
}

type openPathRequest struct {
	Path string `json:"path"`
}

type taskFileStatus struct {
	Path            string  `json:"path"`
	Size            int64   `json:"size"`
	CompletedBytes  int64   `json:"completedBytes"`
	ProgressPercent float64 `json:"progressPercent"`
	Selected        bool    `json:"selected"`
	Priority        string  `json:"priority"`
}

type stateStore struct {
	db *bbolt.DB
}

var errTaskNotFound = errors.New("task not found")

type taskStatus struct {
	ID                string           `json:"id"`
	Magnet            string           `json:"magnet"`
	Name              string           `json:"name"`
	InfoHash          string           `json:"infoHash"`
	Status            string           `json:"status"`
	SavePath          string           `json:"savePath"`
	Source            string           `json:"source"`
	Order             int64            `json:"order"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	Error             string           `json:"error,omitempty"`
	Active            bool             `json:"active"`
	Paused            bool             `json:"paused"`
	AwaitingSelection bool             `json:"awaitingSelection"`
	Diagnostic        string           `json:"diagnostic"`
	DiagnosticCode    string           `json:"diagnosticCode"`
	Queued            bool             `json:"queued"`
	QueuePosition     int              `json:"queuePosition"`
	Trackers          []string         `json:"trackers"`
	TrackerCount      int              `json:"trackerCount"`
	Private           bool             `json:"private"`
	DHTEnabled        bool             `json:"dhtEnabled"`
	DHTServers        int              `json:"dhtServers"`
	ListenAddrs       []string         `json:"listenAddrs"`
	KnownPeers        int              `json:"knownPeers"`
	BytesReadData     int64            `json:"bytesReadData"`
	BytesWasted       int64            `json:"bytesWasted"`
	MetadataAge       int64            `json:"metadataAgeSeconds"`
	ETASeconds        int64            `json:"etaSeconds"`
	Stalled           bool             `json:"stalled"`
	StalledSeconds    int64            `json:"stalledSeconds"`
	TotalBytes        int64            `json:"totalBytes"`
	CompletedBytes    int64            `json:"completedBytes"`
	DownloadSpeed     float64          `json:"downloadSpeed"`
	ProgressPercent   float64          `json:"progressPercent"`
	BytesMissing      int64            `json:"bytesMissing"`
	Peers             int              `json:"peers"`
	PendingPeers      int              `json:"pendingPeers"`
	HalfOpenPeers     int              `json:"halfOpenPeers"`
	ActivePeers       int              `json:"activePeers"`
	Seeders           int              `json:"seeders"`
	MetadataReady     bool             `json:"metadataReady"`
	Files             []taskFileStatus `json:"files,omitempty"`
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
	cfg.NoDefaultPortForwarding = false
	cfg.DisablePEX = false
	cfg.DisableTCP = false
	cfg.DisableUTP = false
	cfg.ListenPort = listenPort
	discovery := newDiscoveryManager(defaultPublicTrackers)
	cfg.DhtStartingNodes = discovery.dhtStartingNodes
	cfg.PeriodicallyAnnounceTorrentsToDht = false
	downloadRateLimiter := rate.NewLimiter(rate.Inf, 1<<20)
	cfg.DownloadRateLimiter = downloadRateLimiter
	cfg.TorrentPeersLowWater = 50
	cfg.TorrentPeersHighWater = 500
	cfg.EstablishedConnsPerTorrent = 80
	cfg.HalfOpenConnsPerTorrent = 30
	cfg.TotalHalfOpenConns = 100

	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("start torrent client: %w", err)
	}
	state, err := openStateStore(absDir)
	if err != nil {
		_ = client.Close()
		return nil, nil, "", fmt.Errorf("open state store: %w", err)
	}
	persisted, err := state.load()
	if err != nil {
		_ = state.Close()
		_ = client.Close()
		return nil, nil, "", fmt.Errorf("load state: %w", err)
	}

	srv := &server{
		downloads: &downloadManager{
			client:      client,
			dataDir:     absDir,
			rateLimiter: downloadRateLimiter,
			state:       state,
			discovery:   discovery,
			settings:    defaultAppSettings(),
			tasks:       make(map[string]*downloadTask),
		},
	}
	if _, err := srv.downloads.applySettings(persisted.Settings); err != nil {
		_ = state.Close()
		_ = client.Close()
		return nil, nil, "", fmt.Errorf("apply saved settings: %w", err)
	}
	if err := srv.downloads.restoreTasks(persisted.Tasks); err != nil {
		_ = state.Close()
		_ = client.Close()
		return nil, nil, "", fmt.Errorf("restore tasks: %w", err)
	}
	discoveryCtx, stopDiscoveryRefresh := context.WithCancel(context.Background())
	var discoveryRefreshDone sync.WaitGroup
	discoveryRefreshDone.Add(1)
	go func() {
		defer discoveryRefreshDone.Done()
		srv.downloads.refreshDiscoverySources(discoveryCtx)
	}()
	return srv, func() {
		stopDiscoveryRefresh()
		discoveryRefreshDone.Wait()
		_ = client.Close()
		_ = state.Close()
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
	mux.HandleFunc("POST /api/tasks/{id}/pause", srv.pauseTask)
	mux.HandleFunc("POST /api/tasks/{id}/resume", srv.resumeTask)
	mux.HandleFunc("POST /api/tasks/{id}/move", srv.moveTask)
	mux.HandleFunc("PUT /api/tasks/{id}/files", srv.updateTaskFiles)
	mux.HandleFunc("POST /api/tasks/{id}/open", srv.openTaskPath)
	mux.HandleFunc("POST /api/tasks/{id}/refresh-discovery", srv.refreshTaskDiscovery)
	mux.HandleFunc("POST /api/tasks/pause-all", srv.pauseAllTasks)
	mux.HandleFunc("POST /api/tasks/resume-all", srv.resumeAllTasks)
	mux.HandleFunc("POST /api/open-download-dir", srv.openDownloadDir)
	mux.HandleFunc("POST /api/parse", srv.parseMagnet)
	mux.HandleFunc("POST /api/torrents", srv.addTorrentFile)
	mux.HandleFunc("GET /api/settings", srv.getSettings)
	mux.HandleFunc("PUT /api/settings", srv.updateSettings)
	return logRequest(cors(mux))
}

func RunHTTP(addr, dataDir string, listenPort int) error {
	srv, cleanup, absDir, err := newServer(dataDir, listenPort)
	if err != nil {
		return err
	}
	defer cleanup()

	log.Printf("bt-go listening on %s, download dir=%s, bt port=%d", addr, absDir, listenPort)
	return http.ListenAndServe(addr, newMux(srv))
}

func RunHTTPOnListener(ln net.Listener, dataDir string, listenPort int) (*http.Server, string, func(), error) {
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
	mi, err := s.downloads.parseMagnet(req.Magnet)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         mi.DisplayName,
		"infoHash":     mi.InfoHash.HexString(),
		"trackers":     trackers(mi),
		"trackerCount": len(mi.Trackers),
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

func (s *server) addTorrentFile(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "multipart form body is required")
		return
	}

	var body io.Reader
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "read multipart body: "+err.Error())
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		contentType := part.Header.Get("Content-Type")
		if contentType != "" {
			if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
				contentType = mediaType
			}
		}
		if part.FileName() != "" && !strings.HasSuffix(strings.ToLower(part.FileName()), ".torrent") && contentType != "application/x-bittorrent" {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "torrent file is required")
			return
		}
		body = part
		defer part.Close()
		break
	}
	if body == nil {
		writeError(w, http.StatusBadRequest, "torrent file is required")
		return
	}

	task, err := s.downloads.addTorrent(body)
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
	deleted, err := s.downloads.delete(r.PathValue("id"), r.URL.Query().Get("deleteFiles") == "true")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *server) pauseTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.downloads.pause(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errTaskNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.status(task))
}

func (s *server) resumeTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.downloads.resume(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errTaskNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.status(task))
}

func (s *server) pauseAllTasks(w http.ResponseWriter, _ *http.Request) {
	if err := s.downloads.pauseAll(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.list())
}

func (s *server) resumeAllTasks(w http.ResponseWriter, _ *http.Request) {
	if err := s.downloads.resumeAll(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.list())
}

func (s *server) moveTask(w http.ResponseWriter, r *http.Request) {
	var req taskMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	task, err := s.downloads.move(r.PathValue("id"), req.Direction)
	if err != nil {
		if errors.Is(err, errTaskNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.status(task))
}

func (s *server) updateTaskFiles(w http.ResponseWriter, r *http.Request) {
	var req fileSelectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	task, err := s.downloads.updateFileSelection(r.PathValue("id"), req)
	if err != nil {
		if errors.Is(err, errTaskNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.status(task))
}

func (s *server) openDownloadDir(w http.ResponseWriter, _ *http.Request) {
	if err := openLocalPath(s.downloads.dataDir); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"opened": true})
}

func (s *server) openTaskPath(w http.ResponseWriter, r *http.Request) {
	var req openPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	path, err := s.downloads.taskOpenPath(r.PathValue("id"), req.Path)
	if err != nil {
		if errors.Is(err, errTaskNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := openLocalPath(path); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"opened": true})
}

func (s *server) refreshTaskDiscovery(w http.ResponseWriter, r *http.Request) {
	task, err := s.downloads.refreshDiscovery(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errTaskNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.downloads.status(task))
}

func (s *server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.downloads.getSettings())
}

func (s *server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req appSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	settings, err := s.downloads.updateSettings(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (m *downloadManager) add(magnet string) (*downloadTask, error) {
	magnet = cleanMagnetInput(magnet)
	mi, err := m.parseMagnet(magnet)
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

	trackerList := trackers(mi)
	t, err := m.client.AddMagnet(mi.String())
	if err != nil {
		return nil, fmt.Errorf("add magnet: %w", err)
	}
	t.AddTrackers(trackerTiers(trackerList))
	t.DisallowDataDownload()
	m.announceToDht(t, false)

	now := time.Now()
	task := &downloadTask{
		ID:        id,
		Magnet:    magnet,
		Name:      mi.DisplayName,
		InfoHash:  id,
		Status:    "metadata",
		SavePath:  m.dataDir,
		Source:    "magnet",
		Trackers:  trackerList,
		Order:     m.nextOrder(),
		CreatedAt: now,
		UpdatedAt: now,
		torrent:   t,
		done:      make(chan struct{}),
	}
	if m.getSettings().WaitForFileSelection && t.Info() != nil {
		task.AwaitingSelection = true
		task.Status = "awaiting_selection"
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.mu.Unlock()

	if err := m.saveState(); err != nil {
		_, _ = m.delete(id, false)
		return nil, err
	}
	m.schedule()
	go m.watch(task)
	return task, nil
}

func (m *downloadManager) addTorrent(r io.Reader) (*downloadTask, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 128<<20))
	if err != nil {
		return nil, fmt.Errorf("read torrent file: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("torrent file is empty")
	}
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse torrent file: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("parse torrent info: %w", err)
	}
	magnet := mi.Magnet(nil, &info)
	private := isPrivateTorrentInfo(info)
	if !private {
		magnet.Trackers = m.mergeTrackersWithDefaults(magnet.Trackers)
	} else {
		magnet.Trackers = mergeTrackers(magnet.Trackers, nil)
	}
	id := magnet.InfoHash.HexString()

	m.mu.Lock()
	if existing, ok := m.tasks[id]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	m.mu.Unlock()

	spec := torrent.TorrentSpecFromMetaInfo(mi)
	spec.Trackers = trackerTiers(magnet.Trackers)
	if private {
		spec.DhtNodes = nil
	}
	spec.DisallowDataDownload = true
	t, _, err := m.client.AddTorrentSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("add torrent file: %w", err)
	}
	t.AddTrackers(trackerTiers(magnet.Trackers))
	t.DisallowDataDownload()
	m.announceToDht(t, private)

	now := time.Now()
	task := &downloadTask{
		ID:        id,
		Magnet:    magnet.String(),
		Name:      info.BestName(),
		InfoHash:  id,
		Status:    "metadata",
		SavePath:  m.dataDir,
		Source:    "torrent",
		Trackers:  trackers(&magnet),
		Private:   private,
		Order:     m.nextOrder(),
		CreatedAt: now,
		UpdatedAt: now,
		metaInfo:  raw,
		torrent:   t,
		done:      make(chan struct{}),
	}
	if m.getSettings().WaitForFileSelection && t.Info() != nil {
		task.AwaitingSelection = true
		task.Status = "awaiting_selection"
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.mu.Unlock()

	if err := m.saveState(); err != nil {
		_, _ = m.delete(id, false)
		return nil, err
	}
	m.schedule()
	go m.watch(task)
	return task, nil
}

func (m *downloadManager) restoreTasks(saved []persistedTask) error {
	sort.Slice(saved, func(i, j int) bool {
		return persistedTaskOrderLess(saved[i], saved[j])
	})
	nextOrder := int64(1)
	for _, savedTask := range saved {
		private := savedTask.Private
		defaults := m.defaultTrackers()
		if private {
			defaults = nil
		}
		mi, err := parseMagnetWithDefaults(savedTask.Magnet, defaults)
		if err != nil {
			log.Printf("skip saved task %s: %v", savedTask.ID, err)
			continue
		}
		id := mi.InfoHash.HexString()
		if savedTask.ID != "" {
			id = savedTask.ID
		}
		trackerList := mergeTrackers(savedTask.Trackers, mi.Trackers)
		var t *torrent.Torrent
		if len(savedTask.MetaInfo) > 0 {
			meta, err := metainfo.Load(bytes.NewReader(savedTask.MetaInfo))
			if err != nil {
				log.Printf("saved torrent metadata for %s is invalid, falling back to magnet: %v", savedTask.ID, err)
			} else {
				spec := torrent.TorrentSpecFromMetaInfo(meta)
				if info, err := meta.UnmarshalInfo(); err == nil && isPrivateTorrentInfo(info) {
					private = true
					trackerList = mergeTrackers(savedTask.Trackers, nil)
				}
				spec.Trackers = trackerTiers(trackerList)
				if private {
					spec.DhtNodes = nil
				}
				spec.DisallowDataDownload = true
				t, _, err = m.client.AddTorrentSpec(spec)
				if err != nil {
					log.Printf("saved torrent metadata for %s failed, falling back to magnet: %v", savedTask.ID, err)
					t = nil
				}
			}
		}
		if t == nil {
			t, err = m.client.AddMagnet(mi.String())
			if err != nil {
				log.Printf("skip saved task %s: %v", savedTask.ID, err)
				continue
			}
		}
		t.AddTrackers(trackerTiers(trackerList))
		t.DisallowDataDownload()
		m.announceToDht(t, private)
		createdAt := savedTask.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		updatedAt := savedTask.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}
		order := savedTask.Order
		if order <= 0 {
			order = nextOrder
		}
		if order >= nextOrder {
			nextOrder = order + 1
		}
		task := &downloadTask{
			ID:                id,
			Magnet:            savedTask.Magnet,
			Name:              savedTask.Name,
			InfoHash:          id,
			Status:            "metadata",
			SavePath:          m.dataDir,
			Source:            savedTask.Source,
			Trackers:          trackerList,
			Private:           private,
			Order:             order,
			Paused:            savedTask.Paused,
			AwaitingSelection: savedTask.AwaitingSelection,
			CreatedAt:         createdAt,
			UpdatedAt:         updatedAt,
			metaInfo:          append([]byte(nil), savedTask.MetaInfo...),
			fileSelection:     selectionFromList(savedTask.Files),
			filePriorities:    prioritiesFromState(savedTask.Files, savedTask.FilePriorities),
			fileSelectionSet:  savedTask.FileSelectionSet || len(savedTask.Files) > 0,
			torrent:           t,
			done:              make(chan struct{}),
		}
		if task.Paused {
			task.Status = "paused"
		}
		if task.Source == "" {
			task.Source = "magnet"
		}
		task.applyFileSelection()
		m.mu.Lock()
		m.tasks[id] = task
		m.mu.Unlock()
		go m.watch(task)
	}
	m.schedule()
	return nil
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
	sortTasks(tasks)

	result := make([]taskStatus, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, m.status(task))
	}
	return result
}

func (m *downloadManager) delete(id string, deleteFiles bool) (bool, error) {
	m.mu.Lock()
	task, ok := m.tasks[id]
	var filePaths []string
	if ok {
		if deleteFiles {
			filePaths = task.filePaths()
		}
		delete(m.tasks, id)
		task.setState("stopped", "")
		task.stop()
	}
	m.mu.Unlock()

	if ok && task.torrent != nil {
		task.torrent.Drop()
	}
	if ok {
		if deleteFiles {
			if err := m.deleteTaskFiles(task, filePaths); err != nil {
				return true, err
			}
		}
		if err := m.saveState(); err != nil {
			log.Printf("save state after delete failed: %v", err)
		}
		m.schedule()
	}
	return ok, nil
}

func (m *downloadManager) pause(id string) (*downloadTask, error) {
	task, ok := m.get(id)
	if !ok {
		return nil, errTaskNotFound
	}
	task.pauseByUser()
	if err := m.saveState(); err != nil {
		return nil, err
	}
	m.schedule()
	return task, nil
}

func (m *downloadManager) resume(id string) (*downloadTask, error) {
	task, ok := m.get(id)
	if !ok {
		return nil, errTaskNotFound
	}
	task.resumeByUser()
	if err := m.saveState(); err != nil {
		return nil, err
	}
	m.schedule()
	return task, nil
}

func (m *downloadManager) pauseAll() error {
	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()
	for _, task := range tasks {
		task.pauseByUser()
	}
	if err := m.saveState(); err != nil {
		return err
	}
	m.schedule()
	return nil
}

func (m *downloadManager) resumeAll() error {
	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()
	for _, task := range tasks {
		task.resumeByUser()
	}
	if err := m.saveState(); err != nil {
		return err
	}
	m.schedule()
	return nil
}

func (m *downloadManager) move(id, direction string) (*downloadTask, error) {
	direction = strings.TrimSpace(strings.ToLower(direction))
	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	task, ok := m.tasks[id]
	for _, item := range m.tasks {
		tasks = append(tasks, item)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, errTaskNotFound
	}
	sortTasks(tasks)
	index := -1
	for i, item := range tasks {
		if item.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, errTaskNotFound
	}
	switch direction {
	case "top":
		if index > 0 {
			tasks = append(append([]*downloadTask{task}, tasks[:index]...), tasks[index+1:]...)
			reindexTasks(tasks)
		}
	case "bottom":
		if index < len(tasks)-1 {
			tasks = append(append(tasks[:index], tasks[index+1:]...), task)
			reindexTasks(tasks)
		}
	case "up":
		if index > 0 {
			tasks[index], tasks[index-1] = tasks[index-1], tasks[index]
			reindexTasks(tasks)
		}
	case "down":
		if index < len(tasks)-1 {
			tasks[index], tasks[index+1] = tasks[index+1], tasks[index]
			reindexTasks(tasks)
		}
	default:
		return nil, errors.New("direction must be top, up, down, or bottom")
	}
	if err := m.saveState(); err != nil {
		return nil, err
	}
	m.schedule()
	return task, nil
}

func (m *downloadManager) updateFileSelection(id string, req fileSelectionRequest) (*downloadTask, error) {
	task, ok := m.get(id)
	if !ok {
		return nil, errTaskNotFound
	}
	if task.torrent == nil || task.torrent.Info() == nil {
		return nil, errors.New("metadata is not ready")
	}
	allowed := make(map[string]struct{})
	for _, file := range task.torrent.Files() {
		if file == nil {
			continue
		}
		allowed[file.DisplayPath()] = struct{}{}
	}
	priorities := make(map[string]string, len(req.Priorities)+len(req.Files))
	for file, priority := range req.Priorities {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file == "" {
			continue
		}
		if _, ok := allowed[file]; !ok {
			return nil, fmt.Errorf("unknown file: %s", file)
		}
		normalized, err := normalizeFilePriority(priority)
		if err != nil {
			return nil, err
		}
		priorities[file] = normalized
	}
	for _, file := range req.Files {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file == "" {
			continue
		}
		if _, ok := allowed[file]; !ok {
			return nil, fmt.Errorf("unknown file: %s", file)
		}
		if _, ok := priorities[file]; !ok {
			priorities[file] = "normal"
		}
	}
	task.setFilePriorities(priorities, true)
	if err := m.saveState(); err != nil {
		return nil, err
	}
	m.schedule()
	return task, nil
}

func (m *downloadManager) taskOpenPath(id, relPath string) (string, error) {
	task, ok := m.get(id)
	if !ok {
		return "", errTaskNotFound
	}
	task.mu.Lock()
	savePath := task.SavePath
	name := task.Name
	task.mu.Unlock()
	target := savePath
	relPath = strings.TrimSpace(relPath)
	if relPath != "" {
		if task.torrent == nil || task.torrent.Info() == nil {
			return "", errors.New("metadata is not ready")
		}
		found := false
		for _, file := range task.torrent.Files() {
			if file != nil && (file.DisplayPath() == relPath || file.Path() == relPath) {
				target = filepath.Join(savePath, filepath.FromSlash(file.Path()))
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("unknown file: %s", relPath)
		}
		if _, err := os.Stat(target); err != nil {
			parent := filepath.Dir(target)
			if _, parentErr := os.Stat(parent); parentErr == nil {
				target = parent
			} else {
				target = savePath
			}
		}
	} else if name != "" {
		candidate := filepath.Join(savePath, name)
		if _, err := os.Stat(candidate); err == nil {
			target = candidate
		}
	}
	return safeChildPath(m.dataDir, target)
}

func (m *downloadManager) refreshDiscovery(id string) (*downloadTask, error) {
	task, ok := m.get(id)
	if !ok {
		return nil, errTaskNotFound
	}
	task.mu.Lock()
	tor := task.torrent
	trackers := append([]string(nil), task.Trackers...)
	task.UpdatedAt = time.Now()
	task.mu.Unlock()
	if tor == nil {
		return nil, errors.New("torrent is not ready")
	}
	if len(trackers) > 0 {
		tor.AddTrackers(trackerTiers(trackers))
	}
	m.announceToDht(tor, task.Private)
	m.schedule()
	return task, nil
}

func (m *downloadManager) announceToDht(tor *torrent.Torrent, private bool) {
	if m == nil || tor == nil || private {
		return
	}
	for _, dhtServer := range m.client.DhtServers() {
		done, stop, err := tor.AnnounceToDht(dhtServer)
		if err != nil {
			continue
		}
		go func() {
			select {
			case <-done:
			case <-time.After(45 * time.Second):
				stop()
			}
		}()
	}
}

func (m *downloadManager) refreshDiscoverySources(ctx context.Context) {
	if m == nil || m.discovery == nil {
		return
	}
	trackers, err := fetchPublicTrackers(ctx, publicTrackerSources, publicTrackerSourceTimeout)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Printf("refresh public trackers failed: %v", err)
		return
	}
	if changed := m.discovery.UpdateTrackers(trackers); !changed {
		log.Printf("refreshed public tracker sources: %d trackers", len(m.discovery.Trackers()))
	} else {
		log.Printf("refreshed public tracker sources: %d trackers", len(m.discovery.Trackers()))
		m.applyDiscoveryTrackersToTasks()
	}

	rankedTrackers := rankTrackersByHealth(ctx, m.discovery.Trackers())
	if changed := m.discovery.UpdateTrackers(rankedTrackers); changed {
		log.Printf("ranked public tracker list after health checks: %d trackers", len(m.discovery.Trackers()))
		m.applyDiscoveryTrackersToTasks()
	}
	rankedDhtNodes := rankDhtNodesByHealth(ctx, m.discovery.DhtNodes())
	if changed := m.discovery.UpdateDhtNodes(rankedDhtNodes); changed {
		log.Printf("ranked DHT bootstrap nodes after health checks: %d nodes", len(m.discovery.DhtNodes()))
	}
}

func (m *downloadManager) applyDiscoveryTrackersToTasks() {
	if m == nil || m.discovery == nil {
		return
	}
	defaults := m.discovery.Trackers()
	if len(defaults) == 0 {
		return
	}

	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()

	changed := false
	for _, task := range tasks {
		task.mu.Lock()
		if task.Private {
			task.mu.Unlock()
			continue
		}
		merged := mergeTrackers(task.Trackers, defaults)
		if len(merged) == len(task.Trackers) {
			task.mu.Unlock()
			continue
		}
		task.Trackers = merged
		tor := task.torrent
		task.UpdatedAt = time.Now()
		task.mu.Unlock()

		if tor != nil {
			tor.AddTrackers(trackerTiers(merged))
		}
		changed = true
	}
	if changed {
		if err := m.saveState(); err != nil {
			log.Printf("save refreshed tracker state failed: %v", err)
		}
	}
}

func (m *downloadManager) deleteTaskFiles(task *downloadTask, paths []string) error {
	if len(paths) == 0 {
		if task.Name != "" {
			paths = []string{filepath.Join(task.SavePath, task.Name)}
		}
	}
	for _, path := range paths {
		cleaned, err := safeChildPath(m.dataDir, path)
		if err != nil {
			return err
		}
		if err := os.Remove(cleaned); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete file %s: %w", cleaned, err)
		}
		removeEmptyParents(filepath.Dir(cleaned), m.dataDir)
	}
	return nil
}

func (m *downloadManager) getSettings() appSettings {
	m.settingsMu.RLock()
	settings := m.settings
	m.settingsMu.RUnlock()
	if settings.MaxActiveDownloads < 1 {
		settings.MaxActiveDownloads = 1
	}
	if settings.DownloadRateLimitBytes < 0 {
		settings.DownloadRateLimitBytes = 0
	}
	return settings
}

func defaultAppSettings() appSettings {
	return appSettings{
		MaxActiveDownloads:     3,
		DownloadRateLimitBytes: 0,
		WaitForFileSelection:   false,
	}
}

func (m *downloadManager) applySettings(settings appSettings) (appSettings, error) {
	if settings.MaxActiveDownloads < 1 {
		return appSettings{}, errors.New("maxActiveDownloads must be at least 1")
	}
	if settings.MaxActiveDownloads > 50 {
		return appSettings{}, errors.New("maxActiveDownloads must be 50 or less")
	}
	if settings.DownloadRateLimitBytes < 0 {
		return appSettings{}, errors.New("downloadRateLimitBytes must be zero or greater")
	}
	if settings.DownloadRateLimitBytes == 0 {
		m.rateLimiter.SetLimit(rate.Inf)
		m.rateLimiter.SetBurst(1 << 20)
	} else {
		m.rateLimiter.SetLimit(rate.Limit(settings.DownloadRateLimitBytes))
		m.rateLimiter.SetBurst(max(int(settings.DownloadRateLimitBytes), 1<<20))
	}

	m.settingsMu.Lock()
	m.settings = settings
	m.settingsMu.Unlock()
	return settings, nil
}

func (m *downloadManager) updateSettings(settings appSettings) (appSettings, error) {
	settings, err := m.applySettings(settings)
	if err != nil {
		return appSettings{}, err
	}
	if err := m.saveState(); err != nil {
		return appSettings{}, err
	}
	m.schedule()
	return settings, nil
}

func (m *downloadManager) status(task *downloadTask) taskStatus {
	var total, completed, missing int64
	var peers, pendingPeers, halfOpenPeers, activePeers, seeders int
	var knownPeers int
	var bytesReadData, bytesWasted int64
	metadataReady := false
	var files []taskFileStatus
	if task.torrent != nil {
		stats := task.torrent.Stats()
		peers = stats.TotalPeers
		pendingPeers = stats.PendingPeers
		halfOpenPeers = stats.HalfOpenPeers
		activePeers = stats.ActivePeers
		seeders = stats.ConnectedSeeders
		knownPeers = len(task.torrent.KnownSwarm())
		bytesReadData = stats.BytesReadData.Int64()
		bytesWasted = stats.ChunksReadWasted.Int64()
	}
	total, completed, missing, files, metadataReady = task.progressSnapshot()

	status, name, infoHash, savePath, createdAt, updatedAt, errText, speed, progressAt := task.refreshWithProgress(completed, total, missing)
	metadataAge := int64(0)
	if !metadataReady && status == "metadata" && !createdAt.IsZero() {
		metadataAge = int64(time.Since(createdAt).Seconds())
	}

	progress := 0.0
	if total > 0 {
		progress = float64(completed) * 100 / float64(total)
	}

	active := task.isActive()
	paused := task.isPaused()
	eta := int64(0)
	if speed > 1 && missing > 0 {
		eta = int64(float64(missing) / speed)
	}
	stalledSeconds := int64(0)
	if active && missing > 0 && speed < 1 && !progressAt.IsZero() {
		stalledSeconds = int64(time.Since(progressAt).Seconds())
		if stalledSeconds < 0 {
			stalledSeconds = 0
		}
	}
	stalled := active && missing > 0 && speed < 1 && stalledSeconds >= 30
	dhtServers := len(m.client.DhtServers())
	listenAddrs := listenerStrings(m.client.ListenAddrs())
	diagnosticCode, diagnostic := taskDiagnostic(status, metadataReady, active, paused, task.isAwaitingSelection(), missing, speed, metadataAge, stalledSeconds, peers, pendingPeers, halfOpenPeers, activePeers, seeders, len(task.Trackers), dhtServers, len(listenAddrs))
	return taskStatus{
		ID:                task.ID,
		Magnet:            task.Magnet,
		Name:              name,
		InfoHash:          infoHash,
		Status:            status,
		SavePath:          savePath,
		Source:            task.Source,
		Order:             task.Order,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
		Error:             errText,
		Active:            active,
		Paused:            paused,
		AwaitingSelection: task.isAwaitingSelection(),
		Diagnostic:        diagnostic,
		DiagnosticCode:    diagnosticCode,
		Queued:            metadataReady && status == "queued",
		QueuePosition:     m.queuePosition(task.ID),
		Trackers:          append([]string(nil), task.Trackers...),
		TrackerCount:      len(task.Trackers),
		Private:           task.Private,
		DHTEnabled:        dhtServers > 0,
		DHTServers:        dhtServers,
		ListenAddrs:       listenAddrs,
		KnownPeers:        knownPeers,
		BytesReadData:     bytesReadData,
		BytesWasted:       bytesWasted,
		MetadataAge:       metadataAge,
		ETASeconds:        eta,
		Stalled:           stalled,
		StalledSeconds:    stalledSeconds,
		TotalBytes:        total,
		CompletedBytes:    completed,
		DownloadSpeed:     speed,
		ProgressPercent:   progress,
		BytesMissing:      missing,
		Peers:             peers,
		PendingPeers:      pendingPeers,
		HalfOpenPeers:     halfOpenPeers,
		ActivePeers:       activePeers,
		Seeders:           seeders,
		MetadataReady:     metadataReady,
		Files:             files,
	}
}

func (m *downloadManager) watch(task *downloadTask) {
	select {
	case <-task.torrent.GotInfo():
		task.setName(task.torrent.Name())
		if !task.isActive() && !task.isPaused() {
			if m.getSettings().WaitForFileSelection && !task.hasFileSelectionSet() {
				task.markAwaitingSelection()
			} else {
				task.setState("queued", "")
			}
		}
		_ = m.saveState()
		m.schedule()
	case <-task.done:
		return
	case <-time.After(10 * time.Minute):
		task.setState("metadata_timeout", "metadata not found within 10 minutes")
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		select {
		case <-task.done:
			return
		default:
		}
		wasActive := task.isActive()
		total, completed, missing, _, _ := task.progressSnapshot()
		status, _, _, _, _, _, _, _, _ := task.refreshWithProgress(completed, total, missing)
		if status == "awaiting_selection" {
			if wasActive {
				task.setActive(false)
				m.schedule()
			}
			continue
		}
		if status == "completed" || status == "stopped" {
			if wasActive {
				task.setActive(false)
				m.schedule()
			}
			return
		}
	}
}

func (m *downloadManager) schedule() {
	settings := m.getSettings()
	limit := settings.MaxActiveDownloads
	if limit < 1 {
		limit = 1
	}

	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()
	sortTasks(tasks)

	active := 0
	for _, task := range tasks {
		if task.isPaused() {
			if task.isActive() {
				task.pauseDownload()
			}
			continue
		}
		if task.isAwaitingSelection() {
			if task.isActive() {
				task.pauseDownload()
			}
			continue
		}
		if !task.canSchedule() {
			if task.isActive() {
				task.pauseDownload()
			}
			continue
		}
		if active < limit {
			if !task.isActive() {
				task.startDownload()
			}
			active++
			continue
		}
		if task.isActive() {
			task.pauseDownload()
		} else {
			task.markQueued()
		}
	}
}

func (m *downloadManager) queuePosition(id string) int {
	settings := m.getSettings()
	limit := settings.MaxActiveDownloads
	if limit < 1 {
		limit = 1
	}

	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()
	sortTasks(tasks)

	activeOrEarlier := 0
	waiting := 0
	for _, task := range tasks {
		if task.isPaused() {
			continue
		}
		if task.isAwaitingSelection() {
			continue
		}
		if !task.canSchedule() {
			continue
		}
		if activeOrEarlier < limit {
			activeOrEarlier++
			if task.ID == id {
				return 0
			}
			continue
		}
		waiting++
		if task.ID == id {
			return waiting
		}
	}
	return 0
}

func (m *downloadManager) snapshotState() persistedState {
	m.settingsMu.RLock()
	settings := m.settings
	m.settingsMu.RUnlock()

	m.mu.RLock()
	tasks := make([]*downloadTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
	}
	m.mu.RUnlock()
	sortTasks(tasks)

	savedTasks := make([]persistedTask, 0, len(tasks))
	for _, task := range tasks {
		task.mu.Lock()
		if task.Status != "stopped" {
			savedTasks = append(savedTasks, persistedTask{
				ID:                task.ID,
				Magnet:            task.Magnet,
				Name:              task.Name,
				InfoHash:          task.InfoHash,
				Source:            task.Source,
				Trackers:          append([]string(nil), task.Trackers...),
				Private:           task.Private,
				MetaInfo:          append([]byte(nil), task.metaInfo...),
				Files:             task.selectedFilesLocked(),
				FilePriorities:    task.filePrioritiesLocked(),
				FileSelectionSet:  task.fileSelectionSet,
				Order:             task.Order,
				Paused:            task.Paused,
				AwaitingSelection: task.AwaitingSelection,
				CreatedAt:         task.CreatedAt,
				UpdatedAt:         task.UpdatedAt,
			})
		}
		task.mu.Unlock()
	}
	return persistedState{
		Settings: settings,
		Tasks:    savedTasks,
	}
}

func (m *downloadManager) saveState() error {
	if m.state == nil {
		return nil
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	return m.state.save(m.snapshotState())
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

func (t *downloadTask) setActive(active bool) {
	t.mu.Lock()
	t.Active = active
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
}

func (t *downloadTask) setOrder(order int64) {
	t.mu.Lock()
	t.Order = order
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
}

func (t *downloadTask) sortSnapshot() taskSortSnapshot {
	t.mu.Lock()
	snapshot := taskSortSnapshot{
		id:        t.ID,
		order:     t.Order,
		createdAt: t.CreatedAt,
	}
	t.mu.Unlock()
	return snapshot
}

func reindexTasks(tasks []*downloadTask) {
	now := time.Now()
	for i, task := range tasks {
		if task == nil {
			continue
		}
		task.mu.Lock()
		task.Order = int64(i + 1)
		task.UpdatedAt = now
		task.mu.Unlock()
	}
}

func (t *downloadTask) pauseByUser() {
	if t.torrent != nil {
		t.torrent.DisallowDataDownload()
	}
	t.mu.Lock()
	t.Active = false
	t.Paused = true
	t.Status = "paused"
	t.Error = ""
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
}

func (t *downloadTask) resumeByUser() {
	t.mu.Lock()
	if t.Status != "completed" && t.Status != "stopped" && !t.AwaitingSelection {
		t.Paused = false
		if t.torrent != nil && t.torrent.Info() != nil {
			t.Status = "queued"
		} else {
			t.Status = "metadata"
		}
		t.Error = ""
		t.UpdatedAt = time.Now()
	}
	t.mu.Unlock()
}

func (t *downloadTask) stop() {
	t.stopOnce.Do(func() {
		close(t.done)
	})
}

func (t *downloadTask) isPaused() bool {
	t.mu.Lock()
	paused := t.Paused
	t.mu.Unlock()
	return paused
}

func (t *downloadTask) isAwaitingSelection() bool {
	t.mu.Lock()
	awaiting := t.AwaitingSelection
	t.mu.Unlock()
	return awaiting
}

func (t *downloadTask) isActive() bool {
	t.mu.Lock()
	active := t.Active
	t.mu.Unlock()
	return active
}

func (t *downloadTask) canSchedule() bool {
	t.mu.Lock()
	status := t.Status
	paused := t.Paused
	awaitingSelection := t.AwaitingSelection
	torrentReady := t.torrent != nil && t.torrent.Info() != nil
	t.mu.Unlock()
	return torrentReady && !paused && !awaitingSelection && status != "completed" && status != "stopped" && status != "metadata_timeout"
}

func (t *downloadTask) startDownload() {
	t.mu.Lock()
	tor := t.torrent
	status := t.Status
	paused := t.Paused
	awaitingSelection := t.AwaitingSelection
	t.mu.Unlock()
	if tor == nil || tor.Info() == nil {
		return
	}
	t.mu.Lock()
	if t.torrent != tor || t.Paused || t.AwaitingSelection || status == "completed" || status == "stopped" || status == "metadata_timeout" || paused || awaitingSelection {
		t.mu.Unlock()
		return
	}
	t.Active = true
	t.Status = "downloading"
	t.Error = ""
	t.UpdatedAt = time.Now()
	t.mu.Unlock()

	tor.AllowDataDownload()
	t.applyFileSelection()

	shouldStop := false
	t.mu.Lock()
	switch {
	case t.Paused:
		t.Active = false
		t.Status = "paused"
		shouldStop = true
	case t.AwaitingSelection:
		t.Active = false
		t.Status = "awaiting_selection"
		shouldStop = true
	case t.Status == "completed" || t.Status == "stopped" || t.Status == "metadata_timeout":
		t.Active = false
		shouldStop = true
	}
	t.mu.Unlock()
	if shouldStop {
		tor.DisallowDataDownload()
	}
}

func (t *downloadTask) markAwaitingSelection() {
	if t.torrent != nil {
		t.torrent.DisallowDataDownload()
	}
	t.mu.Lock()
	if t.Status != "completed" && t.Status != "stopped" && !t.fileSelectionSet {
		t.Active = false
		t.AwaitingSelection = true
		t.Status = "awaiting_selection"
		t.Error = ""
		t.UpdatedAt = time.Now()
	}
	t.mu.Unlock()
}

func (t *downloadTask) hasFileSelectionSet() bool {
	t.mu.Lock()
	set := t.fileSelectionSet
	t.mu.Unlock()
	return set
}

func (t *downloadTask) setFileSelection(selection map[string]bool, explicit bool) {
	t.mu.Lock()
	if len(selection) == 0 {
		t.fileSelection = nil
	} else {
		t.fileSelection = make(map[string]bool, len(selection))
		for file := range selection {
			t.fileSelection[file] = true
		}
	}
	if explicit {
		t.fileSelectionSet = true
		t.AwaitingSelection = false
		if t.Status == "awaiting_selection" || t.Paused {
			t.Paused = false
			t.Status = "queued"
		}
		t.Error = ""
	}
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
	t.applyFileSelection()
}

func (t *downloadTask) setFilePriorities(priorities map[string]string, explicit bool) {
	t.mu.Lock()
	t.filePriorities = make(map[string]string, len(priorities))
	t.fileSelection = make(map[string]bool)
	for file, priority := range priorities {
		if priority == "" {
			continue
		}
		t.filePriorities[file] = priority
		if priority != "skip" {
			t.fileSelection[file] = true
		}
	}
	if len(t.fileSelection) == 0 {
		t.fileSelection = nil
	}
	if len(t.filePriorities) == 0 {
		t.filePriorities = nil
	}
	if explicit {
		t.fileSelectionSet = true
		t.AwaitingSelection = false
		if t.Status == "awaiting_selection" || t.Paused {
			t.Paused = false
			t.Status = "queued"
		}
		t.Error = ""
	}
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
	t.applyFileSelection()
}

func (t *downloadTask) selectedFilesLocked() []string {
	if len(t.filePriorities) > 0 {
		files := make([]string, 0, len(t.filePriorities))
		for file, priority := range t.filePriorities {
			if priority != "skip" {
				files = append(files, file)
			}
		}
		sort.Strings(files)
		return files
	}
	if len(t.fileSelection) == 0 {
		return nil
	}
	files := make([]string, 0, len(t.fileSelection))
	for file, selected := range t.fileSelection {
		if selected {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files
}

func (t *downloadTask) filePrioritiesLocked() map[string]string {
	if len(t.filePriorities) == 0 {
		return nil
	}
	result := make(map[string]string, len(t.filePriorities))
	for file, priority := range t.filePriorities {
		result[file] = priority
	}
	return result
}

func (t *downloadTask) applyFileSelection() {
	t.mu.Lock()
	tor := t.torrent
	selection := make(map[string]bool, len(t.fileSelection))
	priorities := make(map[string]string, len(t.filePriorities))
	fileSelectionSet := t.fileSelectionSet
	for file, selected := range t.fileSelection {
		selection[file] = selected
	}
	for file, priority := range t.filePriorities {
		priorities[file] = priority
	}
	t.mu.Unlock()
	if tor == nil || tor.Info() == nil {
		return
	}
	files := tor.Files()
	if !fileSelectionSet {
		return
	}
	if len(selection) == 0 {
		for _, file := range files {
			if file != nil {
				file.SetPriority(torrent.PiecePriorityNone)
			}
		}
		return
	}
	for _, file := range files {
		if file == nil {
			continue
		}
		priority := priorities[file.DisplayPath()]
		if priority == "" && selection[file.DisplayPath()] {
			priority = "normal"
		}
		switch priority {
		case "high":
			file.SetPriority(torrent.PiecePriorityHigh)
		case "normal":
			file.SetPriority(torrent.PiecePriorityNormal)
		default:
			file.SetPriority(torrent.PiecePriorityNone)
		}
	}
}

func (t *downloadTask) pauseDownload() {
	if t.torrent != nil {
		t.torrent.DisallowDataDownload()
	}
	t.mu.Lock()
	t.Active = false
	if t.Status != "completed" && t.Status != "stopped" && !t.Paused && !t.AwaitingSelection {
		t.Status = "queued"
	}
	t.UpdatedAt = time.Now()
	t.mu.Unlock()
}

func (t *downloadTask) markQueued() {
	t.mu.Lock()
	if t.Status != "completed" && t.Status != "stopped" && !t.Paused && !t.AwaitingSelection {
		t.Active = false
		t.Status = "queued"
		t.UpdatedAt = time.Now()
	}
	t.mu.Unlock()
}

func (t *downloadTask) filePaths() []string {
	t.mu.Lock()
	savePath := t.SavePath
	tor := t.torrent
	t.mu.Unlock()
	if tor == nil || tor.Info() == nil {
		return nil
	}
	files := tor.Files()
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		paths = append(paths, filepath.Join(savePath, filepath.FromSlash(file.Path())))
	}
	return paths
}

func (t *downloadTask) filesStatus() []taskFileStatus {
	_, _, _, files, _ := t.progressSnapshot()
	return files
}

func (t *downloadTask) progressSnapshot() (total, completed, missing int64, files []taskFileStatus, metadataReady bool) {
	t.mu.Lock()
	tor := t.torrent
	selection := make(map[string]bool, len(t.fileSelection))
	priorities := make(map[string]string, len(t.filePriorities))
	customSelection := len(t.fileSelection) > 0
	fileSelectionSet := t.fileSelectionSet
	for file, selected := range t.fileSelection {
		selection[file] = selected
	}
	for file, priority := range t.filePriorities {
		priorities[file] = priority
	}
	t.mu.Unlock()
	if tor == nil || tor.Info() == nil {
		return 0, 0, 0, nil, false
	}
	metadataReady = true
	torrentFiles := tor.Files()
	result := make([]taskFileStatus, 0, len(torrentFiles))
	for _, file := range torrentFiles {
		if file == nil {
			continue
		}
		size := file.Length()
		fileCompleted := file.BytesCompleted()
		progress := 0.0
		if size > 0 {
			progress = float64(fileCompleted) * 100 / float64(size)
		}
		path := file.DisplayPath()
		selected := true
		priority := priorities[path]
		if fileSelectionSet {
			selected = priority != "skip" && (priority != "" || selection[path])
		} else if customSelection {
			selected = selection[path]
		} else {
			selected = true
		}
		if fileSelectionSet && file.Priority() == torrent.PiecePriorityNone {
			selected = false
		}
		if priority == "" {
			priority = priorityLabel(file.Priority())
		}
		result = append(result, taskFileStatus{
			Path:            path,
			Size:            size,
			CompletedBytes:  fileCompleted,
			ProgressPercent: progress,
			Selected:        selected,
			Priority:        priority,
		})
		if selected {
			total += size
			completed += fileCompleted
		}
	}
	if total == 0 && !fileSelectionSet {
		total = tor.Length()
		completed = tor.BytesCompleted()
	}
	missing = total - completed
	if missing < 0 {
		missing = 0
	}
	return total, completed, missing, result, metadataReady
}

func (t *downloadTask) refreshWithProgress(completed, total, missing int64) (status, name, infoHash, savePath string, createdAt, updatedAt time.Time, errText string, speed float64, progressAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	delta := int64(0)
	if !t.last.At.IsZero() {
		elapsed := now.Sub(t.last.At).Seconds()
		if elapsed > 0 {
			delta = completed - t.last.Completed
			if delta < 0 {
				delta = 0
			}
			t.last.Speed = float64(delta) / elapsed
		}
	}
	t.last.At = now
	if delta > 0 || t.last.ProgressAt.IsZero() {
		t.last.ProgressAt = now
	}
	t.last.Completed = completed

	if t.torrent == nil || t.Status == "stopped" {
		return t.Status, t.Name, t.InfoHash, t.SavePath, t.CreatedAt, t.UpdatedAt, t.Error, t.last.Speed, t.last.ProgressAt
	}
	if t.Paused {
		t.Status = "paused"
		t.Active = false
	} else if t.torrent.Info() == nil {
		t.Status = "metadata"
	} else if t.AwaitingSelection {
		t.Status = "awaiting_selection"
		t.Active = false
	} else if total > 0 && missing == 0 {
		t.Status = "completed"
		t.Active = false
	} else if t.Active {
		t.Status = "downloading"
	} else if t.Paused {
		t.Status = "paused"
	} else if t.Status != "metadata_timeout" {
		t.Status = "queued"
	}
	t.UpdatedAt = time.Now()
	return t.Status, t.Name, t.InfoHash, t.SavePath, t.CreatedAt, t.UpdatedAt, t.Error, t.last.Speed, t.last.ProgressAt
}

func parseMagnet(raw string) (*metainfo.Magnet, error) {
	return parseMagnetWithDefaults(raw, defaultPublicTrackers)
}

func (m *downloadManager) parseMagnet(raw string) (*metainfo.Magnet, error) {
	return parseMagnetWithDefaults(raw, m.defaultTrackers())
}

func parseMagnetWithDefaults(raw string, defaults []string) (*metainfo.Magnet, error) {
	raw = cleanMagnetInput(raw)
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
	mi.Trackers = mergeTrackers(mi.Trackers, defaults)
	return &mi, nil
}

func (m *downloadManager) defaultTrackers() []string {
	if m != nil && m.discovery != nil {
		return m.discovery.Trackers()
	}
	return append([]string(nil), defaultPublicTrackers...)
}

func (m *downloadManager) mergeTrackersWithDefaults(primary []string) []string {
	return mergeTrackers(primary, m.defaultTrackers())
}

func cleanMagnetInput(raw string) string {
	raw = strings.TrimSpace(raw)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, raw)
}

func trackers(mi *metainfo.Magnet) []string {
	return mergeTrackers(mi.Trackers, nil)
}

func trackerTiers(trackers []string) [][]string {
	merged := mergeTrackers(trackers, nil)
	if len(merged) == 0 {
		return nil
	}
	tiers := make([][]string, 0, len(merged))
	for _, tracker := range merged {
		tiers = append(tiers, []string{tracker})
	}
	return tiers
}

func mergeTrackers(primary, fallback []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(fallback))
	result := make([]string, 0, len(primary)+len(fallback))
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return
		}
		key := strings.ToLower(parsed.String())
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		result = append(result, raw)
	}
	for _, tr := range primary {
		add(tr)
	}
	for _, tr := range fallback {
		add(tr)
	}
	return result
}

type discoveryManager struct {
	trackers atomic.Value
	dhtNodes atomic.Value
}

func newDiscoveryManager(defaultTrackers []string) *discoveryManager {
	m := &discoveryManager{}
	m.trackers.Store(mergeTrackers(defaultTrackers, nil))
	m.dhtNodes.Store(mergeHostPorts(publicDhtBootstrapNodes, nil))
	return m
}

func (m *discoveryManager) Trackers() []string {
	if m == nil {
		return append([]string(nil), defaultPublicTrackers...)
	}
	trackers, _ := m.trackers.Load().([]string)
	return append([]string(nil), trackers...)
}

func (m *discoveryManager) UpdateTrackers(trackers []string) bool {
	if m == nil {
		return false
	}
	merged := mergeTrackers(trackers, defaultPublicTrackers)
	if len(merged) == 0 {
		return false
	}
	if len(merged) > maxPublicTrackers {
		merged = merged[:maxPublicTrackers]
	}
	current := m.Trackers()
	if stringSlicesEqual(current, merged) {
		return false
	}
	m.trackers.Store(merged)
	return true
}

func (m *discoveryManager) DhtNodes() []string {
	if m == nil {
		return append([]string(nil), publicDhtBootstrapNodes...)
	}
	nodes, _ := m.dhtNodes.Load().([]string)
	return append([]string(nil), nodes...)
}

func (m *discoveryManager) UpdateDhtNodes(nodes []string) bool {
	if m == nil {
		return false
	}
	merged := mergeHostPorts(nodes, publicDhtBootstrapNodes)
	if len(merged) == 0 {
		return false
	}
	current := m.DhtNodes()
	if stringSlicesEqual(current, merged) {
		return false
	}
	m.dhtNodes.Store(merged)
	return true
}

func fetchPublicTrackers(ctx context.Context, sources []string, timeout time.Duration) ([]string, error) {
	client := &http.Client{Timeout: timeout}
	var errs []error
	var merged []string
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		trackers, err := fetchPublicTrackerSource(ctx, client, source)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		merged = mergeTrackers(merged, trackers)
		if len(merged) >= maxPublicTrackers {
			merged = merged[:maxPublicTrackers]
			break
		}
	}
	if len(merged) > 0 {
		return merged, nil
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, errors.New("no public tracker sources configured")
}

func fetchPublicTrackerSource(ctx context.Context, client *http.Client, source string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bt-go/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: status %s", source, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPublicTrackerSourceBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	trackers := parseTrackerList(raw)
	if len(trackers) == 0 {
		return nil, fmt.Errorf("%s: no trackers found", source)
	}
	return trackers, nil
}

func parseTrackerList(raw []byte) []string {
	lines := strings.FieldsFunc(string(raw), func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	trackers := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		trackers = append(trackers, line)
	}
	if len(trackers) > maxPublicTrackers {
		trackers = trackers[:maxPublicTrackers]
	}
	return mergeTrackers(trackers, nil)
}

func rankTrackersByHealth(ctx context.Context, trackers []string) []string {
	trackers = mergeTrackers(trackers, nil)
	if len(trackers) == 0 {
		return nil
	}
	type result struct {
		url string
		rtt time.Duration
		ok  bool
	}
	ctx, cancel := context.WithTimeout(ctx, publicTrackerHealthTimeout)
	defer cancel()
	sem := make(chan struct{}, publicTrackerHealthConcurrency)
	results := make(chan result, len(trackers))
	var wg sync.WaitGroup
	for _, tracker := range trackers {
		tracker := tracker
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- result{url: tracker}
				return
			}
			rtt, ok := probeTracker(ctx, tracker)
			results <- result{url: tracker, rtt: rtt, ok: ok}
		}()
	}
	wg.Wait()
	close(results)

	healthy := make([]result, 0, len(trackers))
	fallback := make([]string, 0, len(trackers))
	for item := range results {
		if item.ok {
			healthy = append(healthy, item)
		} else {
			fallback = append(fallback, item.url)
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		return healthy[i].rtt < healthy[j].rtt
	})
	ranked := make([]string, 0, len(trackers))
	for _, item := range healthy {
		ranked = append(ranked, item.url)
	}
	ranked = mergeTrackers(ranked, fallback)
	if len(ranked) > maxPublicTrackers {
		ranked = ranked[:maxPublicTrackers]
	}
	return ranked
}

func probeTracker(ctx context.Context, tracker string) (time.Duration, bool) {
	parsed, err := url.Parse(tracker)
	if err != nil {
		return 0, false
	}
	start := time.Now()
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return probeHTTPTracker(ctx, tracker, start)
	case "udp":
		return probeUDPTracker(ctx, parsed, start)
	default:
		return 0, false
	}
}

func probeHTTPTracker(ctx context.Context, tracker string, start time.Time) (time.Duration, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, tracker, nil)
	if err != nil {
		return 0, false
	}
	resp, err := publicTrackerProbeHTTPClient.Do(req)
	if err != nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, tracker, nil)
		if err != nil {
			return 0, false
		}
		resp, err = publicTrackerProbeHTTPClient.Do(req)
		if err != nil {
			return 0, false
		}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return time.Since(start), resp.StatusCode < 500
}

func probeUDPTracker(ctx context.Context, parsed *url.URL, start time.Time) (time.Duration, bool) {
	host := parsed.Host
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "80")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", host)
	if err != nil {
		return 0, false
	}
	defer conn.Close()
	stop := closeConnOnCancel(ctx, conn)
	defer stop()
	_ = conn.SetDeadline(probeDeadline(ctx, publicTrackerProbeTimeout))

	req := make([]byte, 16)
	copy(req[:8], []byte{0x00, 0x00, 0x04, 0x17, 0x27, 0x10, 0x19, 0x80})
	// action=0 connect
	if _, err := rand.Read(req[12:16]); err != nil {
		return 0, false
	}
	if _, err := conn.Write(req); err != nil {
		return 0, false
	}
	resp := make([]byte, 16)
	n, err := conn.Read(resp)
	if err != nil || n < 16 {
		return 0, false
	}
	if !bytes.Equal(resp[:8], req[8:16]) {
		return 0, false
	}
	return time.Since(start), true
}

func rankDhtNodesByHealth(ctx context.Context, nodes []string) []string {
	nodes = mergeHostPorts(nodes, nil)
	if len(nodes) == 0 {
		return nil
	}
	type result struct {
		node string
		rtt  time.Duration
		ok   bool
	}
	ctx, cancel := context.WithTimeout(ctx, publicDhtHealthTimeout)
	defer cancel()
	sem := make(chan struct{}, publicDhtHealthConcurrency)
	results := make(chan result, len(nodes))
	var wg sync.WaitGroup
	for _, node := range nodes {
		node := node
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- result{node: node}
				return
			}
			rtt, ok := probeDhtNode(ctx, node)
			results <- result{node: node, rtt: rtt, ok: ok}
		}()
	}
	wg.Wait()
	close(results)

	healthy := make([]result, 0, len(nodes))
	fallback := make([]string, 0, len(nodes))
	for item := range results {
		if item.ok {
			healthy = append(healthy, item)
		} else {
			fallback = append(fallback, item.node)
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		return healthy[i].rtt < healthy[j].rtt
	})
	ranked := make([]string, 0, len(nodes))
	for _, item := range healthy {
		ranked = append(ranked, item.node)
	}
	return mergeHostPorts(ranked, fallback)
}

func probeDhtNode(ctx context.Context, node string) (time.Duration, bool) {
	host := node
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "6881")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", host)
	if err != nil {
		return 0, false
	}
	defer conn.Close()
	stop := closeConnOnCancel(ctx, conn)
	defer stop()
	_ = conn.SetDeadline(probeDeadline(ctx, publicDhtProbeTimeout))

	transactionID := []byte{0, 0}
	if _, err := rand.Read(transactionID); err != nil {
		return 0, false
	}
	query := fmt.Sprintf("d1:ad2:id20:%se1:q4:ping1:t2:%s1:y1:qe", string(randomDhtNodeID()), string(transactionID))
	start := time.Now()
	if _, err := conn.Write([]byte(query)); err != nil {
		return 0, false
	}
	resp := make([]byte, 2048)
	n, err := conn.Read(resp)
	if err != nil || n == 0 {
		return 0, false
	}
	if !bytes.Contains(resp[:n], []byte("1:t2:"+string(transactionID))) {
		return 0, false
	}
	return time.Since(start), bytes.Contains(resp[:n], []byte("1:y1:r"))
}

func randomDhtNodeID() []byte {
	id := make([]byte, 20)
	if _, err := rand.Read(id); err != nil {
		copy(id, "bt-go-dht-node-id-00")
	}
	return id
}

func probeDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}

func closeConnOnCancel(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}

func mergeHostPorts(primary, fallback []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(fallback))
	result := make([]string, 0, len(primary)+len(fallback))
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		host, port, err := net.SplitHostPort(raw)
		if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
			return
		}
		key := strings.ToLower(net.JoinHostPort(host, port))
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		result = append(result, net.JoinHostPort(host, port))
	}
	for _, item := range primary {
		add(item)
	}
	for _, item := range fallback {
		add(item)
	}
	return result
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (m *discoveryManager) dhtStartingNodes(network string) dht.StartingNodesGetter {
	return func() ([]dht.Addr, error) {
		nodes := publicDhtBootstrapNodes
		if m != nil {
			nodes = m.DhtNodes()
		}
		addrs, _ := dht.ResolveHostPorts(nodes)
		if len(addrs) > 0 {
			return addrs, nil
		}
		return dht.GlobalBootstrapAddrs(network)
	}
}

func isPrivateTorrentInfo(info metainfo.Info) bool {
	return info.Private != nil && *info.Private
}

func (m *downloadManager) nextOrder() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var maxOrder int64
	for _, task := range m.tasks {
		task.mu.Lock()
		order := task.Order
		task.mu.Unlock()
		if order > maxOrder {
			maxOrder = order
		}
	}
	return maxOrder + 1
}

func sortTasks(tasks []*downloadTask) {
	sort.Slice(tasks, func(i, j int) bool {
		left := tasks[i].sortSnapshot()
		right := tasks[j].sortSnapshot()
		if left.order != right.order {
			if left.order == 0 {
				return false
			}
			if right.order == 0 {
				return true
			}
			return left.order < right.order
		}
		if !left.createdAt.Equal(right.createdAt) {
			return left.createdAt.Before(right.createdAt)
		}
		return left.id < right.id
	})
}

func persistedTaskOrderLess(left, right persistedTask) bool {
	if left.Order != right.Order {
		if left.Order == 0 {
			return false
		}
		if right.Order == 0 {
			return true
		}
		return left.Order < right.Order
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

type taskSortSnapshot struct {
	id        string
	order     int64
	createdAt time.Time
}

func selectionFromList(files []string) map[string]bool {
	if len(files) == 0 {
		return nil
	}
	selection := make(map[string]bool, len(files))
	for _, file := range files {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file != "" {
			selection[file] = true
		}
	}
	return selection
}

func prioritiesFromState(files []string, saved map[string]string) map[string]string {
	priorities := make(map[string]string, len(files)+len(saved))
	for _, file := range files {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file != "" {
			priorities[file] = "normal"
		}
	}
	for file, priority := range saved {
		file = strings.TrimSpace(filepath.ToSlash(file))
		if file == "" {
			continue
		}
		normalized, err := normalizeFilePriority(priority)
		if err != nil {
			continue
		}
		priorities[file] = normalized
	}
	if len(priorities) == 0 {
		return nil
	}
	return priorities
}

func normalizeFilePriority(priority string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(priority)) {
	case "", "normal", "selected":
		return "normal", nil
	case "high":
		return "high", nil
	case "skip", "none", "unselected":
		return "skip", nil
	default:
		return "", errors.New("file priority must be high, normal, or skip")
	}
}

func priorityLabel(priority torrent.PiecePriority) string {
	switch priority {
	case torrent.PiecePriorityHigh:
		return "high"
	case torrent.PiecePriorityNone:
		return "skip"
	default:
		return "normal"
	}
}

func listenerStrings(addrs []net.Addr) []string {
	if len(addrs) == 0 {
		return nil
	}
	result := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr != nil {
			result = append(result, addr.String())
		}
	}
	sort.Strings(result)
	return result
}

func taskDiagnostic(status string, metadataReady, active, paused, awaitingSelection bool, missing int64, speed float64, metadataAge, stalledSeconds int64, peers, pendingPeers, halfOpenPeers, activePeers, seeders, trackerCount, dhtServers, listenAddrs int) (string, string) {
	switch {
	case status == "completed":
		return "completed", "下载已完成。"
	case status == "metadata_timeout":
		return "metadata_timeout", "10 分钟内没有拿到元数据，建议换磁力或刷新发现源。"
	case paused:
		return "paused", "任务已暂停。"
	case awaitingSelection:
		return "awaiting_selection", "元数据已就绪，正在等待选择文件。"
	case !metadataReady:
		if trackerCount == 0 && dhtServers == 0 {
			return "metadata_no_sources", "正在获取元数据，但没有 Tracker，DHT 也不可用。"
		}
		if metadataAge >= 120 && peers == 0 {
			return "metadata_no_peers", "正在获取元数据，暂时没有发现 Peer。"
		}
		return "metadata_searching", "正在通过 DHT/Tracker 获取元数据。"
	case missing == 0:
		return "completed", "下载已完成。"
	case !active:
		if status == "queued" {
			return "queued", "任务已就绪，正在等待下载队列空位。"
		}
		return "waiting", "任务已就绪，等待调度启动。"
	case peers == 0:
		if trackerCount == 0 && dhtServers == 0 {
			return "no_sources", "没有可用发现源：没有 Tracker，DHT 也不可用。"
		}
		return "no_peers", "还没有发现 Peer，通常是资源冷门或网络/DHT/Tracker 受阻。"
	case activePeers == 0 && halfOpenPeers > 0:
		return "connecting", "已发现 Peer，正在建立连接。"
	case activePeers == 0 && pendingPeers > 0:
		return "pending_peers", "已发现 Peer，但还没有成功连接。"
	case seeders == 0 && speed < 1:
		return "no_seeders", "已连接 Peer，但暂时没有可用做种者或对方没有可下载数据。"
	case speed < 1 && stalledSeconds >= 30:
		return "stalled", "已连接但 30 秒以上没有下载速度，建议刷新发现源或稍后重试。"
	case speed < 1:
		return "warming_up", "已连接 Peer，正在等待数据块响应。"
	case listenAddrs == 0:
		return "listen_unavailable", "正在下载，但本机没有监听地址，可能影响被动连接。"
	default:
		return "downloading", "下载正常。"
	}
}

func safeChildPath(root, path string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("refuse path outside download directory: %s", absPath)
	}
	return absPath, nil
}

func openLocalPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path is required")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func removeEmptyParents(dir, root string) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	for {
		absDir, err := filepath.Abs(dir)
		if err != nil || absDir == absRoot {
			return
		}
		rel, err := filepath.Rel(absRoot, absDir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		if err := os.Remove(absDir); err != nil {
			return
		}
		dir = filepath.Dir(absDir)
	}
}

const (
	publicTrackerSourceTimeout     = 8 * time.Second
	publicTrackerHealthTimeout     = 12 * time.Second
	publicTrackerProbeTimeout      = 3 * time.Second
	publicTrackerHealthConcurrency = 16
	publicDhtHealthTimeout         = 10 * time.Second
	publicDhtProbeTimeout          = 3 * time.Second
	publicDhtHealthConcurrency     = 8
	maxPublicTrackers              = 50
	maxPublicTrackerSourceBytes    = 1 << 20
)

var publicTrackerProbeHTTPClient = &http.Client{
	Timeout: publicTrackerProbeTimeout,
}

var publicTrackerSources = []string{
	"https://raw.githubusercontent.com/ngosang/trackerslist/master/trackers_best.txt",
	"https://newtrackon.com/api/stable",
	"https://cf.trackerslist.com/best.txt",
}

var defaultPublicTrackers = []string{
	"https://tracker.opentrackr.org:443/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://tracker-udp.gbitt.info:80/announce",
	"udp://tracker.fnix.net:6969/announce",
	"udp://tracker.tryhackx.org:6969/announce",
	"udp://tracker.plx.im:6969/announce",
	"udp://tracker.t-1.org:6969/announce",
	"udp://tracker.bittor.pw:1337/announce",
	"udp://exodus.desync.com:6969/announce",
	"http://bt1.archive.org:6969/announce",
	"http://bt2.archive.org:6969/announce",
	"https://tracker.bt4g.com:443/announce",
	"http://tracker.bt4g.com:2095/announce",
	"https://tracker.renfei.net:443/announce",
	"http://tracker.renfei.net:8080/announce",
	"https://tracker.gbitt.info:443/announce",
	"http://tracker.mywaifu.best:6969/announce",
}

var publicDhtBootstrapNodes = []string{
	"router.utorrent.com:6881",
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"dht.libtorrent.org:25401",
	"router.bittorrent.com:8991",
	"dht.aelitis.com:6881",
	"dht.anacrolix.link:42069",
	"router.bittorrent.cloud:42069",
	"router.bt.ouinet.work:6881",
}

var (
	stateBucket = []byte("state")
	stateKey    = []byte("snapshot")
)

func openStateStore(dataDir string) (*stateStore, error) {
	db, err := bbolt.Open(filepath.Join(dataDir, "bt-go-state.db"), 0o600, &bbolt.Options{
		Timeout: time.Second,
	})
	if err != nil {
		return nil, err
	}
	store := &stateStore{db: db}
	if err := store.db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(stateBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *stateStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *stateStore) load() (persistedState, error) {
	var state persistedState
	if s == nil || s.db == nil {
		state.Settings = defaultAppSettings()
		return state, nil
	}
	err := s.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(stateBucket)
		if bucket == nil {
			return nil
		}
		raw := bucket.Get(stateKey)
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, &state)
	})
	if state.Settings.MaxActiveDownloads < 1 {
		state.Settings = defaultAppSettings()
	}
	if state.Settings.DownloadRateLimitBytes < 0 {
		state.Settings.DownloadRateLimitBytes = 0
	}
	return state, err
}

func (s *stateStore) save(state persistedState) error {
	if s == nil || s.db == nil {
		return nil
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(stateBucket)
		if err != nil {
			return err
		}
		return bucket.Put(stateKey, raw)
	})
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
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
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

func Getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func GetenvInt(key string, fallback int) int {
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
    button, textarea, input, select { font: inherit; }
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
    button.compact { height: 32px; padding: 0 10px; font-size: 12px; }
    input[type="number"], input[type="file"], input[type="search"], select {
      width: 100%;
      height: 42px;
      padding: 0 11px;
      border: 1px solid #cbd3df;
      border-radius: 7px;
      background: #fff;
      color: var(--text);
      outline: none;
    }
    input[type="file"] { padding-top: 8px; }
    select {
      appearance: none;
      background-image:
        linear-gradient(45deg, transparent 50%, #657083 50%),
        linear-gradient(135deg, #657083 50%, transparent 50%);
      background-position:
        calc(100% - 16px) 50%,
        calc(100% - 11px) 50%;
      background-size: 5px 5px, 5px 5px;
      background-repeat: no-repeat;
      padding-right: 30px;
    }
    .check-row {
      display: flex;
      align-items: center;
      gap: 8px;
      min-height: 42px;
      color: var(--text);
      font-size: 13px;
      font-weight: 700;
    }
    .check-row input { width: 16px; height: 16px; }
    input:focus, select:focus {
      border-color: var(--blue);
      box-shadow: 0 0 0 3px rgba(29, 102, 209, .12);
    }
    main {
      width: min(1440px, calc(100% - 24px));
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
    .settings {
      padding: 14px 16px;
      margin-bottom: 16px;
    }
    .settings-grid {
      display: grid;
      grid-template-columns: minmax(140px, 1fr) minmax(160px, 1fr) minmax(180px, auto) auto;
      gap: 12px;
      align-items: end;
    }
    label { display: block; color: var(--muted); font-size: 12px; font-weight: 750; margin-bottom: 6px; }
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
    .file-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 12px;
      align-items: center;
      margin-top: 12px;
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
    .table-tools { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
    .table-tools input[type="search"], .table-tools select {
      width: auto;
      min-width: 128px;
      height: 32px;
      font-size: 12px;
      font-weight: 700;
      background-color: #fff;
    }
    .table-tools input[type="search"] { min-width: 190px; }
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
    th.name-col { width: 31%; }
    th.progress-col { width: 18%; }
    th.small-col { width: 10%; }
    th.action-col { width: 210px; }
    .task-actions { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
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
    .pill.paused, .pill.stopped { background: #f1f3f6; color: #596273; }
    .status-stack { display: flex; align-items: center; gap: 7px; min-width: 0; }
    .status-stack .pill { flex: 0 0 auto; }
    .status-stack .muted { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .status-line {
      display: flex;
      align-items: center;
      gap: 7px;
      flex-wrap: wrap;
    }
    .diagnostic {
      display: block;
      max-width: 100%;
      color: #31405a;
      font-size: 12px;
      line-height: 1.35;
      white-space: normal;
      overflow-wrap: anywhere;
    }
    .meta-row {
      display: flex;
      flex-wrap: wrap;
      gap: 5px 10px;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.4;
    }
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
    .progress-text { margin-top: 6px; color: var(--muted); font-size: 12px; line-height: 1.35; }
    .metric { font-weight: 760; line-height: 1.35; overflow-wrap: anywhere; }
    .metric-stack {
      display: grid;
      gap: 4px;
      align-items: start;
    }
    .metric-stack strong {
      font-size: 14px;
      line-height: 1.2;
    }
    .short-code {
      display: inline-block;
      margin-top: 5px;
      color: #6b7484;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 12px;
      line-height: 1.2;
    }
    .muted { color: var(--muted); font-size: 12px; line-height: 1.4; }
    .file-detail-cell { padding: 0 12px 14px; background: #fbfcfe; }
    .file-panel {
      border: 1px solid #e1e6ee;
      border-radius: 8px;
      background: #fff;
      padding: 10px;
    }
    .file-panel-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      margin-bottom: 8px;
    }
    .file-panel-head strong { font-size: 13px; }
    .file-actions { display: flex; gap: 6px; flex-wrap: wrap; }
    .file-list { display: grid; gap: 6px; }
    .file-item {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 106px auto auto;
      gap: 8px;
      align-items: center;
      padding: 8px;
      border: 1px solid #edf0f5;
      border-radius: 7px;
      background: #fff;
    }
    .file-item select { height: 32px; font-size: 12px; font-weight: 700; }
    .file-name { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 700; }
    .file-meta { color: var(--muted); font-size: 12px; margin-top: 3px; }
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
      .settings-grid { grid-template-columns: 1fr; }
      .composer-grid { grid-template-columns: 1fr; }
      .actions { flex-direction: row; min-width: 0; }
      .actions button { flex: 1; }
      .file-row { grid-template-columns: 1fr; }
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
      .file-item { grid-template-columns: 1fr; }
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
    <div class="file-row">
      <input id="torrentFile" type="file" accept=".torrent,application/x-bittorrent">
      <button class="secondary" onclick="uploadTorrent()">上传 torrent</button>
    </div>
    <div id="message" class="message"></div>
  </section>

  <section class="panel settings">
    <div class="settings-grid">
      <div>
        <label for="maxActive">同时下载任务</label>
        <input id="maxActive" type="number" min="1" max="50" step="1" value="3">
      </div>
      <div>
        <label for="rateLimit">最大下载速度 MB/s</label>
        <input id="rateLimit" type="number" min="0" step="0.1" value="0">
      </div>
      <label class="check-row">
        <input id="waitForFiles" type="checkbox">
        <span>解析后先选文件</span>
      </label>
      <button onclick="saveSettings()">保存设置</button>
    </div>
  </section>

  <section class="panel tasks-panel">
    <div class="table-head">
      <strong>下载任务</strong>
      <div class="table-tools">
        <input id="taskSearch" type="search" placeholder="搜索任务" oninput="loadTasks()">
        <select id="taskFilter" onchange="loadTasks()">
          <option value="all">全部状态</option>
          <option value="downloading">下载中</option>
          <option value="queued">排队中</option>
          <option value="awaiting_selection">等待选文件</option>
          <option value="metadata">获取元数据</option>
          <option value="paused">已暂停</option>
          <option value="completed">已完成</option>
          <option value="metadata_timeout">元数据超时</option>
        </select>
        <button class="secondary compact" onclick="openDownloadDir()">打开目录</button>
        <button class="secondary compact" onclick="pauseAllTasks()">全部暂停</button>
        <button class="secondary compact" onclick="resumeAllTasks()">全部继续</button>
        <span id="lastRefresh">等待刷新</span>
      </div>
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

const fmtDuration = seconds => {
  seconds = Math.max(0, Math.floor(seconds || 0));
  if (seconds < 60) return seconds + " 秒";
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  if (minutes < 60) return minutes + " 分 " + rest + " 秒";
  const hours = Math.floor(minutes / 60);
  return hours + " 小时 " + (minutes % 60) + " 分";
};

const fmtETA = seconds => seconds ? fmtDuration(seconds) : "计算中";
const bytesToMB = bytes => Math.round((bytes || 0) / 1024 / 1024 * 10) / 10;
const mbToBytes = mb => Math.max(0, Math.round((Number(mb) || 0) * 1024 * 1024));
const expandedFiles = new Set();
const draftFilePriorities = new Map();
const validFilePriorities = new Set(["high", "normal", "skip"]);

const statusText = status => ({
  metadata: "获取元数据",
  awaiting_selection: "等待选文件",
  queued: "排队中",
  paused: "已暂停",
  metadata_timeout: "元数据超时",
  downloading: "下载中",
  completed: "已完成",
  stopped: "已停止"
}[status] || status || "未知");

function shortHash(value) {
  value = String(value || "");
  return value.length > 12 ? value.slice(0, 12) : value;
}

function shortDiagnostic(task) {
  const map = {
    completed: "完成",
    metadata_timeout: "元数据超时",
    paused: "已暂停",
    awaiting_selection: "待选文件",
    metadata_no_sources: "无发现源",
    metadata_no_peers: "暂无 Peer",
    metadata_searching: "查找资源中",
    queued: "排队中",
    waiting: "等待调度",
    no_sources: "无发现源",
    no_peers: "暂无 Peer",
    connecting: "连接 Peer",
    pending_peers: "连接中",
    no_seeders: "暂无做种",
    stalled: "速度停滞",
    warming_up: "等待数据",
    listen_unavailable: "监听受限",
    downloading: "下载正常"
  };
  return map[task.diagnosticCode] || statusText(task.status);
}

function filePriorityValue(file) {
  if (file && validFilePriorities.has(file.priority)) return file.priority;
  return file && file.selected ? "normal" : "skip";
}

function filePriorityFor(task, file) {
  const draft = draftFilePriorities.get(task.id);
  if (draft && draft.has(file.path)) return draft.get(file.path);
  return filePriorityValue(file);
}

function filePriorityText(priority) {
  return {
    high: "高级",
    normal: "普通",
    skip: "跳过"
  }[priority] || "普通";
}

function setDraftFilePriority(id, path, priority) {
  if (!validFilePriorities.has(priority)) return;
  let draft = draftFilePriorities.get(id);
  if (!draft) {
    draft = new Map();
    draftFilePriorities.set(id, draft);
  }
  draft.set(path, priority);
}

function updateFilePriorityState(select) {
  const row = select.closest(".file-item");
  const state = row && row.querySelector("[data-file-priority-state]");
  if (state) state.textContent = filePriorityText(select.value);
}

function updateFileSelectionCount(id) {
  const title = document.querySelector('[data-file-count-task="' + CSS.escape(id) + '"]');
  if (!title) return;
  const selects = document.querySelectorAll('[data-file-priority-task="' + CSS.escape(id) + '"]');
  let selected = 0;
  selects.forEach(select => {
    if (select.value !== "skip") selected++;
  });
  title.textContent = "文件 " + selected + "/" + selects.length;
}

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
  const magnet = input.value.replace(/[ \t\r\n]/g, "");
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

async function uploadTorrent() {
  const input = document.getElementById("torrentFile");
  const file = input.files && input.files[0];
  if (!file) {
    setMessage("请先选择 torrent 文件", "err");
    return;
  }
  const form = new FormData();
  form.append("file", file);
  setMessage("正在解析 torrent 文件...");
  try {
    const res = await fetch("/api/torrents", {
      method: "POST",
      body: form
    });
    const data = await res.json();
    if (!res.ok) {
      setMessage(data.error || "上传失败", "err");
      return;
    }
    input.value = "";
    setMessage("已添加任务：" + data.infoHash, "ok");
    await loadTasks();
  } catch (err) {
    setMessage("上传失败：" + err.message, "err");
  }
}

async function loadSettings() {
  try {
    const res = await fetch("/api/settings");
    const settings = await res.json();
    document.getElementById("maxActive").value = settings.maxActiveDownloads || 3;
    document.getElementById("rateLimit").value = bytesToMB(settings.downloadRateLimitBytes);
    document.getElementById("waitForFiles").checked = !!settings.waitForFileSelection;
  } catch (err) {
    setMessage("读取设置失败：" + err.message, "err");
  }
}

async function saveSettings() {
  const maxActive = Math.max(1, Math.min(50, parseInt(document.getElementById("maxActive").value, 10) || 1));
  const rateLimit = mbToBytes(document.getElementById("rateLimit").value);
  const waitForFileSelection = document.getElementById("waitForFiles").checked;
  try {
    const res = await fetch("/api/settings", {
      method: "PUT",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({
        maxActiveDownloads: maxActive,
        downloadRateLimitBytes: rateLimit,
        waitForFileSelection
      })
    });
    const data = await res.json();
    if (!res.ok) {
      setMessage(data.error || "设置保存失败", "err");
      return;
    }
    setMessage(rateLimit ? "已限制为 " + fmtBytes(rateLimit) + "/s" : "已设置为不限速", "ok");
    await loadTasks();
  } catch (err) {
    setMessage("设置保存失败：" + err.message, "err");
  }
}

async function pauseTask(id) {
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + "/pause", {method: "POST"});
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    setMessage(data.error || "暂停失败", "err");
  }
  await loadTasks();
}

async function resumeTask(id) {
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + "/resume", {method: "POST"});
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    setMessage(data.error || "继续失败", "err");
  }
  await loadTasks();
}

async function pauseAllTasks() {
  const res = await fetch("/api/tasks/pause-all", {method: "POST"});
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    setMessage(data.error || "全部暂停失败", "err");
  }
  await loadTasks();
}

async function resumeAllTasks() {
  const res = await fetch("/api/tasks/resume-all", {method: "POST"});
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    setMessage(data.error || "全部继续失败", "err");
  }
  await loadTasks();
}

async function moveTask(id, direction) {
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + "/move", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({direction})
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    setMessage(data.error || "调整顺序失败", "err");
  }
  await loadTasks();
}

async function refreshDiscovery(id) {
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + "/refresh-discovery", {method: "POST"});
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    setMessage(data.error || "刷新发现源失败", "err");
    return;
  }
  setMessage("已刷新 Tracker/DHT 发现源", "ok");
  await loadTasks();
}

async function deleteTask(id, deleteFiles) {
  const suffix = deleteFiles ? "?deleteFiles=true" : "";
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + suffix, {method: "DELETE"});
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    setMessage(data.error || "删除失败", "err");
  }
  expandedFiles.delete(id);
  draftFilePriorities.delete(id);
  await loadTasks();
}

async function openDownloadDir() {
  const res = await fetch("/api/open-download-dir", {method: "POST"});
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    setMessage(data.error || "打开下载目录失败", "err");
    return;
  }
  setMessage("已请求系统打开下载目录", "ok");
}

async function openTaskPath(id, path) {
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + "/open", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({path: path || ""})
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    setMessage(data.error || "打开路径失败", "err");
    return;
  }
  setMessage("已请求系统打开路径", "ok");
}

async function saveFileSelection(id) {
  const priorities = {};
  document.querySelectorAll('[data-file-priority-task="' + CSS.escape(id) + '"]').forEach(select => {
    priorities[select.dataset.filePath] = select.value;
  });
  const res = await fetch("/api/tasks/" + encodeURIComponent(id) + "/files", {
    method: "PUT",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({priorities})
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    setMessage(data.error || "文件优先级保存失败", "err");
    return;
  }
  draftFilePriorities.delete(id);
  expandedFiles.add(id);
  setMessage("已保存文件优先级", "ok");
  await loadTasks();
}

function setTaskFilePriority(id, priority) {
  document.querySelectorAll('[data-file-priority-task="' + CSS.escape(id) + '"]').forEach(select => {
    select.value = priority;
    setDraftFilePriority(id, select.dataset.filePath, priority);
    updateFilePriorityState(select);
  });
  updateFileSelectionCount(id);
}

function renderEmpty(tbody, filtered) {
  const tr = document.createElement("tr");
  const td = document.createElement("td");
  td.colSpan = 7;
  const empty = document.createElement("div");
  empty.className = "empty";
  const title = document.createElement("strong");
  title.textContent = filtered ? "没有匹配任务" : "暂无下载任务";
  const desc = document.createElement("span");
  desc.textContent = filtered ? "调整搜索词或状态筛选后再试" : "粘贴磁力链接后会在这里显示进度、速度和 Peer 状态";
  empty.append(title, desc);
  td.appendChild(empty);
  tr.appendChild(td);
  tbody.appendChild(tr);
}

function renderTaskFiles(tbody, task) {
  if (!task.files || !task.files.length) return;
  const tr = document.createElement("tr");
  tr.id = "files-" + task.id;
  tr.hidden = !expandedFiles.has(task.id);
  const td = document.createElement("td");
  td.colSpan = 7;
  td.className = "file-detail-cell";

  const panel = document.createElement("div");
  panel.className = "file-panel";
  const head = document.createElement("div");
  head.className = "file-panel-head";
  const title = document.createElement("strong");
  title.dataset.fileCountTask = task.id;
  const selectedCount = task.files.filter(file => filePriorityFor(task, file) !== "skip").length;
  title.textContent = "文件 " + selectedCount + "/" + task.files.length;
  const fileActions = document.createElement("div");
  fileActions.className = "file-actions";
  const all = document.createElement("button");
  all.className = "secondary compact";
  all.textContent = "全部普通";
  all.onclick = () => setTaskFilePriority(task.id, "normal");
  const high = document.createElement("button");
  high.className = "secondary compact";
  high.textContent = "全部高级";
  high.onclick = () => setTaskFilePriority(task.id, "high");
  const none = document.createElement("button");
  none.className = "secondary compact";
  none.textContent = "全部跳过";
  none.onclick = () => setTaskFilePriority(task.id, "skip");
  const save = document.createElement("button");
  save.className = "compact";
  save.textContent = "保存优先级";
  save.onclick = () => saveFileSelection(task.id);
  fileActions.append(all, high, none, save);
  head.append(title, fileActions);

  const list = document.createElement("div");
  list.className = "file-list";
  task.files.forEach(file => {
    const row = document.createElement("div");
    row.className = "file-item";
    const info = document.createElement("div");
    const name = document.createElement("span");
    name.className = "file-name";
    name.textContent = file.path;
    const meta = document.createElement("div");
    meta.className = "file-meta";
    meta.textContent = fmtBytes(file.completedBytes) + " / " + fmtBytes(file.size) + " · " + Math.max(0, Math.min(100, file.progressPercent || 0)).toFixed(2) + "%";
    info.append(name, meta);
    const select = document.createElement("select");
    select.dataset.filePriorityTask = task.id;
    select.dataset.filePath = file.path;
    [
      ["high", "高级"],
      ["normal", "普通"],
      ["skip", "跳过"]
    ].forEach(([value, label]) => {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = label;
      select.appendChild(option);
    });
    select.value = filePriorityFor(task, file);
    select.onchange = () => {
      setDraftFilePriority(task.id, file.path, select.value);
      updateFilePriorityState(select);
      updateFileSelectionCount(task.id);
    };
    const state = document.createElement("span");
    state.className = "muted";
    state.dataset.filePriorityState = "true";
    state.textContent = filePriorityText(select.value);
    const open = document.createElement("button");
    open.className = "secondary compact";
    open.textContent = "打开";
    open.onclick = () => openTaskPath(task.id, file.path);
    row.append(info, select, state, open);
    list.appendChild(row);
  });

  panel.append(head, list);
  td.appendChild(panel);
  tr.appendChild(td);
  tbody.appendChild(tr);
}

function renderTask(tbody, task) {
  if (task.status === "awaiting_selection" && task.files && task.files.length) {
    expandedFiles.add(task.id);
  }
  const pct = Math.max(0, Math.min(100, task.progressPercent || 0));
  const tr = document.createElement("tr");

  const nameWrap = document.createElement("div");
  const name = document.createElement("span");
  name.className = "task-name";
  name.textContent = task.name || "等待元数据";
  const hash = document.createElement("span");
  hash.className = "short-code";
  hash.textContent = shortHash(task.infoHash || task.id || "");
  nameWrap.append(name, hash);
  tr.appendChild(cell("名称", nameWrap));

  const statusWrap = document.createElement("div");
  statusWrap.className = "status-stack";
  statusWrap.title = shortDiagnostic(task);
  const pill = document.createElement("span");
  pill.className = "pill " + (task.status || "");
  pill.textContent = statusText(task.status);
  const meta = document.createElement("div");
  meta.className = "muted";
  meta.textContent = task.queued ? "队列 " + task.queuePosition : (task.active ? "运行" : "待命");
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
  progressText.textContent = pct.toFixed(2) + "% · ETA " + fmtETA(task.etaSeconds);
  progressWrap.append(bar, progressText);
  tr.appendChild(cell("进度", progressWrap));

  const speed = document.createElement("div");
  speed.className = "metric";
  speed.textContent = fmtBytes(task.downloadSpeed) + "/s";
  tr.appendChild(cell("速度", speed));

  const peers = document.createElement("div");
  peers.className = "metric-stack";
  peers.innerHTML = "<strong></strong><span class=\"muted\"></span>";
  peers.querySelector("strong").textContent = (task.activePeers || 0) + "/" + (task.peers || 0);
  peers.querySelector("span").textContent = "做种 " + (task.seeders || 0);
  tr.appendChild(cell("Peer", peers));

  const size = document.createElement("div");
  size.className = "metric";
  size.textContent = fmtBytes(task.completedBytes) + " / " + fmtBytes(task.totalBytes);
  tr.appendChild(cell("大小", size));

  const actions = document.createElement("div");
  actions.className = "task-actions";
  if (task.status !== "completed" && task.status !== "stopped") {
    const toggle = document.createElement("button");
    toggle.className = "secondary compact";
    toggle.textContent = task.paused ? "继续" : "暂停";
    toggle.onclick = () => task.paused ? resumeTask(task.id) : pauseTask(task.id);
    actions.appendChild(toggle);
  }
  const open = document.createElement("button");
  open.className = "secondary compact";
  open.textContent = "打开";
  open.onclick = () => openTaskPath(task.id, "");
  actions.appendChild(open);
  const refresh = document.createElement("button");
  refresh.className = "secondary compact";
  refresh.textContent = "刷新源";
  refresh.onclick = () => refreshDiscovery(task.id);
  actions.appendChild(refresh);
  const remove = document.createElement("button");
  remove.className = "danger compact";
  remove.textContent = "移除";
  remove.onclick = () => deleteTask(task.id, false);
  actions.appendChild(remove);
  const up = document.createElement("button");
  up.className = "secondary compact";
  up.textContent = "上移";
  up.onclick = () => moveTask(task.id, "up");
  actions.appendChild(up);
  if (task.files && task.files.length) {
    const files = document.createElement("button");
    files.className = "secondary compact";
    files.textContent = task.status === "awaiting_selection" ? "选文件" : "文件";
    files.onclick = () => {
      const row = document.getElementById("files-" + task.id);
      if (!row) return;
      row.hidden = !row.hidden;
      if (row.hidden) {
        expandedFiles.delete(task.id);
      } else {
        expandedFiles.add(task.id);
      }
    };
    actions.appendChild(files);
  }
  const removeFiles = document.createElement("button");
  removeFiles.className = "danger compact";
  removeFiles.textContent = "删文件";
  removeFiles.onclick = () => {
    if (confirm("确认移除任务并删除已下载文件？")) {
      deleteTask(task.id, true);
    }
  };
  actions.appendChild(removeFiles);
  tr.appendChild(cell("操作", actions));

  tbody.appendChild(tr);
  renderTaskFiles(tbody, task);
}

function filteredTasks(tasks) {
  const search = (document.getElementById("taskSearch")?.value || "").trim().toLowerCase();
  const filter = document.getElementById("taskFilter")?.value || "all";
  return tasks.filter(task => {
    if (filter !== "all" && task.status !== filter) {
      return false;
    }
    if (!search) {
      return true;
    }
    const fileText = (task.files || []).map(file => file.path).join(" ");
    const haystack = [
      task.name,
      task.infoHash,
      task.id,
      task.status,
      task.source,
      fileText
    ].filter(Boolean).join(" ").toLowerCase();
    return haystack.includes(search);
  });
}

async function loadTasks() {
  try {
    const res = await fetch("/api/tasks");
    const tasks = await res.json();
    const visibleTasks = filteredTasks(tasks);
    const tbody = document.getElementById("tasks");
    tbody.innerHTML = "";
    renderSummary(tasks);
    if (!visibleTasks.length) {
      renderEmpty(tbody, tasks.length > 0);
      return;
    }
    visibleTasks.forEach(task => renderTask(tbody, task));
  } catch (err) {
    setMessage("刷新失败：" + err.message, "err");
  }
}

loadSettings();
loadTasks();
setInterval(loadTasks, 2000);
</script>
</body>
</html>`
