package com.btgo.mobile;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.ServiceInfo;
import android.os.Binder;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import android.util.Log;
import android.net.wifi.WifiManager;

import java.io.File;

import btgo.Btgo;
import btgo.Engine;

public class DownloadService extends Service {
    private static final String TAG = "DownloadService";
    private static final String CHANNEL_ID = "bt_go_download";
    private static final int NOTIFICATION_ID = 1001;
    private static final String PREFS_NAME = "bt_go_settings";
    private static final String KEY_DOWNLOAD_DIR = "download_dir";
    private static final int BT_LISTEN_PORT = 42069;

    private final LocalBinder binder = new LocalBinder();
    private Engine engine;
    private String startupError;
    private File currentDownloadDir;
    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;

    public final class LocalBinder extends Binder {
        DownloadService getService() {
            return DownloadService.this;
        }
    }

    @Override
    public void onCreate() {
        super.onCreate();
        createNotificationChannel();
        startDownloadForeground();
        acquireRuntimeLocks();
        try {
            startEngine(configuredDownloadDir());
        } catch (Throwable e) {
            failStartup("启动下载核心失败: " + e.getMessage(), e);
        }
    }

    @Override
    public void onTimeout(int startId) {
        stopAfterTimeout(startId);
    }

    @Override
    public void onTimeout(int startId, int fgsType) {
        stopAfterTimeout(startId);
    }

    @Override
    public IBinder onBind(Intent intent) {
        return binder;
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        return engine == null ? START_NOT_STICKY : START_STICKY;
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        flushState();
        Intent restart = new Intent(getApplicationContext(), DownloadService.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(restart);
        } else {
            startService(restart);
        }
        super.onTaskRemoved(rootIntent);
    }

    @Override
    public synchronized void onDestroy() {
        flushState();
        if (engine != null) {
            engine.close();
            engine = null;
        }
        releaseRuntimeLocks();
        super.onDestroy();
    }

    synchronized Engine engine() {
        return engine;
    }

    synchronized String startupError() {
        return startupError;
    }

    synchronized String downloadDirPath() {
        File dir = currentDownloadDir;
        if (dir == null) {
            try {
                dir = configuredDownloadDir();
            } catch (Exception ignored) {
            }
        }
        return dir == null ? "" : dir.getAbsolutePath();
    }

    synchronized String setDownloadDir(String path) throws Exception {
        if (path == null || path.trim().isEmpty()) {
            return resetDownloadDir();
        }
        File dir = resolveDownloadDir(path);
        restartEngine(dir);
        getPreferences().edit().putString(KEY_DOWNLOAD_DIR, dir.getAbsolutePath()).apply();
        return dir.getAbsolutePath();
    }

    synchronized String resetDownloadDir() throws Exception {
        File dir = defaultDownloadDir();
        restartEngine(dir);
        getPreferences().edit().remove(KEY_DOWNLOAD_DIR).apply();
        return dir.getAbsolutePath();
    }

    private void restartEngine(File dir) throws Exception {
        if (engine != null) {
            engine.close();
            engine = null;
        }
        startEngine(dir);
    }

    private void startEngine(File dir) throws Exception {
        ensureDownloadDir(dir);
        startupError = null;
        currentDownloadDir = dir;
        try {
            engine = Btgo.newEngine(dir.getAbsolutePath(), BT_LISTEN_PORT);
        } catch (Exception e) {
            Log.w(TAG, "固定监听端口不可用，改用随机端口: " + BT_LISTEN_PORT, e);
            engine = Btgo.newEngine(dir.getAbsolutePath(), 0);
        }
    }

    private File configuredDownloadDir() throws Exception {
        String path = getPreferences().getString(KEY_DOWNLOAD_DIR, "");
        if (path == null || path.trim().isEmpty()) {
            return defaultDownloadDir();
        }
        return resolveDownloadDir(path);
    }

    private File defaultDownloadDir() throws Exception {
        File dir = getExternalFilesDir("downloads");
        if (dir == null) {
            dir = new File(getFilesDir(), "downloads");
        }
        return dir.getCanonicalFile();
    }

