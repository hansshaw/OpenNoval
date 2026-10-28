package org.ainovel.mobile;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.os.PowerManager;
import android.os.SystemClock;
import java.io.File;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import org.ainovel.mobile.mobilebridge.Mobilebridge;
import org.ainovel.mobile.mobilebridge.WorkObserver;

/** Owns the Go runtime and active request power protection independently of UI. */
public final class NovelService extends Service {
    static final String BACKGROUND_SETTINGS = "org.ainovel.mobile.BACKGROUND_SETTINGS";
    static volatile String launchUrl;
    static volatile String failure;
    static volatile boolean starting;
    private static final ExecutorService runtime = Executors.newSingleThreadExecutor();
    private final ScheduledExecutorService monitor = Executors.newSingleThreadScheduledExecutor();
    private final Handler main = new Handler(Looper.getMainLooper());
    private volatile boolean destroyed;
    private WorkProtection protection;
    private NotificationManager notifications;
    private PowerManager power;
    private String notificationState = "";

    @Override public void onCreate() {
        super.onCreate();
        starting = true;
        failure = null;
        power = getSystemService(PowerManager.class);
        PowerManager.WakeLock cpu = power.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "ainovel:ModelGeneration");
        cpu.setReferenceCounted(false);
        // Modern Wi-Fi low-latency locks only apply to a foreground, screen-on
        // app. Doze network access instead requires the user's battery exemption.
        protection = new WorkProtection(new WorkProtection.Locks() {
            @Override public void acquire(long timeout) { cpu.acquire(timeout); }
            @Override public void release() { if (cpu.isHeld()) cpu.release(); }
        }, SystemClock::elapsedRealtime);
        notifications = getSystemService(NotificationManager.class);
        notifications.createNotificationChannel(new NotificationChannel("writing", "本机创作服务", NotificationManager.IMPORTANCE_LOW));
        startForeground(1, notification(false));
        File data = new File(getFilesDir(), "studio");
        File cache = new File(getCacheDir(), "studio");
        runtime.execute(() -> {
            try {
                String url = Mobilebridge.startWithObserver(data.getAbsolutePath(), cache.getAbsolutePath(), new WorkObserver() {
                    @Override public void onWorkStarted() {
                        // Called before the Go handler starts network activity.
                        // Do not call back into Go from this JNI callback.
                        protection.workStarted();
                        main.post(NovelService.this::updateNotification);
                    }
                });
                main.post(() -> {
                    if (destroyed) return;
                    launchUrl = url;
                    monitor.scheduleWithFixedDelay(this::checkWork, 0, 500, TimeUnit.MILLISECONDS);
                    starting = false;
                });
            } catch (Exception error) {
                main.post(() -> failService("创作服务启动失败：" + error.getMessage()));
            }
        });
    }

    private void checkWork() {
        if (destroyed) return;
        try {
            long revision = protection.revision();
            boolean active = Mobilebridge.hasActiveWork();
            protection.refresh(active, revision);
            main.post(this::updateNotification);
        } catch (RuntimeException error) {
            // Scheduled executors silently stop polling after an exception.
            // Release protection and stop the runtime instead of leaking locks.
            main.post(() -> failService("创作服务状态异常，任务已停止。请重新打开应用。"));
        }
    }

    private void failService(String message) {
        if (destroyed) return;
        failure = message;
        stopRuntime();
        stopSelf();
    }

    private Notification notification(boolean active) {
        PendingIntent open = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        PendingIntent settings = PendingIntent.getActivity(this, 1, new Intent(this, MainActivity.class).setAction(BACKGROUND_SETTINGS), PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        String text = active ? "正在创作 · 可切换到其他应用" : "创作工作台已就绪 · 点击返回作品";
        String detail = active ? "任务由后台服务继续执行。从最近任务列表划掉应用会停止创作。" : "没有运行中的任务，不占用创作电源锁。";
        if (active && !power.isIgnoringBatteryOptimizations(getPackageName())) detail += " 为避免锁屏后网络被暂停，请在后台运行设置中允许后台运行。";
        return new Notification.Builder(this, "writing")
            .setSmallIcon(R.drawable.ic_novel).setContentTitle("AI 小说工作台")
            .setContentText(text).setStyle(new Notification.BigTextStyle().bigText(detail))
            .setContentIntent(open).setOngoing(true).setOnlyAlertOnce(true)
            .addAction(new Notification.Action.Builder(null, "后台运行设置", settings).build()).build();
    }

    private void updateNotification() {
        if (destroyed) return;
        boolean active = protection.active();
        String state = active + ":" + power.isIgnoringBatteryOptimizations(getPackageName());
        if (state.equals(notificationState)) return;
        notificationState = state;
        notifications.notify(1, notification(active));
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        // User clearing the task must never silently restart paid generation.
        return START_NOT_STICKY;
    }

    @Override public void onTaskRemoved(Intent rootIntent) {
        stopRuntime();
        stopSelf();
        super.onTaskRemoved(rootIntent);
    }

    private void stopRuntime() {
        if (destroyed) return;
        destroyed = true;
        starting = false;
        launchUrl = null;
        if (protection != null) protection.stop();
        monitor.shutdownNow();
        main.removeCallbacksAndMessages(null);
        runtime.execute(Mobilebridge::stop);
    }

    @Override public void onDestroy() {
        stopRuntime();
        super.onDestroy();
    }

    // Respect Android 15's dataSync time budget; never disguise the service
    // type or auto-restart to bypass the system's stop decision.
    @Override public void onTimeout(int startId, int fgsType) {
        failure = "系统的后台运行时间已到，任务已停止。重新打开应用后可继续创作。";
        stopRuntime();
        stopSelf();
    }

    @Override public IBinder onBind(Intent intent) { return null; }
}
