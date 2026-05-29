package btgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

func buildFixtureTorrent(t *testing.T, name string, fill byte) (metainfo.MetaInfo, []byte) {
	t.Helper()
	info := metainfo.Info{
		Name:        name,
		PieceLength: 16 * 1024,
		Length:      11,
		Pieces:      bytes.Repeat([]byte{fill}, 20),
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{
		InfoBytes: infoBytes,
		Announce:  "https://torrent.ubuntu.com/announce",
	}
	var file bytes.Buffer
	if err := mi.Write(&file); err != nil {
		t.Fatal(err)
	}
	return mi, file.Bytes()
}

func buildMultiFileFixtureTorrent(t *testing.T) (metainfo.MetaInfo, []byte) {
	t.Helper()
	info := metainfo.Info{
		Name:        "multi-fixture",
		PieceLength: 16 * 1024,
		Files: []metainfo.FileInfo{
			{Path: []string{"video.mp4"}, Length: 11},
			{Path: []string{"sample.txt"}, Length: 7},
		},
		Pieces: bytes.Repeat([]byte{5}, 20),
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{
		InfoBytes: infoBytes,
		Announce:  "https://torrent.ubuntu.com/announce",
	}
	var file bytes.Buffer
	if err := mi.Write(&file); err != nil {
		t.Fatal(err)
	}
	return mi, file.Bytes()
}

func uploadFixtureTorrent(t *testing.T, handler http.Handler, fileName string, torrentBytes []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(torrentBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/torrents", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAddMagnetBeforeMetadataReturnsTask(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	handler := newMux(srv)
	body := `{"magnet":"magnet:?xt=urn:btih:0000000000000000000000000000000000000001&dn=bt-go-test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"metadata"`) {
		t.Fatalf("expected metadata status, got body=%s", rec.Body.String())
	}
	var status taskStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.TrackerCount < len(defaultPublicTrackers) {
		t.Fatalf("expected at least %d default trackers, got %d", len(defaultPublicTrackers), status.TrackerCount)
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/tasks/0000000000000000000000000000000000000001", nil)
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected delete status 200, got %d body=%s", delRec.Code, delRec.Body.String())
	}
}

func TestParseMagnetMergesDefaultTrackers(t *testing.T) {
	mi, err := parseMagnet("magnet:?xt=urn:btih:0000000000000000000000000000000000000001&dn=bt-go-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(mi.Trackers) != len(defaultPublicTrackers) {
		t.Fatalf("expected %d default trackers, got %d: %#v", len(defaultPublicTrackers), len(mi.Trackers), mi.Trackers)
	}
	if !strings.Contains(mi.String(), "tr=") {
		t.Fatalf("expected rebuilt magnet to contain trackers: %s", mi.String())
	}
}

func TestParseMagnetRemovesWhitespace(t *testing.T) {
	magnet := "  magnet:?xt=urn:btih:0000000000000000000000000000000000000001\n\t&dn=bt-go test  "
	mi, err := parseMagnet(magnet)
	if err != nil {
		t.Fatal(err)
	}
	if mi.InfoHash.HexString() != "0000000000000000000000000000000000000001" {
		t.Fatalf("expected info hash to parse after whitespace cleanup, got %s", mi.InfoHash.HexString())
	}
}

func TestParseMagnetKeepsExistingTrackers(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:dafc8c076ca2f3ed376eeae7c76a0d6be2415c45&dn=ubuntu-26.04-desktop-amd64.iso&tr=https%3A%2F%2Ftorrent.ubuntu.com%2Fannounce&tr=https%3A%2F%2Fipv6.torrent.ubuntu.com%2Fannounce"
	mi, err := parseMagnet(magnet)
	if err != nil {
		t.Fatal(err)
	}
	if len(mi.Trackers) != len(defaultPublicTrackers)+2 {
		b, _ := json.Marshal(mi.Trackers)
		t.Fatalf("expected ubuntu trackers plus defaults, got %d: %s", len(mi.Trackers), b)
	}
	if mi.Trackers[0] != "https://torrent.ubuntu.com/announce" {
		t.Fatalf("expected original tracker first, got %#v", mi.Trackers[:2])
	}
}

func TestParseMagnetWithDynamicDefaults(t *testing.T) {
	defaults := []string{
		"udp://tracker.example.com:6969/announce",
		"udp://tracker.example.com:6969/announce",
		"bad tracker",
		"https://tracker.example.org:443/announce",
	}
	mi, err := parseMagnetWithDefaults("magnet:?xt=urn:btih:0000000000000000000000000000000000000001&dn=bt-go-test&tr=udp%3A%2F%2Ftracker.example.com%3A6969%2Fannounce", defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(mi.Trackers) != 2 {
		t.Fatalf("expected existing tracker plus one valid fallback, got %#v", mi.Trackers)
	}
	if mi.Trackers[0] != "udp://tracker.example.com:6969/announce" {
		t.Fatalf("expected original tracker first, got %#v", mi.Trackers)
	}
	if mi.Trackers[1] != "https://tracker.example.org:443/announce" {
		t.Fatalf("expected valid fallback tracker, got %#v", mi.Trackers)
	}
}

func TestParseTrackerList(t *testing.T) {
	raw := []byte(`
# comment
udp://tracker.example.com:6969/announce
https://tracker.example.org:443/announce,udp://tracker.example.com:6969/announce
not a tracker
`)
	trackers := parseTrackerList(raw)
	if len(trackers) != 2 {
		t.Fatalf("expected two valid unique trackers, got %#v", trackers)
	}
}

func TestRankTrackersByHealthPrefersResponsiveTracker(t *testing.T) {
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fast.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer slow.Close()

	ranked := rankTrackersByHealth(context.Background(), []string{
		"udp://127.0.0.1:1/announce",
		slow.URL + "/announce",
		fast.URL + "/announce",
	})
	if len(ranked) != 3 {
		t.Fatalf("expected ranked trackers with fallback entries, got %#v", ranked)
	}
	if ranked[0] != fast.URL+"/announce" {
		t.Fatalf("expected fast tracker first, got %#v", ranked)
	}
}

func TestRankDhtNodesByHealthPrefersResponsiveNode(t *testing.T) {
	addr, closeServer := startTestDhtPingServer(t)
	defer closeServer()

	ranked := rankDhtNodesByHealth(context.Background(), []string{
		"127.0.0.1:1",
		addr,
	})
	if len(ranked) != 2 {
		t.Fatalf("expected ranked DHT nodes with fallback entries, got %#v", ranked)
	}
	if ranked[0] != addr {
		t.Fatalf("expected responsive DHT node first, got %#v", ranked)
	}
}

func startTestDhtPingServer(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			raw := string(buf[:n])
			idx := strings.Index(raw, "1:t2:")
			if idx < 0 || len(raw) < idx+6 {
				continue
			}
			transactionID := raw[idx+5 : idx+7]
			resp := "d1:rd2:id20:01234567890123456789e1:t2:" + transactionID + "1:y1:re"
			_, _ = conn.WriteTo([]byte(resp), peer)
		}
	}()
	return conn.LocalAddr().String(), func() {
		_ = conn.Close()
		<-done
	}
}

func TestPrivateTorrentDoesNotAddPublicTrackers(t *testing.T) {
	private := true
	info := metainfo.Info{
		Name:        "private-fixture.txt",
		PieceLength: 16 * 1024,
		Length:      11,
		Pieces:      bytes.Repeat([]byte{7}, 20),
		Private:     &private,
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{
		InfoBytes: infoBytes,
		Announce:  "https://private.example/announce",
	}
	var file bytes.Buffer
	if err := mi.Write(&file); err != nil {
		t.Fatal(err)
	}

	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	rec := uploadFixtureTorrent(t, newMux(srv), "private.torrent", file.Bytes())
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"trackerCount":1`) {
		t.Fatalf("expected only private tracker, got body=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tracker.opentrackr.org") {
		t.Fatalf("expected no public trackers for private torrent, got body=%s", rec.Body.String())
	}
}

func TestUpdateSettingsAppliesLimiter(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	handler := newMux(srv)
	body := `{"maxActiveDownloads":2,"downloadRateLimitBytes":1048576}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if srv.downloads.getSettings().MaxActiveDownloads != 2 {
		t.Fatalf("expected max active downloads to be updated")
	}
	if got := srv.downloads.rateLimiter.Limit(); got != rate.Limit(1048576) {
		t.Fatalf("expected limiter 1048576, got %v", got)
	}

	req = httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"maxActiveDownloads":2,"downloadRateLimitBytes":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := srv.downloads.rateLimiter.Limit(); got != rate.Inf {
		t.Fatalf("expected unlimited limiter, got %v", got)
	}
}

func TestUpdateSettingsRejectsInvalidConcurrency(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"maxActiveDownloads":0,"downloadRateLimitBytes":0}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newMux(srv).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUploadTorrentFileCreatesTask(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)

	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"maxActiveDownloads":3,"downloadRateLimitBytes":0,"waitForFileSelection":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected settings status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	_, torrentBytes := buildFixtureTorrent(t, "bt-go-fixture.txt", 1)
	rec = uploadFixtureTorrent(t, handler, "fixture.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"source":"torrent"`) {
		t.Fatalf("expected torrent source, got body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"downloading"`) {
		t.Fatalf("expected downloading status after torrent metadata is known, got body=%s", rec.Body.String())
	}
}

func TestStatePersistsSettingsAndMagnetTasks(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := newMux(srv)
	settingsBody := `{"maxActiveDownloads":2,"downloadRateLimitBytes":1048576}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(settingsBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected settings status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	taskBody := `{"magnet":"magnet:?xt=urn:btih:0000000000000000000000000000000000000002&dn=persisted-task"}`
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(taskBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected task status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	cleanup()

	restored, restoredCleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredCleanup()
	if restored.downloads.getSettings().MaxActiveDownloads != 2 {
		t.Fatalf("expected restored max active downloads")
	}
	if got := restored.downloads.rateLimiter.Limit(); got != rate.Limit(1048576) {
		t.Fatalf("expected restored rate limit, got %v", got)
	}
	if _, ok := restored.downloads.get("0000000000000000000000000000000000000002"); !ok {
		t.Fatalf("expected persisted task to be restored")
	}
}

func TestStatePersistsTorrentMetadata(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := newMux(srv)

	mi, torrentBytes := buildFixtureTorrent(t, "persisted-fixture.txt", 2)
	rec := uploadFixtureTorrent(t, handler, "persisted.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	infoHash := mi.HashInfoBytes().HexString()
	cleanup()

	restored, restoredCleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredCleanup()
	task, ok := restored.downloads.get(infoHash)
	if !ok {
		t.Fatalf("expected persisted torrent task to be restored")
	}
	status := restored.downloads.status(task)
	if !status.MetadataReady {
		t.Fatalf("expected persisted torrent metadata to be restored, got %#v", status)
	}
}

func TestPauseResumeTaskAndPersistPausedState(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := newMux(srv)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"maxActiveDownloads":3,"downloadRateLimitBytes":0,"waitForFileSelection":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected settings status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	mi, torrentBytes := buildFixtureTorrent(t, "paused-fixture.txt", 3)
	rec = uploadFixtureTorrent(t, handler, "paused.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	id := mi.HashInfoBytes().HexString()

	req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+id+"/pause", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected pause status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"paused":true`) {
		t.Fatalf("expected paused response, got body=%s", rec.Body.String())
	}
	cleanup()

	restored, restoredCleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredCleanup()
	task, ok := restored.downloads.get(id)
	if !ok {
		t.Fatalf("expected paused task to be restored")
	}
	status := restored.downloads.status(task)
	if !status.Paused || status.Active {
		t.Fatalf("expected restored paused inactive task, got %#v", status)
	}

	handler = newMux(restored)
	req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+id+"/resume", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected resume status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"paused":true`) {
		t.Fatalf("expected resumed response, got body=%s", rec.Body.String())
	}
	status = restored.downloads.status(task)
	if status.Paused || !status.Active {
		t.Fatalf("expected resumed active task, got %#v", status)
	}
}

func TestDeleteTaskCanRemoveDownloadedFile(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	mi, torrentBytes := buildFixtureTorrent(t, "delete-fixture.txt", 4)
	rec := uploadFixtureTorrent(t, handler, "delete.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	filePath := filepath.Join(dir, "delete-fixture.txt")
	if err := os.WriteFile(filePath, []byte("partial data"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+mi.HashInfoBytes().HexString()+"?deleteFiles=true", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected delete status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected downloaded file to be removed, stat err=%v", err)
	}
}

func TestUpdateFileSelectionAndPersistIt(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := newMux(srv)
	mi, torrentBytes := buildMultiFileFixtureTorrent(t)
	rec := uploadFixtureTorrent(t, handler, "multi.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	id := mi.HashInfoBytes().HexString()

	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+id+"/files", strings.NewReader(`{"files":["video.mp4"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected file selection status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"path":"video.mp4"`) || !strings.Contains(rec.Body.String(), `"selected":true`) {
		t.Fatalf("expected selected video file, got body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"path":"sample.txt"`) || !strings.Contains(rec.Body.String(), `"selected":false`) {
		t.Fatalf("expected skipped sample file, got body=%s", rec.Body.String())
	}
	cleanup()

	restored, restoredCleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredCleanup()
	task, ok := restored.downloads.get(id)
	if !ok {
		t.Fatalf("expected selected task to be restored")
	}
	status := restored.downloads.status(task)
	if len(status.Files) != 2 {
		t.Fatalf("expected 2 files, got %#v", status.Files)
	}
	selected := map[string]bool{}
	for _, file := range status.Files {
		selected[file.Path] = file.Selected
	}
	if !selected["video.mp4"] || selected["sample.txt"] {
		t.Fatalf("expected only video.mp4 selected, got %#v", selected)
	}
}

func TestUpdateFilePrioritiesAndPersistThem(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := newMux(srv)
	mi, torrentBytes := buildMultiFileFixtureTorrent(t)
	rec := uploadFixtureTorrent(t, handler, "multi.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	id := mi.HashInfoBytes().HexString()

	body := `{"priorities":{"video.mp4":"high","sample.txt":"skip"}}`
	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+id+"/files", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected file priority status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"path":"video.mp4"`) || !strings.Contains(rec.Body.String(), `"priority":"high"`) || !strings.Contains(rec.Body.String(), `"selected":true`) {
		t.Fatalf("expected high-priority video file, got body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"path":"sample.txt"`) || !strings.Contains(rec.Body.String(), `"priority":"skip"`) || !strings.Contains(rec.Body.String(), `"selected":false`) {
		t.Fatalf("expected skipped sample file, got body=%s", rec.Body.String())
	}
	cleanup()

	restored, restoredCleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredCleanup()
	task, ok := restored.downloads.get(id)
	if !ok {
		t.Fatalf("expected priority task to be restored")
	}
	status := restored.downloads.status(task)
	priorities := map[string]string{}
	selected := map[string]bool{}
	for _, file := range status.Files {
		priorities[file.Path] = file.Priority
		selected[file.Path] = file.Selected
	}
	if priorities["video.mp4"] != "high" || !selected["video.mp4"] {
		t.Fatalf("expected restored high video priority, got priorities=%#v selected=%#v", priorities, selected)
	}
	if priorities["sample.txt"] != "skip" || selected["sample.txt"] {
		t.Fatalf("expected restored skipped sample priority, got priorities=%#v selected=%#v", priorities, selected)
	}
}

func TestUpdateFileSelectionRejectsUnknownFile(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	mi, torrentBytes := buildMultiFileFixtureTorrent(t)
	rec := uploadFixtureTorrent(t, handler, "multi.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+mi.HashInfoBytes().HexString()+"/files", strings.NewReader(`{"files":["missing.bin"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateFilePriorityRejectsInvalidValue(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	mi, torrentBytes := buildMultiFileFixtureTorrent(t)
	rec := uploadFixtureTorrent(t, handler, "multi.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+mi.HashInfoBytes().HexString()+"/files", strings.NewReader(`{"priorities":{"video.mp4":"urgent"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTaskOpenPathIsLimitedToDownloadDirectory(t *testing.T) {
	srv, cleanup, dir, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	mi, torrentBytes := buildMultiFileFixtureTorrent(t)
	rec := uploadFixtureTorrent(t, handler, "multi.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	id := mi.HashInfoBytes().HexString()
	expected := filepath.Join(dir, "multi-fixture", "video.mp4")
	if err := os.MkdirAll(filepath.Dir(expected), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(expected, []byte("partial data"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := srv.downloads.taskOpenPath(id, "video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if path != expected {
		t.Fatalf("expected open path %s, got %s", expected, path)
	}
	if _, err := srv.downloads.taskOpenPath(id, "../outside.txt"); err == nil {
		t.Fatalf("expected outside path to be rejected")
	}
	if _, err := srv.downloads.taskOpenPath(id, "missing.bin"); err == nil {
		t.Fatalf("expected unknown file to be rejected")
	}
}

func TestWaitForFileSelectionStopsTorrentUntilFilesAreSelected(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)

	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"maxActiveDownloads":3,"downloadRateLimitBytes":0,"waitForFileSelection":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected settings status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	mi, torrentBytes := buildMultiFileFixtureTorrent(t)
	rec = uploadFixtureTorrent(t, handler, "multi.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"awaiting_selection"`) || !strings.Contains(rec.Body.String(), `"awaitingSelection":true`) {
		t.Fatalf("expected awaiting selection response, got body=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("expected inactive awaiting task, got body=%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPut, "/api/tasks/"+mi.HashInfoBytes().HexString()+"/files", strings.NewReader(`{"files":["video.mp4"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected file selection status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"awaitingSelection":true`) || !strings.Contains(rec.Body.String(), `"active":true`) {
		t.Fatalf("expected selected task to start, got body=%s", rec.Body.String())
	}
}

func TestPauseAllAndResumeAll(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"maxActiveDownloads":3,"downloadRateLimitBytes":0,"waitForFileSelection":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected settings status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	firstMI, firstBytes := buildFixtureTorrent(t, "batch-one.txt", 6)
	secondMI, secondBytes := buildFixtureTorrent(t, "batch-two.txt", 7)
	if rec := uploadFixtureTorrent(t, handler, "one.torrent", firstBytes); rec.Code != http.StatusCreated {
		t.Fatalf("expected first upload 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := uploadFixtureTorrent(t, handler, "two.torrent", secondBytes); rec.Code != http.StatusCreated {
		t.Fatalf("expected second upload 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/tasks/pause-all", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected pause-all 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{firstMI.HashInfoBytes().HexString(), secondMI.HashInfoBytes().HexString()} {
		task, _ := srv.downloads.get(id)
		status := srv.downloads.status(task)
		if !status.Paused || status.Active {
			t.Fatalf("expected paused inactive task %s, got %#v", id, status)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/tasks/resume-all", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected resume-all 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{firstMI.HashInfoBytes().HexString(), secondMI.HashInfoBytes().HexString()} {
		task, _ := srv.downloads.get(id)
		status := srv.downloads.status(task)
		if status.Paused || !status.Active {
			t.Fatalf("expected resumed active task %s, got %#v", id, status)
		}
	}
}

func TestMoveTaskPersistsOrder(t *testing.T) {
	dir := t.TempDir()
	srv, cleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := newMux(srv)

	firstMI, firstBytes := buildFixtureTorrent(t, "order-one.txt", 8)
	secondMI, secondBytes := buildFixtureTorrent(t, "order-two.txt", 9)
	if rec := uploadFixtureTorrent(t, handler, "one.torrent", firstBytes); rec.Code != http.StatusCreated {
		t.Fatalf("expected first upload 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := uploadFixtureTorrent(t, handler, "two.torrent", secondBytes); rec.Code != http.StatusCreated {
		t.Fatalf("expected second upload 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	secondID := secondMI.HashInfoBytes().HexString()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+secondID+"/move", strings.NewReader(`{"direction":"top"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected move status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	list := srv.downloads.list()
	if len(list) != 2 || list[0].ID != secondID {
		t.Fatalf("expected second task first, got %#v", list)
	}
	cleanup()

	restored, restoredCleanup, _, err := newServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredCleanup()
	list = restored.downloads.list()
	if len(list) != 2 || list[0].ID != secondID || list[1].ID != firstMI.HashInfoBytes().HexString() {
		t.Fatalf("expected order to persist, got %#v", list)
	}
}

func TestTaskStatusIncludesDiagnostics(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	body := `{"magnet":"magnet:?xt=urn:btih:0000000000000000000000000000000000000003&dn=diagnostic-task"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"diagnosticCode":"metadata_searching"`) {
		t.Fatalf("expected metadata diagnostic, got body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"dhtEnabled":true`) {
		t.Fatalf("expected DHT diagnostic fields, got body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"listenAddrs"`) {
		t.Fatalf("expected listen address diagnostic field, got body=%s", rec.Body.String())
	}
}

func TestRefreshDiscoveryEndpoint(t *testing.T) {
	srv, cleanup, _, err := newServer(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	handler := newMux(srv)
	mi, torrentBytes := buildFixtureTorrent(t, "refresh-fixture.txt", 10)
	rec := uploadFixtureTorrent(t, handler, "refresh.torrent", torrentBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+mi.HashInfoBytes().HexString()+"/refresh-discovery", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected refresh status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"diagnosticCode"`) {
		t.Fatalf("expected diagnostic response, got body=%s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/tasks/missing/refresh-discovery", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected missing refresh status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}
