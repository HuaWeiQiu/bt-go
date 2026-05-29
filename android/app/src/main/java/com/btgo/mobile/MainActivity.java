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
import android.view.Gravity;
import android.view.View;
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
    private EditText magnetInput;
    private TextView message;
    private LinearLayout taskList;
    private final Map<String, Map<String, String>> draftPriorities = new LinkedHashMap<>();

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
            engine = binder.getService().engine();
            refreshTasks();
            handler.post(refreshLoop);
        }

        @Override
        public void onServiceDisconnected(ComponentName name) {
            engine = null;
        }
    };

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        requestNotificationPermission();
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
        unbindService(serviceConnection);
        super.onDestroy();
    }

    private View buildView() {
        int pad = dp(14);
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(pad, pad, pad, pad);
        root.setBackgroundColor(Color.rgb(247, 249, 252));
        scroll.addView(root);

        TextView title = new TextView(this);
        title.setText("bt-go");
        title.setTextSize(26);
        title.setTextColor(Color.rgb(21, 25, 34));
        title.setGravity(Gravity.START);
        root.addView(title);

        magnetInput = new EditText(this);
        magnetInput.setHint("粘贴 magnet:?xt=urn:btih:...");
        magnetInput.setSingleLine(false);
        magnetInput.setMinLines(3);
        root.addView(magnetInput, new LinearLayout.LayoutParams(-1, -2));

        LinearLayout actions = new LinearLayout(this);
        actions.setOrientation(LinearLayout.HORIZONTAL);
        actions.setGravity(Gravity.CENTER_VERTICAL);
        root.addView(actions);

        Button add = button("添加下载");
        add.setOnClickListener(v -> addTask());
        actions.addView(add, new LinearLayout.LayoutParams(0, dp(44), 1));

        Button refresh = button("刷新");
        refresh.setOnClickListener(v -> refreshTasks());
        actions.addView(refresh, new LinearLayout.LayoutParams(0, dp(44), 1));

        message = new TextView(this);
        message.setTextColor(Color.rgb(101, 112, 131));
        message.setPadding(0, dp(8), 0, dp(8));
        root.addView(message);

        taskList = new LinearLayout(this);
        taskList.setOrientation(LinearLayout.VERTICAL);
        root.addView(taskList);
        return scroll;
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
                engine.addMagnet(magnet);
                runOnUiThread(() -> {
                    magnetInput.setText("");
                    setMessage("已添加任务");
                    refreshTasks();
                });
            } catch (Exception e) {
                runOnUiThread(() -> setMessage(e.getMessage()));
            }
        });
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
        taskList.removeAllViews();
        try {
            JSONArray tasks = new JSONArray(json == null ? "[]" : json);
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
            TextView raw = text(json == null ? "" : json, 13, Color.rgb(49, 64, 90));
            raw.setPadding(0, dp(12), 0, 0);
            taskList.addView(raw);
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

        TextView name = text(task.optString("name", "等待元数据"), 16, Color.rgb(21, 25, 34));
        name.setSingleLine(false);
        card.addView(name);

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
                fmtBytes((long) speed) + "/s · Peer " + task.optInt("activePeers") + "/" + task.optInt("peers") + " · 做种 " + task.optInt("seeders"),
                13,
                Color.rgb(49, 64, 90)
        );
        network.setPadding(0, dp(8), 0, dp(8));
        card.addView(network);

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

        Button remove = button("移除");
        remove.setOnClickListener(v -> runTaskAction(() -> engine.deleteTask(id, false)));
        actions.addView(remove, new LinearLayout.LayoutParams(0, dp(40), 1));

        JSONArray files = task.optJSONArray("files");
        if (files != null && files.length() > 0) {
            card.addView(fileSelectionView(id, status, files));
        }
        return card;
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
        return file.optBoolean("selected", true) ? "normal" : "skip";
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
        message.setText(text == null ? "" : text);
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
