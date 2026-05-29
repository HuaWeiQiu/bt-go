package com.btgo.mobile;

import android.Manifest;
import android.content.ComponentName;
import android.content.Context;
import android.content.Intent;
import android.content.ServiceConnection;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.text.InputType;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.TextView;

import android.app.Activity;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.LinkedHashMap;
import java.util.Map;

import btgo.Engine;

public class MainActivity extends Activity {
    private final Handler handler = new Handler(Looper.getMainLooper());
    private Engine engine;
    private DownloadService downloadService;
    private EditText magnetInput;
    private EditText downloadDirInput;
    private EditText maxActiveInput;
    private EditText rateLimitInput;
    private TextView message;
    private LinearLayout rootView;
    private LinearLayout taskList;
    private String filePageTaskId;
    private JSONObject filePageTask;
    private String pendingFilePageTaskId;
    private final Map<String, Map<String, String>> draftPriorities = new LinkedHashMap<>();
    private final Map<String, Boolean> expandedFiles = new LinkedHashMap<>();
    private final Map<String, Boolean> seenTaskFiles = new LinkedHashMap<>();

    private final Runnable refreshLoop = new Runnable() {
        @Override
        public void run() {
            refreshTasks();
            handler.postDelayed(this, 1500);
        }
    };

    private final ServiceConnection serviceConnection = new ServiceConnection() {
        @Override
        public void onServiceConnected(ComponentName name, IBinder service) {
            DownloadService.LocalBinder binder = (DownloadService.LocalBinder) service;
            downloadService = binder.getService();
            engine = downloadService.engine();
            updateDownloadDirInput(downloadService.downloadDirPath());
            if (engine == null) {
                setMessage(downloadService.startupError());
                return;
            }
            loadPerformanceSettings();
            refreshTasks();
            handler.post(refreshLoop);
        }

        @Override
        public void onServiceDisconnected(ComponentName name) {
            downloadService = null;
            engine = null;
        }
    };

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        requestNotificationPermission();
        configureSystemBars();
        setContentView(buildView());
        Intent intent = new Intent(this, DownloadService.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent);
        } else {
            startService(intent);
        }
        bindService(intent, serviceConnection, Context.BIND_AUTO_CREATE);
    }

    @Override
    protected void onDestroy() {
        handler.removeCallbacks(refreshLoop);
        try {
            unbindService(serviceConnection);
        } catch (IllegalArgumentException ignored) {
        }
        super.onDestroy();
    }

    @Override
    public void onBackPressed() {
        if (filePageTaskId != null) {
            closeFilePage();
            return;
        }
        super.onBackPressed();
    }

    private View buildView() {
        int pad = dp(14);
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setClipToPadding(false);
        scroll.setBackgroundColor(Color.rgb(247, 249, 252));
        rootView = new LinearLayout(this);
        rootView.setOrientation(LinearLayout.VERTICAL);
        rootView.setPadding(pad, pad, pad, pad);
        rootView.setBackgroundColor(Color.rgb(247, 249, 252));
        scroll.addView(rootView);
        renderTaskPageShell();
        applyContentInsets(scroll);
        return scroll;
    }

    private void renderTaskPageShell() {
        if (rootView == null) {
            return;
        }
        rootView.removeAllViews();

        TextView title = new TextView(this);
        title.setText("bt-go");
        title.setTextSize(26);
        title.setTextColor(Color.rgb(21, 25, 34));
        title.setGravity(Gravity.START);
        rootView.addView(title);

        magnetInput = new EditText(this);
        magnetInput.setHint("粘贴 magnet:?xt=urn:btih:...");
        magnetInput.setSingleLine(false);
        magnetInput.setMinLines(3);
        rootView.addView(magnetInput, new LinearLayout.LayoutParams(-1, -2));

        TextView dirLabel = text("下载目录", 14, Color.rgb(21, 25, 34));
        dirLabel.setPadding(0, dp(10), 0, 0);
        rootView.addView(dirLabel);

        downloadDirInput = new EditText(this);
        downloadDirInput.setHint("默认应用下载目录，或输入可写路径");
        downloadDirInput.setSingleLine(false);
        downloadDirInput.setMinLines(2);
        rootView.addView(downloadDirInput, new LinearLayout.LayoutParams(-1, -2));
        if (downloadService != null) {
            updateDownloadDirInput(downloadService.downloadDirPath());
        }

        LinearLayout dirActions = new LinearLayout(this);
        dirActions.setOrientation(LinearLayout.HORIZONTAL);
        dirActions.setGravity(Gravity.CENTER_VERTICAL);
        rootView.addView(dirActions);

        Button saveDir = button("保存目录");
        saveDir.setOnClickListener(v -> saveDownloadDir());
        dirActions.addView(saveDir, new LinearLayout.LayoutParams(0, dp(40), 1));

        Button resetDir = button("恢复默认");
        resetDir.setOnClickListener(v -> resetDownloadDir());
        dirActions.addView(resetDir, new LinearLayout.LayoutParams(0, dp(40), 1));

        TextView perfLabel = text("性能设置", 14, Color.rgb(21, 25, 34));
        perfLabel.setPadding(0, dp(12), 0, 0);
        rootView.addView(perfLabel);

        maxActiveInput = new EditText(this);
        maxActiveInput.setHint("同时下载任务数，建议单任务测速填 1");
        maxActiveInput.setSingleLine(true);
        maxActiveInput.setInputType(InputType.TYPE_CLASS_NUMBER);
        rootView.addView(maxActiveInput, new LinearLayout.LayoutParams(-1, -2));

        rateLimitInput = new EditText(this);
        rateLimitInput.setHint("下载限速 MB/s，0 表示不限速");
        rateLimitInput.setSingleLine(true);
        rateLimitInput.setInputType(InputType.TYPE_CLASS_NUMBER | InputType.TYPE_NUMBER_FLAG_DECIMAL);
        rootView.addView(rateLimitInput, new LinearLayout.LayoutParams(-1, -2));

        LinearLayout perfActions = new LinearLayout(this);
        perfActions.setOrientation(LinearLayout.HORIZONTAL);
        perfActions.setGravity(Gravity.CENTER_VERTICAL);
        rootView.addView(perfActions);

        Button savePerf = button("保存性能");
        savePerf.setOnClickListener(v -> savePerformanceSettings());
        perfActions.addView(savePerf, new LinearLayout.LayoutParams(0, dp(40), 1));

        Button singleTask = button("单任务优先");
        singleTask.setOnClickListener(v -> setSingleTaskMode());
        perfActions.addView(singleTask, new LinearLayout.LayoutParams(0, dp(40), 1));

        if (engine != null) {
            loadPerformanceSettings();
        }

        LinearLayout actions = new LinearLayout(this);
        actions.setOrientation(LinearLayout.HORIZONTAL);
        actions.setGravity(Gravity.CENTER_VERTICAL);
        rootView.addView(actions);

        Button add = button("添加下载");
        add.setOnClickListener(v -> addTask());
        actions.addView(add, new LinearLayout.LayoutParams(0, dp(44), 1));

        Button refresh = button("刷新");
        refresh.setOnClickListener(v -> refreshTasks());
        actions.addView(refresh, new LinearLayout.LayoutParams(0, dp(44), 1));

        message = new TextView(this);
        message.setTextColor(Color.rgb(101, 112, 131));
        message.setPadding(0, dp(8), 0, dp(8));
        rootView.addView(message);

        taskList = new LinearLayout(this);
        taskList.setOrientation(LinearLayout.VERTICAL);
        rootView.addView(taskList);
    }

    private void updateDownloadDirInput(String path) {
        if (downloadDirInput != null) {
            downloadDirInput.setText(path == null ? "" : path);
        }
    }

    private void saveDownloadDir() {
        if (downloadService == null) {
            setMessage("下载核心启动中");
            return;
        }
        String path = downloadDirInput.getText().toString();
        runInBackground(() -> {
            try {
                String actual = downloadService.setDownloadDir(path);
                engine = downloadService.engine();
                runOnUiThread(() -> {
                    updateDownloadDirInput(actual);
                    setMessage("已切换下载目录");
                    loadPerformanceSettings();
                    refreshTasks();
                });
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private void resetDownloadDir() {
        if (downloadService == null) {
            setMessage("下载核心启动中");
            return;
        }
        runInBackground(() -> {
            try {
                String actual = downloadService.resetDownloadDir();
                engine = downloadService.engine();
                runOnUiThread(() -> {
                    updateDownloadDirInput(actual);
                    setMessage("已恢复默认下载目录");
                    loadPerformanceSettings();
                    refreshTasks();
                });
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private void loadPerformanceSettings() {
        if (engine == null) {
            return;
        }
        runInBackground(() -> {
            try {
                String json = engine.getSettings();
                runOnUiThread(() -> updatePerformanceInputs(json));
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private void updatePerformanceInputs(String json) {
        if (maxActiveInput == null || rateLimitInput == null) {
            return;
        }
        try {
            JSONObject settings = new JSONObject(json);
            int maxActive = Math.max(1, settings.optInt("maxActiveDownloads", 1));
            long rateLimit = Math.max(0, settings.optLong("downloadRateLimitBytes", 0));
            maxActiveInput.setText(String.valueOf(maxActive));
            rateLimitInput.setText(formatMB(rateLimit));
        } catch (Exception e) {
            setMessage(e.getMessage());
        }
    }

    private void savePerformanceSettings() {
        if (engine == null) {
            setMessage("下载核心启动中");
            return;
        }
        final int maxActive;
        final long rateLimit;
        try {
            maxActive = parseMaxActiveDownloads();
            rateLimit = parseDownloadRateLimitBytes();
        } catch (Exception e) {
            setMessage(e.getMessage());
            return;
        }
        runInBackground(() -> {
            try {
                String json = engine.updateSettings(maxActive, rateLimit, false);
                runOnUiThread(() -> {
                    updatePerformanceInputs(json);
                    setMessage("已保存性能设置：" + maxActive + " 个任务，限速 " + fmtBytes(rateLimit) + "/s");
                    refreshTasks();
                });
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private void setSingleTaskMode() {
        if (maxActiveInput != null) {
            maxActiveInput.setText("1");
        }
        if (rateLimitInput != null) {
            rateLimitInput.setText("0");
        }
        savePerformanceSettings();
    }

    private int parseMaxActiveDownloads() throws Exception {
        String raw = maxActiveInput == null ? "" : maxActiveInput.getText().toString().trim();
        if (raw.isEmpty()) {
            return 1;
        }
        int value = Integer.parseInt(raw);
        if (value < 1 || value > 50) {
            throw new Exception("同时下载任务数必须在 1 到 50 之间");
        }
        return value;
    }

    private long parseDownloadRateLimitBytes() throws Exception {
        String raw = rateLimitInput == null ? "" : rateLimitInput.getText().toString().trim();
        if (raw.isEmpty()) {
            return 0;
        }
        double mbPerSecond = Double.parseDouble(raw);
        if (Double.isNaN(mbPerSecond) || Double.isInfinite(mbPerSecond) || mbPerSecond < 0) {
            throw new Exception("下载限速不能小于 0");
        }
        if (mbPerSecond == 0) {
            return 0;
        }
        return Math.max(1, (long) (mbPerSecond * 1024 * 1024));
    }

    private String formatMB(long bytes) {
        if (bytes <= 0) {
            return "0";
        }
        double mb = bytes / 1024.0 / 1024.0;
        if (Math.abs(mb - Math.rint(mb)) < 0.05) {
            return String.format(java.util.Locale.US, "%.0f", mb);
        }
        return String.format(java.util.Locale.US, "%.1f", mb);
    }

    private void configureSystemBars() {
        Window window = getWindow();
        window.setStatusBarColor(Color.TRANSPARENT);
        window.setNavigationBarColor(Color.TRANSPARENT);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            int flags = View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                flags |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
            }
            window.getDecorView().setSystemUiVisibility(flags);
        }
    }

    private void applyContentInsets(ScrollView scroll) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) {
            return;
        }
        scroll.setOnApplyWindowInsetsListener((view, insets) -> {
            WindowInsets systemInsets = view.getRootWindowInsets();
            int left = systemInsets == null ? 0 : systemInsets.getSystemWindowInsetLeft();
            int top = systemInsets == null ? 0 : systemInsets.getSystemWindowInsetTop();
            int right = systemInsets == null ? 0 : systemInsets.getSystemWindowInsetRight();
            int bottom = systemInsets == null ? 0 : systemInsets.getSystemWindowInsetBottom();
            view.setPadding(left, top, right, bottom);
            return insets;
        });
        scroll.post(scroll::requestApplyInsets);
    }

    private void addTask() {
        if (engine == null) {
            setMessage("下载核心启动中");
            return;
        }
        String magnet = magnetInput.getText().toString().replaceAll("[ \\t\\r\\n]", "");
        if (magnet.isEmpty()) {
            setMessage("请先粘贴磁力链接");
            return;
        }
        runInBackground(() -> {
            try {
                String json = engine.addMagnet(magnet);
                runOnUiThread(() -> {
                    magnetInput.setText("");
                    setMessage("已添加任务");
                    pendingFilePageTaskId = taskIdFromJSON(json);
                    refreshTasks();
                });
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private String taskIdFromJSON(String json) {
        try {
            return new JSONObject(json == null ? "{}" : json).optString("id", "");
        } catch (Exception e) {
            return "";
        }
    }

    private void refreshTasks() {
        if (engine == null) {
            return;
        }
        runInBackground(() -> {
            try {
                String json = engine.listTasks();
                runOnUiThread(() -> renderTasks(json));
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private void renderTasks(String json) {
        try {
            JSONArray tasks = new JSONArray(json == null ? "[]" : json);
            updateFilePageNavigation(tasks);
            if (filePageTaskId != null) {
                renderFilePage();
                return;
            }
            if (taskList == null || rootView == null) {
                renderTaskPageShell();
            }
            taskList.removeAllViews();
            if (tasks.length() == 0) {
                TextView empty = text("暂无下载任务", 15, Color.rgb(101, 112, 131));
                empty.setGravity(Gravity.CENTER);
                empty.setPadding(0, dp(28), 0, 0);
                taskList.addView(empty);
                return;
            }
            for (int i = 0; i < tasks.length(); i++) {
                taskList.addView(taskCard(tasks.getJSONObject(i)));
            }
        } catch (Exception e) {
            if (taskList == null || rootView == null) {
                renderTaskPageShell();
            }
            TextView raw = text(json == null ? "" : json, 13, Color.rgb(49, 64, 90));
            raw.setPadding(0, dp(12), 0, 0);
            taskList.addView(raw);
        }
    }

    private void updateFilePageNavigation(JSONArray tasks) throws Exception {
        filePageTask = null;
        boolean pendingFound = false;
        boolean currentFound = false;
        Map<String, Boolean> currentTaskIds = new LinkedHashMap<>();
        for (int i = 0; i < tasks.length(); i++) {
            JSONObject task = tasks.getJSONObject(i);
            String id = task.optString("id");
            if (!id.isEmpty()) {
                currentTaskIds.put(id, true);
            }
            JSONArray files = task.optJSONArray("files");
            boolean hasFiles = files != null && files.length() > 0;
            if (hasFiles && !seenTaskFiles.containsKey(id)) {
                seenTaskFiles.put(id, true);
                if (id.equals(pendingFilePageTaskId)) {
                    filePageTaskId = id;
                    filePageTask = task;
                    pendingFilePageTaskId = null;
                    pendingFound = true;
                }
            }
            if (id.equals(pendingFilePageTaskId)) {
                pendingFound = true;
                if (hasFiles) {
                    filePageTaskId = id;
                    filePageTask = task;
                    pendingFilePageTaskId = null;
                }
            }
            if (id.equals(filePageTaskId)) {
                currentFound = true;
                if (hasFiles) {
                    filePageTask = task;
                }
            }
        }
        expandedFiles.keySet().retainAll(currentTaskIds.keySet());
        seenTaskFiles.keySet().retainAll(currentTaskIds.keySet());
        draftPriorities.keySet().retainAll(currentTaskIds.keySet());
        if (pendingFilePageTaskId != null && !pendingFound) {
            pendingFilePageTaskId = null;
        }
        if (filePageTaskId != null && (!currentFound || filePageTask == null)) {
            filePageTaskId = null;
            filePageTask = null;
        }
    }

    private void openFilePage(JSONObject task) {
        filePageTaskId = task.optString("id");
        filePageTask = task;
        renderFilePage();
    }

    private void closeFilePage() {
        filePageTaskId = null;
        filePageTask = null;
        renderTaskPageShell();
        refreshTasks();
    }

    private void renderFilePage() {
        if (rootView == null || filePageTask == null) {
            return;
        }
        rootView.removeAllViews();
        String id = filePageTask.optString("id");
        String status = filePageTask.optString("status");
        JSONArray files = filePageTask.optJSONArray("files");

        LinearLayout header = new LinearLayout(this);
        header.setOrientation(LinearLayout.HORIZONTAL);
        header.setGravity(Gravity.CENTER_VERTICAL);
        rootView.addView(header);

        Button back = button("返回");
        back.setOnClickListener(v -> closeFilePage());
        header.addView(back, new LinearLayout.LayoutParams(dp(84), dp(42)));

        TextView title = text(filePageTask.optString("name", "文件列表"), 20, Color.rgb(21, 25, 34));
        title.setSingleLine(false);
        header.addView(title, new LinearLayout.LayoutParams(0, -2, 1));

        message = new TextView(this);
        message.setTextColor(Color.rgb(101, 112, 131));
        message.setPadding(0, dp(8), 0, dp(8));
        rootView.addView(message);

        if (files == null || files.length() == 0) {
            TextView empty = text("正在获取文件列表", 15, Color.rgb(101, 112, 131));
            empty.setGravity(Gravity.CENTER);
            empty.setPadding(0, dp(28), 0, 0);
            rootView.addView(empty);
            return;
        }
        try {
            rootView.addView(fileSelectionView(id, status, files));
        } catch (Exception e) {
            setMessage(e.getMessage());
        }
    }

    private View taskCard(JSONObject task) throws Exception {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setPadding(dp(12), dp(12), dp(12), dp(12));
        card.setBackgroundColor(Color.WHITE);
        LinearLayout.LayoutParams cardParams = new LinearLayout.LayoutParams(-1, -2);
        cardParams.setMargins(0, dp(8), 0, dp(8));
        card.setLayoutParams(cardParams);

        String id = task.optString("id");
        String status = task.optString("status");
        boolean paused = task.optBoolean("paused");
        double progress = task.optDouble("progressPercent", 0);
        long completed = task.optLong("completedBytes", 0);
        long total = task.optLong("totalBytes", 0);
        double speed = task.optDouble("downloadSpeed", 0);
        JSONArray files = task.optJSONArray("files");
        boolean hasFiles = files != null && files.length() > 0;
        boolean filesExpanded = Boolean.TRUE.equals(expandedFiles.get(id));

        LinearLayout header = new LinearLayout(this);
        header.setOrientation(LinearLayout.HORIZONTAL);
        header.setGravity(Gravity.CENTER_VERTICAL);
        card.addView(header);

        TextView name = text(task.optString("name", "等待元数据"), 16, Color.rgb(21, 25, 34));
        name.setSingleLine(false);
        if (hasFiles) {
            name.setOnClickListener(v -> openFilePage(task));
        }
        header.addView(name, new LinearLayout.LayoutParams(0, -2, 1));

        if (hasFiles) {
            Button filesToggle = button(filesExpanded ? "收起" : "展开");
            filesToggle.setOnClickListener(v -> {
                if (Boolean.TRUE.equals(expandedFiles.get(id))) {
                    expandedFiles.remove(id);
                } else {
                    expandedFiles.put(id, true);
                }
                refreshTasks();
            });
            header.addView(filesToggle, new LinearLayout.LayoutParams(dp(78), dp(38)));
        }

        TextView meta = text(
                statusText(status) + " · " + fmtPercent(progress) + " · " + fmtBytes(completed) + " / " + fmtBytes(total),
                13,
                Color.rgb(101, 112, 131)
        );
        meta.setPadding(0, dp(4), 0, dp(6));
        card.addView(meta);

        ProgressBar bar = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        bar.setMax(1000);
        bar.setProgress((int) Math.max(0, Math.min(1000, progress * 10)));
        card.addView(bar, new LinearLayout.LayoutParams(-1, dp(8)));

        TextView network = text(
                fmtBytes((long) speed) + "/s · Peer " + task.optInt("activePeers") + "/" + task.optInt("peers") + " · 已知 " + task.optInt("knownPeers") + " · 做种 " + task.optInt("seeders"),
                13,
                Color.rgb(49, 64, 90)
        );
        network.setPadding(0, dp(8), 0, dp(8));
        card.addView(network);

        TextView discovery = text(
                "连接 " + task.optInt("pendingPeers") + "/" + task.optInt("halfOpenPeers") + " · 全局 " + task.optInt("clientHalfOpen") + " · 不可直连 " + task.optInt("undialablePeers"),
                12,
                Color.rgb(101, 112, 131)
        );
        discovery.setPadding(0, 0, 0, dp(6));
        card.addView(discovery);

        TextView discoverySources = text(
                "Tracker " + task.optInt("trackerCount") + " · DHT " + task.optInt("dhtServers"),
                12,
                Color.rgb(101, 112, 131)
        );
        discoverySources.setPadding(0, 0, 0, dp(6));
        card.addView(discoverySources);

        LinearLayout actions = new LinearLayout(this);
        actions.setOrientation(LinearLayout.HORIZONTAL);
        card.addView(actions);

        Button toggle = button(paused ? "继续" : "暂停");
        toggle.setOnClickListener(v -> runTaskAction(() -> {
            if (paused) {
                engine.resumeTask(id);
            } else {
                engine.pauseTask(id);
            }
        }));
        actions.addView(toggle, new LinearLayout.LayoutParams(0, dp(40), 1));

        Button refresh = button("刷新源");
        refresh.setOnClickListener(v -> runTaskAction(() -> engine.refreshDiscovery(id)));
        actions.addView(refresh, new LinearLayout.LayoutParams(0, dp(40), 1));

        if (hasFiles) {
            Button filesPage = button("文件");
            filesPage.setOnClickListener(v -> openFilePage(task));
            actions.addView(filesPage, new LinearLayout.LayoutParams(0, dp(40), 1));
        }

        Button remove = button("移除");
        remove.setOnClickListener(v -> runTaskAction(() -> engine.deleteTask(id, false)));
        actions.addView(remove, new LinearLayout.LayoutParams(0, dp(40), 1));

        if (hasFiles && filesExpanded) {
            card.addView(downloadingFilesView(id, files));
        }
        return card;
    }

    private View downloadingFilesView(String id, JSONArray files) throws Exception {
        LinearLayout panel = new LinearLayout(this);
        panel.setOrientation(LinearLayout.VERTICAL);
        panel.setPadding(0, dp(10), 0, 0);

        int downloading = 0;
        for (int i = 0; i < files.length(); i++) {
            if (isDownloadingFile(id, files.getJSONObject(i))) {
                downloading++;
            }
        }

        TextView title = text("正在下载的文件 " + downloading + "/" + files.length(), 14, Color.rgb(21, 25, 34));
        title.setPadding(0, dp(2), 0, dp(6));
        panel.addView(title);

        if (downloading == 0) {
            TextView empty = text("没有正在下载的文件", 13, Color.rgb(101, 112, 131));
            empty.setPadding(0, dp(4), 0, 0);
            panel.addView(empty);
            return panel;
        }

        for (int i = 0; i < files.length(); i++) {
            JSONObject file = files.getJSONObject(i);
            if (isDownloadingFile(id, file)) {
                panel.addView(fileDownloadRow(file));
            }
        }
        return panel;
    }

    private boolean isDownloadingFile(String id, JSONObject file) {
        return !"skip".equals(priorityFor(id, file));
    }

    private View fileDownloadRow(JSONObject file) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.VERTICAL);
        row.setPadding(0, dp(7), 0, dp(5));

        TextView name = text(file.optString("path"), 13, Color.rgb(21, 25, 34));
        name.setSingleLine(false);
        row.addView(name);

        TextView meta = text(
                fmtBytes(file.optLong("completedBytes", 0)) + " / " + fmtBytes(file.optLong("size", 0)) + " · " + fmtPercent(file.optDouble("progressPercent", 0)),
                12,
                Color.rgb(101, 112, 131)
        );
        meta.setPadding(0, dp(2), 0, dp(3));
        row.addView(meta);

        ProgressBar bar = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        bar.setMax(1000);
        bar.setProgress((int) Math.max(0, Math.min(1000, file.optDouble("progressPercent", 0) * 10)));
        row.addView(bar, new LinearLayout.LayoutParams(-1, dp(6)));
        return row;
    }

    private View fileSelectionView(String id, String status, JSONArray files) throws Exception {
        LinearLayout panel = new LinearLayout(this);
        panel.setOrientation(LinearLayout.VERTICAL);
        panel.setPadding(0, dp(10), 0, 0);

        int selected = 0;
        for (int i = 0; i < files.length(); i++) {
            JSONObject file = files.getJSONObject(i);
            if (!"skip".equals(priorityFor(id, file))) {
                selected++;
            }
        }

        TextView title = text("文件 " + selected + "/" + files.length(), 14, Color.rgb(21, 25, 34));
        title.setPadding(0, dp(2), 0, dp(6));
        panel.addView(title);

        if ("awaiting_selection".equals(status)) {
            TextView hint = text("请选择要下载的文件并保存", 13, Color.rgb(101, 112, 131));
            hint.setPadding(0, 0, 0, dp(6));
            panel.addView(hint);
        }

        LinearLayout batch = new LinearLayout(this);
        batch.setOrientation(LinearLayout.HORIZONTAL);
        panel.addView(batch);

        Button allNormal = button("全部普通");
        allNormal.setOnClickListener(v -> setAllFilePriority(id, files, "normal"));
        batch.addView(allNormal, new LinearLayout.LayoutParams(0, dp(38), 1));

        Button allHigh = button("全部高级");
        allHigh.setOnClickListener(v -> setAllFilePriority(id, files, "high"));
        batch.addView(allHigh, new LinearLayout.LayoutParams(0, dp(38), 1));

        Button allSkip = button("全部跳过");
        allSkip.setOnClickListener(v -> setAllFilePriority(id, files, "skip"));
        batch.addView(allSkip, new LinearLayout.LayoutParams(0, dp(38), 1));

        Button save = button("保存");
        save.setOnClickListener(v -> saveFilePriorities(id, files));
        batch.addView(save, new LinearLayout.LayoutParams(0, dp(38), 1));

        for (int i = 0; i < files.length(); i++) {
            JSONObject file = files.getJSONObject(i);
            panel.addView(filePriorityRow(id, file));
        }
        return panel;
    }

    private View filePriorityRow(String id, JSONObject file) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.VERTICAL);
        row.setPadding(0, dp(8), 0, dp(4));

        String path = file.optString("path");
        TextView name = text(path, 13, Color.rgb(21, 25, 34));
        name.setSingleLine(false);
        row.addView(name);

        TextView meta = text(
                fmtBytes(file.optLong("completedBytes", 0)) + " / " + fmtBytes(file.optLong("size", 0)) + " · " + fmtPercent(file.optDouble("progressPercent", 0)),
                12,
                Color.rgb(101, 112, 131)
        );
        row.addView(meta);

        LinearLayout actions = new LinearLayout(this);
        actions.setOrientation(LinearLayout.HORIZONTAL);
        row.addView(actions);

        String current = priorityFor(id, file);
        actions.addView(priorityButton(id, path, "high", "高级", current), new LinearLayout.LayoutParams(0, dp(36), 1));
        actions.addView(priorityButton(id, path, "normal", "普通", current), new LinearLayout.LayoutParams(0, dp(36), 1));
        actions.addView(priorityButton(id, path, "skip", "跳过", current), new LinearLayout.LayoutParams(0, dp(36), 1));
        return row;
    }

    private Button priorityButton(String id, String path, String priority, String label, String current) {
        Button button = button(label);
        button.setEnabled(!priority.equals(current));
        button.setOnClickListener(v -> {
            setDraftPriority(id, path, priority);
            refreshTasks();
        });
        return button;
    }

    private String priorityFor(String id, JSONObject file) {
        String path = file.optString("path");
        Map<String, String> draft = draftPriorities.get(id);
        if (draft != null && draft.containsKey(path)) {
            return draft.get(path);
        }
        String priority = file.optString("priority", "");
        if ("high".equals(priority) || "normal".equals(priority) || "skip".equals(priority)) {
            return priority;
        }
        return file.optBoolean("selected", true) ? "high" : "skip";
    }

    private void setDraftPriority(String id, String path, String priority) {
        Map<String, String> draft = draftPriorities.get(id);
        if (draft == null) {
            draft = new LinkedHashMap<>();
            draftPriorities.put(id, draft);
        }
        draft.put(path, priority);
    }

    private void setAllFilePriority(String id, JSONArray files, String priority) {
        try {
            for (int i = 0; i < files.length(); i++) {
                setDraftPriority(id, files.getJSONObject(i).optString("path"), priority);
            }
            refreshTasks();
        } catch (Exception e) {
            setMessage(e.getMessage());
        }
    }

    private void saveFilePriorities(String id, JSONArray files) {
        if (engine == null) {
            setMessage("下载核心启动中");
            return;
        }
        final String payload;
        try {
            JSONObject priorities = new JSONObject();
            for (int i = 0; i < files.length(); i++) {
                JSONObject file = files.getJSONObject(i);
                priorities.put(file.optString("path"), priorityFor(id, file));
            }
            payload = priorities.toString();
        } catch (Exception e) {
            setMessage(e.getMessage());
            return;
        }
        runInBackground(() -> {
            try {
                engine.updateFilePriorities(id, payload);
                runOnUiThread(() -> {
                    draftPriorities.remove(id);
                    setMessage("已保存文件优先级");
                    refreshTasks();
                });
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private void runTaskAction(TaskAction action) {
        if (engine == null) {
            setMessage("下载核心启动中");
            return;
        }
        runInBackground(() -> {
            try {
                action.run();
                runOnUiThread(this::refreshTasks);
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
    }

    private TextView text(String value, int size, int color) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(size);
        view.setTextColor(color);
        return view;
    }

    private String statusText(String status) {
        switch (status) {
            case "metadata":
                return "获取元数据";
            case "awaiting_selection":
                return "等待选文件";
            case "queued":
                return "排队中";
            case "paused":
                return "已暂停";
            case "downloading":
                return "下载中";
            case "completed":
                return "已完成";
            case "metadata_timeout":
                return "元数据超时";
            default:
                return status == null || status.isEmpty() ? "未知" : status;
        }
    }

    private String fmtPercent(double value) {
        return String.format(java.util.Locale.US, "%.1f%%", Math.max(0, Math.min(100, value)));
    }

    private String fmtBytes(long bytes) {
        if (bytes <= 0) {
            return "0 B";
        }
        String[] units = {"B", "KB", "MB", "GB", "TB"};
        double value = bytes;
        int index = 0;
        while (value >= 1024 && index < units.length - 1) {
            value /= 1024;
            index++;
        }
        return String.format(java.util.Locale.US, index == 0 ? "%.0f %s" : "%.2f %s", value, units[index]);
    }

    private Button button(String text) {
        Button button = new Button(this);
        button.setText(text);
        button.setAllCaps(false);
        return button;
    }

    private void setMessage(String text) {
        if (message != null) {
            message.setText(text == null ? "" : text);
        }
    }

    private void runInBackground(Runnable runnable) {
        new Thread(runnable).start();
    }

    private void requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 10);
        }
    }

    private int dp(int value) {
        return (int) (value * getResources().getDisplayMetrics().density + 0.5f);
    }

    private interface TaskAction {
        void run() throws Exception;
    }
}
