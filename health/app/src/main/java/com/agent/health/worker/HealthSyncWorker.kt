package com.agent.health.worker

import android.content.Context
import androidx.core.app.NotificationCompat
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.agent.health.HealthApp
import com.agent.health.data.HealthConnectManager
import com.agent.health.data.PreferencesManager
import com.agent.health.network.WebhookSender
import java.time.Instant
import java.util.concurrent.TimeUnit

class HealthSyncWorker(
    private val context: Context,
    params: WorkerParameters
) : CoroutineWorker(context, params) {

    private val prefsManager = PreferencesManager(context)
    private val healthConnectManager = HealthConnectManager(context)
    private val webhookSender = WebhookSender()

    override suspend fun doWork(): Result {
        if (!prefsManager.isSyncEnabled) {
            return Result.success()
        }

        val webhookUrl = prefsManager.webhookUrl
        if (webhookUrl.isBlank()) {
            return Result.failure()
        }

        val now = Instant.now()
        val intervalMinutes = prefsManager.intervalMinutes
        var lastSyncTime = Instant.ofEpochMilli(prefsManager.lastSyncTimestamp)

        // 만약 기존 동기화 기록이 없거나 24시간 이전이라면 (intervalMinutes * 2) 이전 시점으로 설정
        if (prefsManager.lastSyncTimestamp == 0L || lastSyncTime.isBefore(now.minusSeconds(86400))) {
            lastSyncTime = now.minusSeconds(intervalMinutes * 60)
        }

        return try {
            val payload = healthConnectManager.readHealthData(lastSyncTime, now)
            val sendResult = webhookSender.sendHealthData(webhookUrl, payload)

            if (sendResult.isSuccess) {
                prefsManager.lastSyncTimestamp = now.toEpochMilli()
                Result.success()
            } else {
                Result.retry()
            }
        } catch (e: Exception) {
            e.printStackTrace()
            Result.retry()
        }
    }

    companion object {
        const val WORK_NAME = "mi_fitness_health_sync_work"

        fun scheduleWork(context: Context, intervalMinutes: Long) {
            val prefs = PreferencesManager(context)
            // 15분 미만 강제 방지 (15분 최소 보장)
            val safeInterval = if (intervalMinutes < PreferencesManager.MIN_INTERVAL_MINUTES) {
                PreferencesManager.MIN_INTERVAL_MINUTES
            } else {
                intervalMinutes
            }

            val syncRequest = PeriodicWorkRequestBuilder<HealthSyncWorker>(
                safeInterval, TimeUnit.MINUTES,
                5, TimeUnit.MINUTES //  flexInterval
            ).build()

            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                WORK_NAME,
                ExistingPeriodicWorkPolicy.UPDATE,
                syncRequest
            )

            prefs.isSyncEnabled = true
            prefs.intervalMinutes = safeInterval
        }

        fun cancelWork(context: Context) {
            val prefs = PreferencesManager(context)
            WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME)
            prefs.isSyncEnabled = false
        }
    }
}
