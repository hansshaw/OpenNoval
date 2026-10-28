package org.ainovel.mobile;

/** Plain JVM lifecycle tests; no emulator or Android mocks are required. */
public final class WorkProtectionTest {
    private static final class FakeLocks implements WorkProtection.Locks {
        int acquired, released;
        long timeout;
        boolean held, denied;
        @Override public void acquire(long timeoutMillis) {
            acquired++;
            held = true;
            timeout = timeoutMillis;
            if (denied) throw new SecurityException("device policy");
        }
        @Override public void release() { released++; held = false; }
    }
    private static final class FakeClock implements WorkProtection.Clock {
        long now;
        @Override public long elapsedMillis() { return now; }
    }

    public static void main(String[] args) {
        immediateStartAndIdleRelease();
        renewOnlyWhileActive();
        staleIdleSnapshotCannotReleaseNewWork();
        stopRejectsLateCallbacksAndPolls();
        deniedPartialAcquireIsReleasedAndCanRecover();
        System.out.println("WorkProtection: 5 lifecycle tests passed");
    }

    private static void immediateStartAndIdleRelease() {
        FakeLocks locks = new FakeLocks();
        WorkProtection policy = new WorkProtection(locks, new FakeClock());
        policy.refresh(false, policy.revision());
        check(locks.acquired == 0 && !policy.active(), "idle service must not hold power locks");
        policy.workStarted();
        check(locks.held && policy.active(), "start callback must protect before the first polling tick");
        check(locks.timeout > 0, "CPU protection must have a bounded timeout");
        policy.refresh(true, policy.revision());
        check(locks.acquired == 1, "same lease should not be reacquired every tick");
        policy.refresh(false, policy.revision());
        check(!locks.held && locks.released == 1 && !policy.active(), "completed work must release immediately");
    }

    private static void renewOnlyWhileActive() {
        FakeLocks locks = new FakeLocks();
        FakeClock clock = new FakeClock();
        WorkProtection policy = new WorkProtection(locks, clock);
        policy.workStarted();
        clock.now = 29_999;
        policy.refresh(true, policy.revision());
        check(locks.acquired == 1, "lease renewed too early");
        clock.now = 30_000;
        policy.refresh(true, policy.revision());
        check(locks.acquired == 2 && clock.now < locks.timeout, "long request must renew before CPU timeout");
        policy.refresh(false, policy.revision());
        clock.now = 1_000_000;
        policy.refresh(false, policy.revision());
        check(locks.acquired == 2 && locks.released == 1, "idle ticks must neither renew nor double-release");
    }

    private static void staleIdleSnapshotCannotReleaseNewWork() {
        FakeLocks locks = new FakeLocks();
        WorkProtection policy = new WorkProtection(locks, new FakeClock());
        long beforeGoSnapshot = policy.revision();
        policy.workStarted(); // Another thread receives a POST before that idle snapshot returns.
        policy.refresh(false, beforeGoSnapshot);
        check(locks.held && policy.active(), "stale idle snapshot released a new request's protection");
        policy.refresh(false, policy.revision());
        check(!locks.held, "fresh idle snapshot should release completed request");
    }

    private static void stopRejectsLateCallbacksAndPolls() {
        FakeLocks locks = new FakeLocks();
        WorkProtection policy = new WorkProtection(locks, new FakeClock());
        policy.workStarted();
        long pollRevision = policy.revision();
        policy.stop();
        policy.refresh(true, pollRevision);
        policy.workStarted();
        policy.stop();
        check(!policy.active() && !policy.protectedNow() && !locks.held, "clearing recents must be terminal");
        check(locks.acquired == 1 && locks.released == 1, "shutdown must release once and never reacquire");
    }

    private static void deniedPartialAcquireIsReleasedAndCanRecover() {
        FakeLocks locks = new FakeLocks();
        locks.denied = true;
        WorkProtection policy = new WorkProtection(locks, new FakeClock());
        policy.workStarted();
        check(policy.active() && !policy.protectedNow() && !locks.held, "denied second lock must release partial protection");
        check(locks.released == 1, "partial acquire cleanup was skipped");
        locks.denied = false;
        policy.refresh(true, policy.revision());
        check(locks.held && policy.protectedNow(), "later permission recovery must retry protection");
        policy.stop();
        check(!locks.held && locks.released == 2, "recovered protection leaked at shutdown");
    }

    private static void check(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
