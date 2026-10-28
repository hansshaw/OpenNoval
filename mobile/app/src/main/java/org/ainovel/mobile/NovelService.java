package org.ainovel.mobile;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.os.IBinder;
import java.io.File;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.ainovel.mobile.mobilebridge.Mobilebridge;

/** Owns the in-process Go runtime independently of Activity recreation. */
public final class NovelService extends Service {
    static volatile String launchUrl;
    static volatile String failure;
    static volatile boolean starting;
    private static final ExecutorService runtime = Executors.newSingleThreadExecutor();
    private volatile boolean destroyed;

    @Override public void onCreate() {
        super.onCreate();
        starting = true;
        failure = null;
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel("writing", "本机创作服务", NotificationManager.IMPORTANCE_LOW));
        PendingIntent open = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        startForeground(1, new Notification.Builder(this, "writing")
            .setSmallIcon(R.drawable.ic_novel).setContentTitle("AI 小说工作台")
            .setContentText("本机创作服务运行中 · 点击返回作品")
            .setContentIntent(open).setOngoing(true).build());
        File data = new File(getFilesDir(), "studio");
        File cache = new File(getCacheDir(), "studio");
        runtime.execute(() -> {
            try {
                String url = Mobilebridge.start(data.getAbsolutePath(), cache.getAbsolutePath());
                if (!destroyed) launchUrl = url;
            } catch (Exception error) {
                failure = "创作服务启动失败：" + error.getMessage();
                stopSelf();
            } finally { starting = false; }
        });
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        // An explicit launch restores state; never restart an AI request silently.
        return START_NOT_STICKY;
    }

    @Override public void onDestroy() {
        destroyed = true;
        launchUrl = null;
        runtime.execute(Mobilebridge::stop);
        super.onDestroy();
    }

    // Android 15 limits dataSync foreground-service time in the background.
    @Override public void onTimeout(int startId, int fgsType) {
        failure = "后台运行时间已到，请重新打开应用继续创作。";
        stopSelf();
    }

    @Override public IBinder onBind(Intent intent) { return null; }
}
