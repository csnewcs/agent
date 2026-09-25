package com.agent.health.data

import android.content.Context
import android.content.SharedPreferences

class PreferencesManager(context: Context) {

    private val prefs: SharedPreferences =
        context.getSharedPreferences("health_agent_prefs", Context.MODE_PRIVATE)

    var webhookUrl: String
        get() = prefs.getString(KEY_WEBHOOK_URL, DEFAULT_TEST_WEBHOOK_URL) ?: DEFAULT_TEST_WEBHOOK_URL
        set(value) = prefs.edit().putString(KEY_WEBHOOK_URL, value.trim()).apply()

    /**
     * N분 주기 설정 (최소 15분 강제 로직 포함)
     */
    var intervalMinutes: Long
        get() {
            val value = prefs.getLong(KEY_INTERVAL_MINUTES, DEFAULT_INTERVAL_MINUTES)
            return if (value < MIN_INTERVAL_MINUTES) MIN_INTERVAL_MINUTES else value
        }
        set(value) {
            // 15분 미만으로 설정 시 최소 15분으로 강제 제한
            val safeInterval = if (value < MIN_INTERVAL_MINUTES) MIN_INTERVAL_MINUTES else value
            prefs.edit().putLong(KEY_INTERVAL_MINUTES, safeInterval).apply()
        }

    var isSyncEnabled: Boolean
        get() = prefs.getBoolean(KEY_IS_SYNC_ENABLED, false)
        set(value) = prefs.edit().putBoolean(KEY_IS_SYNC_ENABLED, value).apply()

    var lastSyncTimestamp: Long
        get() = prefs.getLong(KEY_LAST_SYNC_TIMESTAMP, 0L)
        set(value) = prefs.edit().putLong(KEY_LAST_SYNC_TIMESTAMP, value).apply()

    companion object {
        private const val KEY_WEBHOOK_URL = "key_webhook_url"
        private const val KEY_INTERVAL_MINUTES = "key_interval_minutes"
        private const val KEY_IS_SYNC_ENABLED = "key_is_sync_enabled"
        private const val KEY_LAST_SYNC_TIMESTAMP = "key_last_sync_timestamp"

        const val DEFAULT_TEST_WEBHOOK_URL = "http://10.0.2.2:5678/webhook-test/dda51830-b125-418e-8e33-7c2358caebff"
        const val MIN_INTERVAL_MINUTES: Long = 15L // 최소 수집 주기 (15분 이하 강제 금지)
        const val DEFAULT_INTERVAL_MINUTES: Long = 15L
    }
}
