package btgo

import core "bt-go"

type Engine struct {
	core *core.Engine
}

func NewEngine(dataDir string, listenPort int) (*Engine, error) {
	engine, err := core.NewEngine(dataDir, listenPort)
	if err != nil {
		return nil, err
	}
	return &Engine{core: engine}, nil
}

func (e *Engine) Close() {
	if e == nil || e.core == nil {
		return
	}
	e.core.Close()
	e.core = nil
}

func (e *Engine) FlushState() error {
	if e == nil || e.core == nil {
		return nil
	}
	return e.core.FlushState()
}

func (e *Engine) DataDir() string {
	if e == nil || e.core == nil {
		return ""
	}
	return e.core.DataDir()
}

func (e *Engine) AddMagnet(magnet string) (string, error) {
	return e.core.AddMagnet(magnet)
}

func (e *Engine) AddTorrentFile(path string) (string, error) {
	return e.core.AddTorrentFile(path)
}

func (e *Engine) ParseMagnet(magnet string) (string, error) {
	return e.core.ParseMagnet(magnet)
}

func (e *Engine) ListTasks() (string, error) {
	return e.core.ListTasks()
}

func (e *Engine) PauseTask(id string) (string, error) {
	return e.core.PauseTask(id)
}

func (e *Engine) ResumeTask(id string) (string, error) {
	return e.core.ResumeTask(id)
}

func (e *Engine) DeleteTask(id string, deleteFiles bool) (string, error) {
	return e.core.DeleteTask(id, deleteFiles)
}

func (e *Engine) PauseAll() error {
	return e.core.PauseAll()
}

func (e *Engine) ResumeAll() error {
	return e.core.ResumeAll()
}

func (e *Engine) MoveTask(id, direction string) (string, error) {
	return e.core.MoveTask(id, direction)
}

func (e *Engine) RefreshDiscovery(id string) (string, error) {
	return e.core.RefreshDiscovery(id)
}

func (e *Engine) GetSettings() (string, error) {
	return e.core.GetSettings()
}

func (e *Engine) UpdateSettings(maxActiveDownloads int, downloadRateLimitBytes int64, waitForFileSelection bool) (string, error) {
	return e.core.UpdateSettings(maxActiveDownloads, downloadRateLimitBytes, waitForFileSelection)
}

func (e *Engine) UpdateFilePriorities(id, prioritiesJSON string) (string, error) {
	return e.core.UpdateFilePriorities(id, prioritiesJSON)
}
