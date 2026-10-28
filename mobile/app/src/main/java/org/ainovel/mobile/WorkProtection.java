package org.ainovel.mobile;

/** Thread-safe power lease, independent of Activity, WebView and Android APIs. */
final class WorkProtection {
    interface Locks { void acquire(long timeoutMillis); void release(); }
    interface Clock { long elapsedMillis(); }
    private static final long LEASE_MILLIS = 120_000;
    private static final long RENEW_MILLIS = 30_000;
    private final Locks locks;
    private final Clock clock;
    private boolean stopped, active, held;
    private long revision, renewAt;

    WorkProtection(Locks locks, Clock clock) { this.locks = locks; this.clock = clock; }

    synchronized void workStarted() {
        if (stopped) return;
        revision++;
        update(true);
    }

    synchronized long revision() { return revision; }

    synchronized void refresh(boolean hasWork, long observedRevision) {
        if (stopped) return;
        // A start callback can arrive while the Go snapshot is being read.
        // Never release the newly acquired lease using that older idle result.
        if (!hasWork && observedRevision != revision) return;
        update(hasWork);
    }

    synchronized boolean active() { return active; }
    synchronized boolean protectedNow() { return held; }

    synchronized void stop() {
        stopped = true;
        revision++;
        active = false;
        release();
    }

    private void update(boolean hasWork) {
        active = hasWork;
        if (!hasWork) { release(); return; }
        long now = clock.elapsedMillis();
        if (held && now < renewAt) return;
        try {
            locks.acquire(LEASE_MILLIS);
            held = true;
            renewAt = now + RENEW_MILLIS;
        } catch (RuntimeException denied) {
            // Do not leak a partially acquired lease or throw through JNI if
            // device policy rejects protection.
            held = true;
            release();
        }
    }

    private void release() {
        if (!held) return;
        held = false;
        try { locks.release(); } catch (RuntimeException ignored) { }
    }
}
