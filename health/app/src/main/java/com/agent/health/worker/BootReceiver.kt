package com.agent.health.worker

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.agent.health.data.PreferencesManager

class BootReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED || intent.action == Intent.ACTION_MY_PACKAGE_REPLACED) {
            val prefs = PreferencesManager(context)
            if (prefs.isSyncEnabled) {
                HealthSyncWorker.scheduleWork(context, prefs.intervalMinutes)
            }
        }
    }
}
