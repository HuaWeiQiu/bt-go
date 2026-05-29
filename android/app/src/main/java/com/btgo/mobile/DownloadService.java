package com.btgo.mobile;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.os.Binder;
import android.os.Build;
import android.os.IBinder;

import btgo.Btgo;
import btgo.Engine;

public class DownloadService extends Service {
    private static final String CHANNEL_ID = "bt_go_download";
    private static final int NOTIFICATION_ID = 1001;

    private final LocalBinder binder = new LocalBinder();
    private Engine engine;

    public final class LocalBinder extends Binder {
        DownloadService getService() {
            return DownloadService.this;
        }
    }

    @Override
    public void onCreate() {
        super.onCreate();
        createNotificationChannel();
        startForeground(NOTIFICATION_ID, notification("bt-go 正在运行"));
        try {
            engine = Btgo.newEngine(getExternalFilesDir("downloads").getAbsolutePath(), 0);
        } catch (Exception e) {
            stopForeground(true);
            stopSelf();
            throw new IllegalStateException("启动下载核心失败", e);
        }
    }

    @Override
    public IBinder onBind(Intent intent) {
        return binder;
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        return START_STICKY;
    }

    @Override
    public void onDestroy() {
        if (engine != null) {
            engine.close();
            engine = null;
        }
        super.onDestroy();
    }

    Engine engine() {
        return engine;
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
