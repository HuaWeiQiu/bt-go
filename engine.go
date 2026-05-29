package btgo

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

type Engine struct {
	srv     *server
	cleanup func()
	dataDir string
}

func NewEngine(dataDir string, listenPort int) (*Engine, error) {
	srv, cleanup, absDir, err := newServer(dataDir, listenPort)
	if err != nil {
		return nil, err
	}
	return &Engine{srv: srv, cleanup: cleanup, dataDir: absDir}, nil
}

func (e *Engine) Close() {
	if e == nil || e.cleanup == nil {
		return
	}
	e.cleanup()
	e.cleanup = nil
}

func (e *Engine) FlushState() error {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return errors.New("engine is closed")
	}
	return e.srv.downloads.saveState()
}

func (e *Engine) DataDir() string {
	if e == nil {
		return ""
	}
	return e.dataDir
}

func (e *Engine) AddMagnet(magnet string) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	task, err := e.srv.downloads.add(magnet)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) AddTorrentFile(path string) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("torrent file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	task, err := e.srv.downloads.addTorrent(file)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) ParseMagnet(magnet string) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	mi, err := e.srv.downloads.parseMagnet(magnet)
	if err != nil {
		return "", err
	}
	return marshalJSON(map[string]any{
		"name":         mi.DisplayName,
		"infoHash":     mi.InfoHash.HexString(),
		"trackers":     trackers(mi),
		"trackerCount": len(mi.Trackers),
	})
}

func (e *Engine) ListTasks() (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	return marshalJSON(e.srv.downloads.list())
}

func (e *Engine) PauseTask(id string) (string, error) {
	task, err := e.taskAction(id, (*downloadManager).pause)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) ResumeTask(id string) (string, error) {
	task, err := e.taskAction(id, (*downloadManager).resume)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) DeleteTask(id string, deleteFiles bool) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	deleted, err := e.srv.downloads.delete(id, deleteFiles)
	if err != nil {
		return "", err
	}
	if !deleted {
		return "", errTaskNotFound
	}
	return marshalJSON(map[string]bool{"deleted": true})
}

func (e *Engine) PauseAll() error {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return errors.New("engine is closed")
	}
	return e.srv.downloads.pauseAll()
}

func (e *Engine) ResumeAll() error {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return errors.New("engine is closed")
	}
	return e.srv.downloads.resumeAll()
}

func (e *Engine) MoveTask(id, direction string) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	task, err := e.srv.downloads.move(id, direction)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) RefreshDiscovery(id string) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	task, err := e.srv.downloads.refreshDiscovery(id)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) GetSettings() (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	return marshalJSON(e.srv.downloads.getSettings())
}

func (e *Engine) UpdateSettings(maxActiveDownloads int, downloadRateLimitBytes int64, waitForFileSelection bool) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	settings, err := e.srv.downloads.updateSettings(appSettings{
		MaxActiveDownloads:     maxActiveDownloads,
		DownloadRateLimitBytes: downloadRateLimitBytes,
		WaitForFileSelection:   waitForFileSelection,
	})
	if err != nil {
		return "", err
	}
	return marshalJSON(settings)
}

func (e *Engine) UpdateFilePriorities(id, prioritiesJSON string) (string, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return "", errors.New("engine is closed")
	}
	var req fileSelectionRequest
	if strings.TrimSpace(prioritiesJSON) != "" {
		if err := json.Unmarshal([]byte(prioritiesJSON), &req.Priorities); err != nil {
			return "", err
		}
	}
	if req.Priorities == nil {
		req.Priorities = map[string]string{}
	}
	task, err := e.srv.downloads.updateFileSelection(id, req)
	if err != nil {
		return "", err
	}
	return marshalJSON(e.srv.downloads.status(task))
}

func (e *Engine) taskAction(id string, fn func(*downloadManager, string) (*downloadTask, error)) (*downloadTask, error) {
	if e == nil || e.srv == nil || e.srv.downloads == nil {
		return nil, errors.New("engine is closed")
	}
	return fn(e.srv.downloads, id)
}

func marshalJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return "", err
		}
		return "", err
	}
	return string(raw), nil
}
