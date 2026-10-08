package com.wireztna.android

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import androidx.hilt.work.HiltWorkerFactory
import androidx.work.Configuration
import dagger.hilt.android.HiltAndroidApp
import javax.inject.Inject

@HiltAndroidApp
class WireZtnaApp : Application(), Configuration.Provider {

    @Inject
    lateinit var workerFactory: HiltWorkerFactory

    override val workManagerConfiguration: Configuration
        get() = Configuration.Builder()
            .setWorkerFactory(workerFactory)
            .build()

    override fun onCreate() {
        super.onCreate()
        createNotificationChannels()
    }

    private fun createNotificationChannels() {
        val channel = NotificationChannel(
            NOTIFICATION_CHANNEL_VPN,
            getString(R.string.vpn_notification_channel),
            NotificationManager.IMPORTANCE_LOW
        ).apply {
            description = "Shows VPN tunnel status"
            setShowBadge(false)
        }

        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(channel)
    }

    companion object {
        const val NOTIFICATION_CHANNEL_VPN = "vpn_status"
        const val NOTIFICATION_ID_VPN = 1
    }
}
