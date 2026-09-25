package com.agent.health

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build

class HealthApp : Application() {

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                "Health Data Sync Channel",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Mi Fitness 헬스 데이터 동기화를 위한 백그라운드 서비스 채널"
            }
            val notificationManager = getSystemService(NotificationManager::class.java)
            notificationManager?.createNotificationChannel(channel)
        }
    }

    companion object {
        const val CHANNEL_ID = "health_sync_channel"
    }
}
