package org.ainovel.mobile;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.app.NotificationManager;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.PowerManager;
import android.provider.DocumentsContract;
import android.provider.Settings;
import android.util.Base64;
import android.view.View;
import android.view.WindowInsets;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.JsResult;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.LinearLayout;
import android.widget.TextView;
import java.io.ByteArrayInputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.OutputStream;
import java.util.Collections;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.json.JSONArray;
import org.json.JSONObject;

public final class MainActivity extends Activity {
    private static final int PICK_INPUT = 10, SAVE_OUTPUT = 11;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final ExecutorService files = Executors.newSingleThreadExecutor();
    private WebView web;
    private TextView status;
    private LinearLayout root;
    private Uri origin;
    private String sessionUrl;
    private ValueCallback<Uri[]> chooser;
    private View fullscreenView;
    private WebChromeClient.CustomViewCallback fullscreenCallback;
    private final NativeBridge bridge = new NativeBridge();
    private long waitingSince;
    private boolean loaded;

    @Override public void onCreate(Bundle saved) {
        super.onCreate(saved);
        root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundColor(Color.rgb(9, 16, 24));
        if (Build.VERSION.SDK_INT >= 30) {
            getWindow().setDecorFitsSystemWindows(false);
            root.setOnApplyWindowInsetsListener((view, insets) -> {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.displayCutout());
                android.graphics.Insets keyboard = insets.getInsets(WindowInsets.Type.ime());
                view.setPadding(bars.left, bars.top, bars.right, Math.max(bars.bottom, keyboard.bottom));
                return WindowInsets.CONSUMED;
            });
        }
        status = new TextView(this);
        status.setText("正在打开手机创作工作台…");
        status.setTextColor(Color.WHITE);
        status.setPadding(24, 48, 24, 24);
        root.addView(status);
        web = new WebView(this);
        web.setBackgroundColor(Color.rgb(9, 16, 24));
        root.addView(web, new LinearLayout.LayoutParams(-1, 0, 1));
        setContentView(root);
        configureWebView();
        waitingSince = System.currentTimeMillis();
        startForegroundService(new Intent(this, NovelService.class));
        main.post(awaitBackend);
        SharedPreferences preferences = getSharedPreferences("native-preferences", MODE_PRIVATE);
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
                && !preferences.getBoolean("notification-requested", false)
                && !NovelService.BACKGROUND_SETTINGS.equals(getIntent().getAction())) {
            preferences.edit().putBoolean("notification-requested", true).apply();
            requestPermissions(new String[] { Manifest.permission.POST_NOTIFICATIONS }, 12);
        }
    }

    private final Runnable awaitBackend = new Runnable() {
        @Override public void run() {
            if (isFinishing() || isDestroyed()) return;
            String url = NovelService.launchUrl;
            if (url != null) {
                sessionUrl = url;
                origin = Uri.parse(url);
                web.loadUrl(url);
                loaded = true;
            } else if (NovelService.failure != null || System.currentTimeMillis() - waitingSince > 60000) {
                status.setText((NovelService.failure != null ? NovelService.failure : "启动超时") + "\n请退出后重新打开应用。");
            } else main.postDelayed(this, 200);
        }
    };

    private boolean trusted(Uri uri) {
        return origin != null && sessionUrl != null && sessionUrl.equals(NovelService.launchUrl)
            && "http".equals(uri.getScheme()) && "127.0.0.1".equals(uri.getHost())
            && uri.getPort() == origin.getPort() && uri.getUserInfo() == null;
    }

    private void configureWebView() {
        WebSettings settings = web.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(true); // Only system-selected input documents.
        settings.setAllowFileAccessFromFileURLs(false);
        settings.setAllowUniversalAccessFromFileURLs(false);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        settings.setJavaScriptCanOpenWindowsAutomatically(false);
        settings.setMediaPlaybackRequiresUserGesture(true);
        CookieManager.getInstance().setAcceptCookie(true);
        CookieManager.getInstance().setAcceptThirdPartyCookies(web, false);
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG);
        web.addJavascriptInterface(bridge, "ainovelAndroid");
        web.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                if (trusted(request.getUrl())) return false;
                Uri uri = request.getUrl();
                if (request.isForMainFrame() && request.hasGesture() && ("https".equals(uri.getScheme()) || "http".equals(uri.getScheme()))) {
                    try { startActivity(new Intent(Intent.ACTION_VIEW, uri)); }
                    catch (android.content.ActivityNotFoundException ignored) { showMessage("手机没有可打开此链接的浏览器。"); }
                }
                return true;
            }
            @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
                if (trusted(request.getUrl())) return null;
                return new WebResourceResponse("text/plain", "UTF-8", 403, "Forbidden", Collections.emptyMap(), new ByteArrayInputStream(new byte[0]));
            }
            @Override public void onPageFinished(WebView view, String url) {
                if (trusted(Uri.parse(url))) { status.setVisibility(View.GONE); CookieManager.getInstance().flush(); }
            }
            @Override public void onReceivedError(WebView view, WebResourceRequest request, android.webkit.WebResourceError error) {
                if (request.isForMainFrame()) {
                    status.setVisibility(View.VISIBLE);
                    status.setText("工作台连接中断，请退出后重新打开。已保存的作品仍在本机。");
                }
            }
        });
        web.setWebChromeClient(new WebChromeClient() {
            @Override public void onShowCustomView(View view, CustomViewCallback callback) {
                if (fullscreenView != null) { callback.onCustomViewHidden(); return; }
                fullscreenView = view;
                fullscreenCallback = callback;
                web.setVisibility(View.GONE);
                root.addView(view, new LinearLayout.LayoutParams(-1, 0, 1));
            }
            @Override public void onHideCustomView() { leaveFullscreen(); }
            @Override public boolean onJsAlert(WebView view, String url, String message, JsResult result) {
                if (!trusted(Uri.parse(url))) { result.cancel(); return true; }
                new AlertDialog.Builder(MainActivity.this).setMessage(message).setPositiveButton("确定", (d, w) -> result.confirm())
                    .setOnCancelListener(d -> result.cancel()).show();
                return true;
            }
            @Override public boolean onJsConfirm(WebView view, String url, String message, JsResult result) {
                if (!trusted(Uri.parse(url))) { result.cancel(); return true; }
                new AlertDialog.Builder(MainActivity.this).setMessage(message).setPositiveButton("确定", (d, w) -> result.confirm())
                    .setNegativeButton("取消", (d, w) -> result.cancel()).setOnCancelListener(d -> result.cancel()).show();
                return true;
            }
            @Override public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback, FileChooserParams params) {
                if (chooser != null) chooser.onReceiveValue(null);
                chooser = callback;
                Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*");
                intent.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, params.getMode() == FileChooserParams.MODE_OPEN_MULTIPLE);
                try { startActivityForResult(intent, PICK_INPUT); }
                catch (android.content.ActivityNotFoundException error) { chooser.onReceiveValue(null); chooser = null; showMessage("找不到系统文件选择器。"); }
                return true;
            }
        });
    }

    @Override protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == PICK_INPUT && chooser != null) {
            chooser.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(resultCode, data));
            chooser = null;
        } else if (requestCode == SAVE_OUTPUT) {
            if (!bridge.hasExport()) {
                showMessage("应用已被系统重新启动，导出会话已结束。请回到作品重新导出。");
                return;
            }
            if (resultCode != RESULT_OK || data == null || data.getData() == null) { bridge.completeExport(false, null); return; }
            Uri destination = data.getData();
            files.execute(() -> {
                String error = null;
                try (FileInputStream input = new FileInputStream(bridge.exportFile()); OutputStream output = getContentResolver().openOutputStream(destination, "wt")) {
                    if (output == null) throw new java.io.IOException("无法写入所选位置");
                    byte[] buffer = new byte[64 * 1024];
                    for (int count; (count = input.read(buffer)) != -1;) output.write(buffer, 0, count);
                } catch (Exception problem) {
                    error = "导出失败：" + problem.getMessage();
                    try { DocumentsContract.deleteDocument(getContentResolver(), destination); } catch (Exception ignored) { }
                }
                bridge.completeExport(error == null, error);
            });
        }
    }

    @Override public void onBackPressed() {
        if (fullscreenView != null) { leaveFullscreen(); return; }
        if (!loaded) { exit(); return; }
        web.evaluateJavascript("Boolean(window.ainovelMobile && window.ainovelMobile.handleBack())", handled -> {
            if (!"true".equals(handled)) moveTaskToBack(true);
        });
    }

    private void exit() { stopService(new Intent(this, NovelService.class)); finish(); }
    private void leaveFullscreen() {
        if (fullscreenView == null) return;
        root.removeView(fullscreenView);
        fullscreenView = null;
        WebChromeClient.CustomViewCallback callback = fullscreenCallback;
        fullscreenCallback = null;
        web.setVisibility(View.VISIBLE);
        if (callback != null) callback.onCustomViewHidden();
    }
    private void showMessage(String message) { runOnUiThread(() -> new AlertDialog.Builder(this).setMessage(message).setPositiveButton("确定", null).show()); }

    private void showBackgroundSettings() {
        if (isFinishing() || isDestroyed()) return;
        PowerManager power = getSystemService(PowerManager.class);
        boolean allowed = power.isIgnoringBatteryOptimizations(getPackageName());
        boolean notifications = getSystemService(NotificationManager.class).areNotificationsEnabled();
        String message = "切换应用或锁屏后，创作任务由后台服务继续。从最近任务列表划掉应用会停止任务，已保存的作品会保留。\n\n"
            + "电池后台运行：" + (allowed ? "已允许" : "尚未允许，锁屏后网络可能被系统暂停")
            + "\n任务通知：" + (notifications ? "已开启" : "未开启")
            + "\n\n部分手机还需在应用电池设置中选择“不限制”或允许后台活动。系统强制停止和长时间后台运行限额仍会停止任务。";
        new AlertDialog.Builder(this).setTitle("后台运行设置").setMessage(message)
            .setPositiveButton(allowed ? "应用系统设置" : "允许后台运行", (d, w) -> {
                Intent intent = allowed
                    ? new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:" + getPackageName()))
                    : new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:" + getPackageName()));
                openSystemSettings(intent);
            })
            .setNeutralButton("通知设置", (d, w) -> openSystemSettings(new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(Settings.EXTRA_APP_PACKAGE, getPackageName())))
            .setNegativeButton("关闭", null).show();
    }

    private void openSystemSettings(Intent intent) {
        try { startActivity(intent); }
        catch (android.content.ActivityNotFoundException | SecurityException error) {
            try { startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:" + getPackageName()))); }
            catch (android.content.ActivityNotFoundException | SecurityException unavailable) { showMessage("请在手机系统设置中打开本应用，允许后台运行和任务通知。"); }
        }
    }

    private void consumeBackgroundSettingsIntent() {
        if (NovelService.BACKGROUND_SETTINGS.equals(getIntent().getAction())) {
            getIntent().setAction(null);
            main.post(this::showBackgroundSettings);
        }
    }

    @Override protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        consumeBackgroundSettingsIntent();
    }

    @Override protected void onResume() {
        super.onResume();
        if (web != null) web.onResume();
        consumeBackgroundSettingsIntent();
        if (loaded && NovelService.launchUrl == null && !NovelService.starting) {
            status.setVisibility(View.VISIBLE);
            status.setText("后台服务已停止，请退出后重新打开应用。已保存的作品仍在本机。");
        }
    }
    @Override protected void onPause() { if (web != null) web.onPause(); super.onPause(); }
    @Override protected void onDestroy() {
        main.removeCallbacks(awaitBackend);
        leaveFullscreen();
        if (chooser != null) chooser.onReceiveValue(null);
        if (web != null) { web.removeJavascriptInterface("ainovelAndroid"); web.destroy(); }
        bridge.close();
        files.shutdown();
        super.onDestroy();
    }

    public final class NativeBridge {
        @JavascriptInterface public void openBackgroundSettings() { runOnUiThread(MainActivity.this::showBackgroundSettings); }
        private String exportId;
        private File pending;
        private FileOutputStream stream;
        private long bytes;
        private boolean committed;
        private SharedPreferences storage() { return getSharedPreferences("web-storage", MODE_PRIVATE); }
        @JavascriptInterface public String storageGet(String key) { return storage().getString(key, null); }
        @JavascriptInterface public String storageKeys() { return new JSONArray(storage().getAll().keySet()).toString(); }
        @JavascriptInterface public String storageSet(String key, String value) {
            if (key.length() > 2048 || value.length() > 16 * 1024 * 1024) return "本地草稿超过存储上限";
            return storage().edit().putString(key, value).commit() ? "" : "本地草稿保存失败";
        }
        @JavascriptInterface public void storageRemove(String key) { storage().edit().remove(key).commit(); }
        @JavascriptInterface public void storageClear() { storage().edit().clear().commit(); }

        @JavascriptInterface public synchronized String beginExport(String id) {
            if (exportId != null) return "请先完成当前导出";
            try {
                pending = File.createTempFile("novel-export-", ".tmp", getCacheDir());
                stream = new FileOutputStream(pending);
                exportId = id; bytes = 0; committed = false;
                return "";
            } catch (Exception error) { close(); return "无法准备导出文件"; }
        }
        @JavascriptInterface public synchronized String appendExport(String id, String base64) {
            if (!id.equals(exportId) || stream == null || committed) return "导出会话已结束";
            try {
                if (base64.length() > 96 * 1024) throw new java.io.IOException();
                byte[] part = Base64.decode(base64, Base64.NO_WRAP);
                bytes += part.length;
                if (bytes > 128L * 1024 * 1024) throw new java.io.IOException();
                stream.write(part);
                return "";
            } catch (Exception error) { close(); return "导出文件过大或存储空间不足"; }
        }
        @JavascriptInterface public synchronized String finishExport(String id, String filename, String format) {
            if (!id.equals(exportId) || stream == null || committed) return "导出会话已结束";
            String mime = switch (format) {
                case "txt" -> "text/plain"; case "epub" -> "application/epub+zip"; case "zip" -> "application/zip";
                case "mp3" -> "audio/mpeg"; case "mp4" -> "video/mp4"; case "png" -> "image/png";
                case "jpg", "jpeg" -> "image/jpeg"; case "webp" -> "image/webp"; case "gif" -> "image/gif";
                default -> null;
            };
            if (mime == null) { close(); return "不支持的导出格式"; }
            try { stream.close(); stream = null; committed = true; }
            catch (Exception error) { close(); return "无法写入导出文件"; }
            String safeName = filename.replace('\\', '/');
            safeName = safeName.substring(safeName.lastIndexOf('/') + 1).replaceAll("[\\p{Cntrl}]", "");
            if (safeName.length() > 160 || !safeName.toLowerCase(java.util.Locale.ROOT).endsWith("." + format)) safeName = "novel." + format;
            final String title = safeName;
            main.post(() -> {
                try {
                    startActivityForResult(new Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE)
                        .setType(mime).putExtra(Intent.EXTRA_TITLE, title), SAVE_OUTPUT);
                } catch (Exception error) { completeExport(false, "无法打开系统保存对话框"); }
            });
            return "";
        }
        @JavascriptInterface public synchronized void cancelExport(String id) { if (id.equals(exportId) && !committed) close(); }
        synchronized File exportFile() throws java.io.IOException {
            if (pending == null || !committed) throw new java.io.IOException("导出会话不存在");
            return pending;
        }
        synchronized boolean hasExport() { return pending != null && committed; }
        synchronized void completeExport(boolean saved, String error) {
            if (exportId == null) return;
            String script = "window.ainovelAndroidExportComplete(" + JSONObject.quote(exportId) + "," + saved + "," + (error == null ? "null" : JSONObject.quote(error)) + ")";
            close();
            main.post(() -> { if (!isDestroyed()) web.evaluateJavascript(script, null); });
        }
        synchronized void close() {
            try { if (stream != null) stream.close(); } catch (Exception ignored) { }
            if (pending != null) pending.delete();
            stream = null; pending = null; exportId = null; committed = false;
        }
    }
}