    private File resolveDownloadDir(String raw) throws Exception {
        String path = raw == null ? "" : raw.trim();
        if (path.isEmpty()) {
            return defaultDownloadDir();
        }
        File dir = new File(path);
        if (!dir.isAbsolute()) {
            File base = getExternalFilesDir(null);
            if (base == null) {
                base = getFilesDir();
            }
            dir = new File(base, path);
        }
        return dir.getCanonicalFile();
    }

    private void ensureDownloadDir(File dir) throws Exception {
        if (dir == null) {
            throw new IllegalStateException("下载目录为空");
        }
        if (!dir.exists() && !dir.mkdirs()) {
            throw new IllegalStateException("无法创建下载目录: " + dir.getAbsolutePath());
        }
        if (!dir.isDirectory()) {
            throw new IllegalStateException("下载路径不是目录: " + dir.getAbsolutePath());
        }
        File probe = File.createTempFile(".bt-go-write-test", ".tmp", dir);
        if (!probe.delete()) {
            Log.w(TAG, "无法删除目录写入测试文件: " + probe.getAbsolutePath());
        }
    }

    private SharedPreferences getPreferences() {
        return getSharedPreferences(PREFS_NAME, MODE_PRIVATE);
    }

    private void failStartup(String message, Throwable cause) {
        startupError = message == null ? "启动下载核心失败" : message;
        if (cause != null) {
            Log.e(TAG, startupError, cause);
        } else {
            Log.e(TAG, startupError);
        }
        stopForeground(STOP_FOREGROUND_REMOVE);
        releaseRuntimeLocks();
        stopSelf();
    }

    private void stopAfterTimeout(int startId) {
        flushState();
        if (engine != null) {
            engine.close();
            engine = null;
        }
        releaseRuntimeLocks();
        stopForeground(STOP_FOREGROUND_REMOVE);
        stopSelf(startId);
    }

    private synchronized void flushState() {
        if (engine == null) {
            return;
        }
        try {
            engine.flushState();
        } catch (Throwable e) {
            Log.w(TAG, "保存续接状态失败", e);
        }
    }

    private void acquireRuntimeLocks() {
        try {
            PowerManager powerManager = (PowerManager) getSystemService(Context.POWER_SERVICE);
            if (powerManager != null) {
                wakeLock = powerManager.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "btgo:download");
                wakeLock.setReferenceCounted(false);
                wakeLock.acquire();
            }
        } catch (Throwable e) {
            Log.w(TAG, "无法获取下载唤醒锁", e);
        }
        try {
            WifiManager wifiManager = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
            if (wifiManager != null) {
                int lockMode = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q
                        ? WifiManager.WIFI_MODE_FULL_LOW_LATENCY
                        : WifiManager.WIFI_MODE_FULL_HIGH_PERF;
                wifiLock = wifiManager.createWifiLock(lockMode, "btgo:download");
                wifiLock.setReferenceCounted(false);
                wifiLock.acquire();
            }
        } catch (Throwable e) {
            Log.w(TAG, "无法获取 Wi-Fi 下载锁", e);
        }
    }

    private void releaseRuntimeLocks() {
        try {
            if (wifiLock != null && wifiLock.isHeld()) {
                wifiLock.release();
            }
        } catch (Throwable e) {
            Log.w(TAG, "释放 Wi-Fi 下载锁失败", e);
        } finally {
            wifiLock = null;
        }
        try {
            if (wakeLock != null && wakeLock.isHeld()) {
                wakeLock.release();
            }
        } catch (Throwable e) {
            Log.w(TAG, "释放下载唤醒锁失败", e);
        } finally {
            wakeLock = null;
        }
    }

    private void startDownloadForeground() {
        Notification foregroundNotification = notification("bt-go 正在运行");
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                    NOTIFICATION_ID,
                    foregroundNotification,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
            );
        } else {
            startForeground(NOTIFICATION_ID, foregroundNotification);
        }
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return;
        }
        NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID,
                "bt-go 下载服务",
                NotificationManager.IMPORTANCE_LOW
        );
        NotificationManager manager = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        manager.createNotificationChannel(channel);
    }

    private Notification notification(String text) {
        Notification.Builder builder = Build.VERSION.SDK_INT >= Build.VERSION_CODES.O
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        return builder
                .setContentTitle("bt-go")
                .setContentText(text)
                .setSmallIcon(com.btgo.mobile.R.drawable.ic_notification)
                .setOngoing(true)
                .build();
    }
}
